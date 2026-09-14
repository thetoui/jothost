package files

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/jothost/panel/agent/internal/fsperm"
)

// Chunk is one slice of a file's content.
type Chunk struct {
	Path string `json:"path"`
	// Offset is where this chunk starts, in bytes.
	Offset int64 `json:"offset"`
	// Data is the raw bytes. The operations layer base64-encodes it, because
	// the protocol is JSON and file content is not text.
	Data []byte `json:"data"`
	// Size is the whole file's size, so a caller can page without a second
	// round trip and can tell a truncated read from a complete one.
	Size int64 `json:"size"`
	// EOF says this chunk reached the end of the file.
	EOF bool `json:"eof"`
}

// Read returns up to length bytes from offset.
//
// A caller reads a file by walking offsets rather than asking for all of it:
// the socket refuses anything over 1 MiB, and a file manager must be able to
// serve a file larger than the Agent's memory.
func (m *Manager) Read(path string, offset int64, length int) (Chunk, error) {
	if !m.Available() {
		return Chunk{}, ErrUnavailable
	}
	if offset < 0 {
		return Chunk{}, errors.New("offset cannot be negative")
	}
	if length <= 0 || length > MaxChunkBytes {
		length = MaxChunkBytes
	}

	// ResolveFile refuses devices, FIFOs and sockets. Reading /dev/zero through
	// a file manager would return bytes forever.
	resolved, err := m.validator.ResolveFile(path)
	if err != nil {
		return Chunk{}, translate(err)
	}

	handle, err := os.Open(resolved) //nolint:gosec // resolved and root-checked
	if err != nil {
		return Chunk{}, translate(err)
	}
	defer func() { _ = handle.Close() }()

	info, err := handle.Stat()
	if err != nil {
		return Chunk{}, fmt.Errorf("stat file: %w", err)
	}

	if offset >= info.Size() {
		return Chunk{Path: resolved, Offset: offset, Size: info.Size(), EOF: true}, nil
	}

	buffer := make([]byte, length)
	read, err := handle.ReadAt(buffer, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return Chunk{}, fmt.Errorf("read file: %w", err)
	}

	return Chunk{
		Path:   resolved,
		Offset: offset,
		Data:   buffer[:read],
		Size:   info.Size(),
		EOF:    offset+int64(read) >= info.Size(),
	}, nil
}

// WriteRequest is one chunk being written.
type WriteRequest struct {
	Path   string
	Offset int64
	Data   []byte
	// Truncate starts the file over. The first chunk of an upload sets it; the
	// rest do not, which is what makes a resumed or chunked upload append
	// rather than repeatedly clobber.
	Truncate bool
	// Final says this is the last chunk, so the file is cut to length. Without
	// it, overwriting a large file with a smaller one would leave the old tail
	// in place.
	Final bool
}

// Write puts one chunk into a file, creating it if needed.
func (m *Manager) Write(req WriteRequest) (Entry, error) {
	if !m.Available() {
		return Entry{}, ErrUnavailable
	}
	if req.Offset < 0 {
		return Entry{}, errors.New("offset cannot be negative")
	}
	if len(req.Data) > MaxChunkBytes {
		return Entry{}, ErrTooLarge
	}

	resolved, err := m.resolveForCreate(req.Path)
	if err != nil {
		return Entry{}, err
	}

	// An existing path must be a regular file. Writing through a symlink is
	// how an upload lands on /etc/passwd, and pathsec only guarantees the link
	// stays inside the root — inside the root is still the wrong file.
	existed := true
	if info, err := os.Lstat(resolved); err == nil {
		if !info.Mode().IsRegular() {
			return Entry{}, ErrNotRegular
		}
	} else if os.IsNotExist(err) {
		existed = false
	} else {
		return Entry{}, translate(err)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if req.Truncate {
		flags |= os.O_TRUNC
	}

	handle, err := os.OpenFile(resolved, flags, newFileMode) //nolint:gosec // resolved and root-checked
	if err != nil {
		return Entry{}, translate(err)
	}
	// A new upload gets the mode stated; the umask would otherwise leave it
	// 0600 and unservable. A file that already existed keeps the mode it had,
	// which somebody may have chosen.
	if !existed {
		if err := fsperm.SetCreated(handle, newFileMode); err != nil {
			_ = handle.Close()
			return Entry{}, err
		}
	}

	if _, err := handle.WriteAt(req.Data, req.Offset); err != nil {
		_ = handle.Close()
		return Entry{}, fmt.Errorf("write file: %w", err)
	}
	if req.Final {
		if err := handle.Truncate(req.Offset + int64(len(req.Data))); err != nil {
			_ = handle.Close()
			return Entry{}, fmt.Errorf("truncate file: %w", err)
		}
	}
	if err := handle.Close(); err != nil {
		return Entry{}, fmt.Errorf("close file: %w", err)
	}

	// Only a file the panel just created takes its parent's owner. An existing
	// file keeps whoever owned it: an upload replacing a file must not quietly
	// change who can read it.
	if !existed {
		if err := inheritOwner(resolved); err != nil {
			return Entry{}, err
		}
	}
	return m.describe(resolved)
}

// Copy duplicates a file or a directory tree.
func (m *Manager) Copy(source, destination string, overwrite bool) (Entry, error) {
	from, to, err := m.resolvePair(source, destination)
	if err != nil {
		return Entry{}, err
	}

	info, err := os.Lstat(from)
	if err != nil {
		return Entry{}, translate(err)
	}

	if _, err := os.Lstat(to); err == nil && !overwrite {
		return Entry{}, ErrExists
	}

	if info.IsDir() {
		if err := copyTree(from, to); err != nil {
			return Entry{}, err
		}
		return m.describe(to)
	}
	if !info.Mode().IsRegular() {
		return Entry{}, ErrNotRegular
	}
	if err := copyFile(from, to, info); err != nil {
		return Entry{}, err
	}
	return m.describe(to)
}

// Move renames a file or directory, falling back to copy-and-delete across
// filesystems.
func (m *Manager) Move(source, destination string, overwrite bool) (Entry, error) {
	from, to, err := m.resolvePair(source, destination)
	if err != nil {
		return Entry{}, err
	}
	// Moving a root away is the same loss as deleting it.
	if err := m.checkRemovable(from); err != nil {
		return Entry{}, err
	}
	if err := m.checkMutable(from); err != nil {
		return Entry{}, err
	}

	if _, err := os.Lstat(to); err == nil {
		if !overwrite {
			return Entry{}, ErrExists
		}
		if err := os.RemoveAll(to); err != nil {
			return Entry{}, fmt.Errorf("replace destination: %w", err)
		}
	}

	if err := os.Rename(from, to); err == nil {
		return m.describe(to)
	} else if !errors.Is(err, syscall.EXDEV) {
		return Entry{}, fmt.Errorf("move: %w", err)
	}

	// Different filesystems: a bind-mounted site directory is enough to cause
	// this, and failing would be an unexplainable refusal to move a file two
	// directories over.
	info, err := os.Lstat(from)
	if err != nil {
		return Entry{}, translate(err)
	}
	if info.IsDir() {
		err = copyTree(from, to)
	} else {
		err = copyFile(from, to, info)
	}
	if err != nil {
		return Entry{}, err
	}
	if err := os.RemoveAll(from); err != nil {
		return Entry{}, fmt.Errorf("remove source after move: %w", err)
	}
	return m.describe(to)
}

// resolvePair validates a source that must exist and a destination that need
// not, and refuses the shapes that would corrupt a tree.
func (m *Manager) resolvePair(source, destination string) (string, string, error) {
	if !m.Available() {
		return "", "", ErrUnavailable
	}

	from, err := m.validator.ResolveExisting(source)
	if err != nil {
		return "", "", translate(err)
	}
	to, err := m.resolveForCreate(destination)
	if err != nil {
		return "", "", err
	}

	if from == to {
		return "", "", ErrExists
	}
	// Copying a directory into itself recurses until the disk is full.
	if isInside(to, from) {
		return "", "", ErrDestInSource
	}
	return from, to, nil
}

// copyFile copies one regular file, preserving mode and ownership.
func copyFile(source, destination string, info os.FileInfo) error {
	in, err := os.Open(source) //nolint:gosec // resolved and root-checked
	if err != nil {
		return translate(err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(destination, //nolint:gosec // resolved and root-checked
		os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return translate(err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy file contents: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close copy: %w", err)
	}

	// The mode is applied explicitly: OpenFile's permission argument is masked
	// by the process umask, so the copy would otherwise be more restrictive
	// than the original for no reason the caller can see.
	if err := os.Chmod(destination, info.Mode().Perm()); err != nil {
		return fmt.Errorf("set copy permissions: %w", err)
	}
	return preserveOwner(source, destination)
}

// copyTree copies a directory recursively.
//
// Symlinks are recreated as symlinks rather than followed. Following them would
// duplicate whatever they point at — and a link to a directory above the source
// would copy an unbounded tree.
func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relative, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)

		info, err := entry.Info()
		if err != nil {
			return err
		}

		switch {
		case entry.IsDir():
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return fmt.Errorf("create directory in copy: %w", err)
			}
			if err := os.Chmod(target, info.Mode().Perm()); err != nil {
				return fmt.Errorf("set directory permissions in copy: %w", err)
			}
			return preserveOwner(current, target)
		case entry.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(current)
			if err != nil {
				return fmt.Errorf("read symlink: %w", err)
			}
			if err := os.Symlink(link, target); err != nil {
				return fmt.Errorf("recreate symlink: %w", err)
			}
			return preserveOwner(current, target)
		case info.Mode().IsRegular():
			return copyFile(current, target, info)
		default:
			// Devices, sockets and FIFOs are skipped. Recreating them needs
			// privileges this operation should not be exercising.
			return nil
		}
	})
}

// preserveOwner gives a copy the same owner as its source.
func preserveOwner(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return translate(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if err := os.Lchown(destination, int(stat.Uid), int(stat.Gid)); err != nil {
		return fmt.Errorf("set copy ownership: %w", err)
	}
	return nil
}
