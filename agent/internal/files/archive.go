package files

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Archive errors a caller can act on.
var (
	ErrArchiveTooLarge  = errors.New("the archive expands to more than this host allows")
	ErrArchiveTooMany   = errors.New("the archive contains more entries than this host allows")
	ErrArchiveEscape    = errors.New("the archive contains a path that escapes the destination")
	ErrArchiveUnsafe    = errors.New("the archive contains an entry type that cannot be extracted safely")
	ErrArchiveNotZip    = errors.New("the file is not a zip archive")
	ErrNothingToArchive = errors.New("no source paths were given")
)

// Extraction bounds.
//
// A zip bomb is a few kilobytes that expands to terabytes. Without a ceiling on
// both the total size and the entry count, one upload fills the host's disk and
// takes every site on it offline.
const (
	MaxExtractedBytes   int64 = 2 << 30 // 2 GiB
	MaxArchiveEntries         = 20000
	MaxCompressionRatio       = 200
)

// ArchiveResult reports what an archive operation did.
type ArchiveResult struct {
	Path    string `json:"path"`
	Entries int    `json:"entries"`
	Size    int64  `json:"size"`
}

// Archive writes the given sources into a zip file.
func (m *Manager) Archive(sources []string, destination string) (ArchiveResult, error) {
	if !m.Available() {
		return ArchiveResult{}, ErrUnavailable
	}
	if len(sources) == 0 {
		return ArchiveResult{}, ErrNothingToArchive
	}

	target, err := m.resolveForCreate(destination)
	if err != nil {
		return ArchiveResult{}, err
	}
	if _, err := os.Lstat(target); err == nil {
		return ArchiveResult{}, ErrExists
	}

	resolved := make([]string, 0, len(sources))
	for _, source := range sources {
		path, err := m.validator.ResolveExisting(source)
		if err != nil {
			return ArchiveResult{}, translate(err)
		}
		// Archiving the destination into itself grows until the disk is full.
		if path == target || isInside(target, path) {
			return ArchiveResult{}, ErrDestInSource
		}
		resolved = append(resolved, path)
	}

	handle, err := os.OpenFile(target, //nolint:gosec // resolved and root-checked
		os.O_CREATE|os.O_EXCL|os.O_WRONLY, newFileMode)
	if err != nil {
		if os.IsExist(err) {
			return ArchiveResult{}, ErrExists
		}
		return ArchiveResult{}, translate(err)
	}

	writer := zip.NewWriter(handle)
	count := 0

	for _, source := range resolved {
		added, err := addToArchive(writer, source)
		if err != nil {
			_ = writer.Close()
			_ = handle.Close()
			_ = os.Remove(target)
			return ArchiveResult{}, err
		}
		count += added
	}

	if err := writer.Close(); err != nil {
		_ = handle.Close()
		_ = os.Remove(target)
		return ArchiveResult{}, fmt.Errorf("finish archive: %w", err)
	}
	if err := handle.Close(); err != nil {
		return ArchiveResult{}, fmt.Errorf("close archive: %w", err)
	}
	if err := inheritOwner(target); err != nil {
		return ArchiveResult{}, err
	}

	info, err := os.Stat(target)
	if err != nil {
		return ArchiveResult{}, translate(err)
	}
	return ArchiveResult{Path: target, Entries: count, Size: info.Size()}, nil
}

// addToArchive writes one source, recursing into directories.
func addToArchive(writer *zip.Writer, source string) (int, error) {
	base := filepath.Dir(source)
	count := 0

	err := filepath.WalkDir(source, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if count >= MaxArchiveEntries {
			return ErrArchiveTooMany
		}

		relative, err := filepath.Rel(base, current)
		if err != nil {
			return err
		}
		// Zip paths are always forward-slashed and always relative.
		name := filepath.ToSlash(relative)

		info, err := entry.Info()
		if err != nil {
			return err
		}

		switch {
		case entry.IsDir():
			if _, err := writer.Create(name + "/"); err != nil {
				return fmt.Errorf("add directory to archive: %w", err)
			}
		case info.Mode().IsRegular():
			header, err := zip.FileInfoHeader(info)
			if err != nil {
				return fmt.Errorf("build archive header: %w", err)
			}
			header.Name = name
			header.Method = zip.Deflate

			target, err := writer.CreateHeader(header)
			if err != nil {
				return fmt.Errorf("add file to archive: %w", err)
			}
			file, err := os.Open(current) //nolint:gosec // inside a resolved tree
			if err != nil {
				return err
			}
			if _, err := io.Copy(target, file); err != nil {
				_ = file.Close()
				return fmt.Errorf("write file into archive: %w", err)
			}
			if err := file.Close(); err != nil {
				return err
			}
		default:
			// Symlinks, devices and sockets are skipped. A symlink stored in an
			// archive is the delivery mechanism for a link that, once
			// extracted somewhere else, points at a file the extractor should
			// never have touched.
			return nil
		}

		count++
		return nil
	})
	if err != nil {
		return count, err
	}
	return count, nil
}

// Extract unpacks a zip archive into a directory.
//
// This is where "zip slip" lives: an archive entry named ../../etc/cron.d/evil
// escapes the destination on any extractor that joins names blindly. Every
// entry is resolved and re-checked against the destination and the manager's
// roots, which is two independent reasons an escaping entry is refused.
func (m *Manager) Extract(archivePath, destination string) (ArchiveResult, error) {
	if !m.Available() {
		return ArchiveResult{}, ErrUnavailable
	}

	source, err := m.validator.ResolveFile(archivePath)
	if err != nil {
		return ArchiveResult{}, translate(err)
	}

	target, err := m.validator.ResolveDir(destination)
	if err != nil {
		// A destination that does not exist yet is created, because extracting
		// into a new folder is the normal thing to want.
		created, createErr := m.Mkdir(destination, true)
		if createErr != nil {
			return ArchiveResult{}, createErr
		}
		target = created.Path
	}
	if err := m.checkMutable(target); err != nil {
		return ArchiveResult{}, err
	}

	reader, err := zip.OpenReader(source)
	if err != nil {
		return ArchiveResult{}, ErrArchiveNotZip
	}
	defer func() { _ = reader.Close() }()

	if len(reader.File) > MaxArchiveEntries {
		return ArchiveResult{}, ErrArchiveTooMany
	}

	// The declared sizes are checked before a single byte is written. They are
	// only a claim — the copy below is bounded independently — but a bomb that
	// announces itself can be refused without touching the disk at all.
	var declared int64
	for _, entry := range reader.File {
		size := int64(entry.UncompressedSize64)
		if size < 0 || declared > MaxExtractedBytes-size {
			return ArchiveResult{}, ErrArchiveTooLarge
		}
		declared += size
	}

	var written int64
	count := 0

	for _, entry := range reader.File {
		path, err := m.safeExtractPath(target, entry.Name)
		if err != nil {
			return ArchiveResult{}, err
		}

		mode := entry.Mode()
		switch {
		case mode&fs.ModeSymlink != 0, mode&fs.ModeDevice != 0,
			mode&fs.ModeNamedPipe != 0, mode&fs.ModeSocket != 0:
			// Refused rather than skipped. A symlink extracted into a document
			// root can point anywhere, and the next write through it lands
			// outside every check this package makes.
			return ArchiveResult{}, ErrArchiveUnsafe
		case entry.FileInfo().IsDir():
			if err := os.MkdirAll(path, newDirMode); err != nil {
				return ArchiveResult{}, fmt.Errorf("create extracted directory: %w", err)
			}
			if err := inheritOwner(path); err != nil {
				return ArchiveResult{}, err
			}
		default:
			size, err := m.extractFile(entry, path, MaxExtractedBytes-written)
			if err != nil {
				return ArchiveResult{}, err
			}
			written += size
		}
		count++
	}

	return ArchiveResult{Path: target, Entries: count, Size: written}, nil
}

// safeExtractPath turns an archive entry name into a path inside the
// destination, or refuses it.
func (m *Manager) safeExtractPath(destination, name string) (string, error) {
	if name == "" {
		return "", ErrArchiveEscape
	}
	if strings.ContainsRune(name, '\x00') {
		return "", ErrArchiveEscape
	}
	// An absolute name would make filepath.Join discard the destination.
	cleaned := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") {
		return "", ErrArchiveEscape
	}

	candidate := filepath.Join(destination, cleaned)
	if candidate != destination && !isInside(candidate, destination) {
		return "", ErrArchiveEscape
	}

	// Checked a second time against the manager's own roots, and through
	// symlink resolution: an earlier entry in the same archive may have created
	// a directory that is a link, and the join above cannot see that.
	resolved, err := m.validator.Resolve(candidate)
	if err != nil {
		return "", ErrArchiveEscape
	}
	if resolved != destination && !isInside(resolved, destination) {
		return "", ErrArchiveEscape
	}
	return resolved, nil
}

// extractFile writes one archive entry, bounded by the remaining budget.
func (m *Manager) extractFile(entry *zip.File, path string, budget int64) (int64, error) {
	if budget <= 0 {
		return 0, ErrArchiveTooLarge
	}
	if err := os.MkdirAll(filepath.Dir(path), newDirMode); err != nil {
		return 0, fmt.Errorf("create directory for extracted file: %w", err)
	}

	source, err := entry.Open()
	if err != nil {
		return 0, fmt.Errorf("open archive entry: %w", err)
	}
	defer func() { _ = source.Close() }()

	target, err := os.OpenFile(path, //nolint:gosec // resolved against the destination
		os.O_CREATE|os.O_TRUNC|os.O_WRONLY, newFileMode)
	if err != nil {
		return 0, translate(err)
	}

	// One byte past the budget is read deliberately: a copy that stops exactly
	// at the limit cannot tell a file that just fits from one that does not.
	written, err := io.Copy(target, io.LimitReader(source, budget+1))
	if err != nil {
		_ = target.Close()
		_ = os.Remove(path)
		return 0, fmt.Errorf("write extracted file: %w", err)
	}
	if written > budget {
		_ = target.Close()
		_ = os.Remove(path)
		return 0, ErrArchiveTooLarge
	}
	if err := target.Close(); err != nil {
		return 0, fmt.Errorf("close extracted file: %w", err)
	}

	// The archive's own mode is not applied. An archive can declare 0777, or
	// setuid, and an extractor that honours it is handing the uploader whatever
	// permissions they chose to write into the zip.
	if err := inheritOwner(path); err != nil {
		return 0, err
	}
	return written, nil
}
