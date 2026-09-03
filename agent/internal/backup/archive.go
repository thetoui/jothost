package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Writing and reading the archive format.
//
// tar.gz rather than zip, which is what the file manager uses. The file manager
// archives things a person is going to download and open on a laptop, where zip
// is what every operating system understands. This archives a Linux host's own
// filesystem, where the properties that matter are different: tar records the
// mode, the ownership and the symlinks that make a website's directory work,
// and zip does not carry them portably.
//
// Streaming in one pass, in both directions. An archive of a busy host is
// larger than its memory, so nothing here ever holds an archive, a member, or a
// directory listing of unbounded size — with one exception, the manifest, which
// is bounded explicitly.

// writer builds an archive while digesting it.
type writer struct {
	file *os.File
	gzip *gzip.Writer
	tar  *tar.Writer
	// sum digests the compressed bytes as they are written, which is what a
	// verify recomputes. Digesting the *uncompressed* stream instead would
	// mean a verify had to decompress before it could say anything, and a
	// truncated archive would fail as "corrupt gzip" rather than as a digest
	// that did not match.
	sum     hash.Hash
	written int64
	members int
}

// newWriter creates an archive at path, which must not exist.
func newWriter(destination string) (*writer, error) {
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create archive: %w", err)
	}
	sum := sha256.New()
	// Every byte that reaches the file is digested on the way past, so the
	// digest cannot disagree with what was stored.
	counted := &countingWriter{inner: io.MultiWriter(file, sum)}
	// BestSpeed rather than the default: a backup is bound by disk and network,
	// and the highest compression level costs several times the CPU for a few
	// per cent — on a host that is also serving the websites being backed up.
	gz, err := gzip.NewWriterLevel(counted, gzip.BestSpeed)
	if err != nil {
		_ = file.Close()
		_ = os.Remove(destination)
		return nil, fmt.Errorf("create archive: %w", err)
	}

	w := &writer{file: file, gzip: gz, tar: tar.NewWriter(gz), sum: sum}
	counted.total = &w.written
	return w, nil
}

// countingWriter records how many bytes have reached the file.
type countingWriter struct {
	inner io.Writer
	total *int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.inner.Write(p)
	if c.total != nil {
		*c.total += int64(n)
	}
	return n, err
}

// addFile writes one regular file, returning its member record.
func (w *writer) addFile(name, source string, info fs.FileInfo) (Member, error) {
	if w.members >= MaxMembers {
		return Member{}, fmt.Errorf("%w: more than %d entries", ErrTooLarge, MaxMembers)
	}

	handle, err := os.Open(source) //nolint:gosec // resolved against the site root
	if err != nil {
		return Member{}, fmt.Errorf("read %s: %w", name, err)
	}
	defer func() { _ = handle.Close() }()

	header := &tar.Header{
		Name:     name,
		Mode:     int64(info.Mode().Perm()),
		Size:     info.Size(),
		ModTime:  info.ModTime(),
		Typeflag: tar.TypeReg,
	}
	if err := w.tar.WriteHeader(header); err != nil {
		return Member{}, fmt.Errorf("write %s: %w", name, err)
	}

	sum := sha256.New()
	copied, err := io.Copy(io.MultiWriter(w.tar, sum), handle)
	if err != nil {
		return Member{}, fmt.Errorf("write %s: %w", name, err)
	}
	if copied != info.Size() {
		// tar records the size in the header, so a file that changed length
		// while it was being read produces an archive tar itself will reject.
		// Saying so here names the file; discovering it at restore does not.
		return Member{}, fmt.Errorf("%w: %s changed while it was being read",
			ErrArchiveMalformed, name)
	}

	w.members++
	return Member{
		Path:     name,
		Size:     copied,
		Checksum: hex.EncodeToString(sum.Sum(nil)),
		Mode:     uint32(info.Mode().Perm()),
	}, nil
}

// addDir writes a directory entry.
func (w *writer) addDir(name string, info fs.FileInfo) (Member, error) {
	header := &tar.Header{
		Name:     strings.TrimSuffix(name, "/") + "/",
		Mode:     int64(info.Mode().Perm()),
		ModTime:  info.ModTime(),
		Typeflag: tar.TypeDir,
	}
	if err := w.tar.WriteHeader(header); err != nil {
		return Member{}, fmt.Errorf("write %s: %w", name, err)
	}
	w.members++
	return Member{Path: header.Name, Mode: uint32(info.Mode().Perm())}, nil
}

// addSymlink writes a symlink entry.
//
// Symlinks are archived as links rather than followed. Following them would
// duplicate whatever they point at — and a link out of the document root would
// pull the rest of the host into a website's backup.
func (w *writer) addSymlink(name, target string, info fs.FileInfo) (Member, error) {
	header := &tar.Header{
		Name:     name,
		Linkname: target,
		Mode:     int64(info.Mode().Perm()),
		ModTime:  info.ModTime(),
		Typeflag: tar.TypeSymlink,
	}
	if err := w.tar.WriteHeader(header); err != nil {
		return Member{}, fmt.Errorf("write %s: %w", name, err)
	}
	w.members++
	return Member{Path: name, Mode: uint32(info.Mode().Perm()), Link: target}, nil
}

// addManifest writes the manifest as the archive's last member.
func (w *writer) addManifest(manifest Manifest) error {
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	header := &tar.Header{
		Name:     ManifestName,
		Mode:     0o600,
		Size:     int64(len(encoded)),
		ModTime:  manifest.CreatedAt,
		Typeflag: tar.TypeReg,
	}
	if err := w.tar.WriteHeader(header); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	if _, err := w.tar.Write(encoded); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	w.members++
	return nil
}

// close finishes the archive and returns its size and digest.
func (w *writer) close() (int64, string, error) {
	if err := w.tar.Close(); err != nil {
		_ = w.gzip.Close()
		_ = w.file.Close()
		return 0, "", fmt.Errorf("finish archive: %w", err)
	}
	if err := w.gzip.Close(); err != nil {
		_ = w.file.Close()
		return 0, "", fmt.Errorf("finish archive: %w", err)
	}
	// Sync before the size is read. Without it the archive can be reported as
	// complete while its last blocks are still in the page cache, which is
	// precisely the failure a verify is supposed to catch and would instead
	// pass — the verify would read the same cache.
	if err := w.file.Sync(); err != nil {
		_ = w.file.Close()
		return 0, "", fmt.Errorf("flush archive: %w", err)
	}
	if err := w.file.Close(); err != nil {
		return 0, "", fmt.Errorf("close archive: %w", err)
	}
	return w.written, hex.EncodeToString(w.sum.Sum(nil)), nil
}

// abandon closes and removes a half-written archive.
func (w *writer) abandon() {
	_ = w.tar.Close()
	_ = w.gzip.Close()
	name := w.file.Name()
	_ = w.file.Close()
	_ = os.Remove(name)
}

// walkTree adds a directory tree to the archive under prefix.
//
// It returns the members written and the bytes of file content. Errors reading
// one file are fatal rather than skipped: a backup that quietly omitted the
// file it could not read would be a backup somebody trusted and should not.
func (w *writer) walkTree(root, prefix string) ([]Member, int64, error) {
	members := []Member{}
	var bytes int64

	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("read %s: %w", current, err)
		}

		relative, err := filepath.Rel(root, current)
		if err != nil {
			return fmt.Errorf("read %s: %w", current, err)
		}
		if relative == "." {
			return nil
		}
		name := path.Join(prefix, filepath.ToSlash(relative))

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("read %s: %w", current, err)
		}

		switch {
		case entry.IsDir():
			member, err := w.addDir(name, info)
			if err != nil {
				return err
			}
			members = append(members, member)
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(current)
			if err != nil {
				return fmt.Errorf("read %s: %w", current, err)
			}
			member, err := w.addSymlink(name, target, info)
			if err != nil {
				return err
			}
			members = append(members, member)
		case info.Mode().IsRegular():
			if info.Size() > MaxMemberBytes {
				return fmt.Errorf("%w: %s is %d bytes", ErrTooLarge, name, info.Size())
			}
			member, err := w.addFile(name, current, info)
			if err != nil {
				return err
			}
			members = append(members, member)
			bytes += member.Size
		default:
			// Sockets, devices and fifos. A website's directory should not
			// contain them, and archiving one would produce something a
			// restore could not recreate without privileges it should not use.
			return nil
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}

	sort.Slice(members, func(i, j int) bool { return members[i].Path < members[j].Path })
	return members, bytes, nil
}

// reader walks an archive.
type reader struct {
	file *os.File
	gzip *gzip.Reader
	tar  *tar.Reader
}

// openArchive opens an archive for reading.
func openArchive(source string) (*reader, error) {
	file, err := os.Open(source) //nolint:gosec // an agent-owned staging path
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrArchiveMalformed, err)
	}
	gz, err := gzip.NewReader(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %s", ErrArchiveMalformed, err)
	}
	return &reader{file: file, gzip: gz, tar: tar.NewReader(gz)}, nil
}

func (r *reader) close() {
	_ = r.gzip.Close()
	_ = r.file.Close()
}

// digestFile returns the SHA-256 of a file on disk.
func digestFile(path string) (string, int64, error) {
	handle, err := os.Open(path) //nolint:gosec // an agent-owned staging path
	if err != nil {
		return "", 0, fmt.Errorf("read archive: %w", err)
	}
	defer func() { _ = handle.Close() }()

	sum := sha256.New()
	size, err := io.Copy(sum, handle)
	if err != nil {
		return "", 0, fmt.Errorf("read archive: %w", err)
	}
	return hex.EncodeToString(sum.Sum(nil)), size, nil
}

// safeMemberPath turns an archive member's name into a path under root.
//
// This is the tar equivalent of the zip-slip check in the file manager, and it
// is the single most important function in a restore: an archive is data from
// somewhere else, and a member called "../../etc/shadow" is how a restore
// becomes a way to write anywhere as root.
//
// It refuses absolute names, dot segments, backslashes, and anything that does
// not land inside root after cleaning — belt and braces, because each of the
// three has been the bug in somebody's tar extractor.
func safeMemberPath(root, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: an archive entry has no name", ErrArchiveMalformed)
	}
	if strings.ContainsAny(name, "\\\x00") {
		return "", fmt.Errorf("%w: %q contains a backslash or a null byte",
			ErrArchiveMalformed, name)
	}
	if path.IsAbs(name) {
		return "", fmt.Errorf("%w: %q is an absolute path", ErrArchiveMalformed, name)
	}
	for _, segment := range strings.Split(strings.Trim(name, "/"), "/") {
		if segment == ".." {
			return "", fmt.Errorf("%w: %q escapes the destination", ErrArchiveMalformed, name)
		}
	}

	cleanedRoot := filepath.Clean(root)
	target := filepath.Join(cleanedRoot, filepath.FromSlash(path.Clean(name)))
	if target != cleanedRoot && !strings.HasPrefix(target, cleanedRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %q escapes the destination", ErrArchiveMalformed, name)
	}
	return target, nil
}

// safeLinkTarget refuses a symlink that would point out of root.
//
// A symlink is written without following it, so the danger is not the link
// itself but what a *later* member does through it: an archive containing
// "site/config -> /etc" followed by "site/config/passwd" writes to /etc/passwd
// through a path that passed every check on its own.
func safeLinkTarget(root, name, target string) error {
	if target == "" {
		return fmt.Errorf("%w: %q is a symlink to nothing", ErrArchiveMalformed, name)
	}
	if path.IsAbs(target) {
		return fmt.Errorf("%w: %q points outside the destination", ErrArchiveMalformed, name)
	}

	linkPath, err := safeMemberPath(root, name)
	if err != nil {
		return err
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(linkPath), filepath.FromSlash(target)))
	cleanedRoot := filepath.Clean(root)
	if resolved != cleanedRoot && !strings.HasPrefix(resolved, cleanedRoot+string(os.PathSeparator)) {
		return fmt.Errorf("%w: %q points outside the destination", ErrArchiveMalformed, name)
	}
	return nil
}

// readManifest scans an archive and returns its manifest.
func readManifest(source string) (Manifest, error) {
	archive, err := openArchive(source)
	if err != nil {
		return Manifest{}, err
	}
	defer archive.close()

	for {
		header, err := archive.tar.Next()
		if err == io.EOF {
			return Manifest{}, fmt.Errorf("%w: it has no %s", ErrArchiveMalformed, ManifestName)
		}
		if err != nil {
			return Manifest{}, fmt.Errorf("%w: %s", ErrArchiveMalformed, err)
		}
		if header.Name != ManifestName {
			continue
		}
		if header.Size > MaxManifestBytes {
			return Manifest{}, fmt.Errorf("%w: the manifest is %d bytes",
				ErrTooLarge, header.Size)
		}
		encoded, err := io.ReadAll(io.LimitReader(archive.tar, MaxManifestBytes))
		if err != nil {
			return Manifest{}, fmt.Errorf("%w: %s", ErrArchiveMalformed, err)
		}
		var manifest Manifest
		if err := json.Unmarshal(encoded, &manifest); err != nil {
			return Manifest{}, fmt.Errorf("%w: the manifest is not readable: %s",
				ErrArchiveMalformed, err)
		}
		return manifest, nil
	}
}
