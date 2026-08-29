// Package files implements the panel's file manager against the real
// filesystem.
//
// Every operation here runs as root, so the whole package is written on the
// assumption that each path came from a browser and is hostile. Paths are
// resolved through pathsec (CLAUDE.md section 5), which normalises them,
// refuses traversal, and refuses anything that escapes its root through a
// symlink. Nothing in this package builds a shell command.
package files

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"

	"github.com/jothost/panel/agent/internal/pathsec"
)

// Errors a caller distinguishes. Everything else is internal.
var (
	ErrUnavailable   = errors.New("file management is not available on this host")
	ErrNotFound      = errors.New("no such file or directory")
	ErrExists        = errors.New("a file or directory already exists at that path")
	ErrNotDirectory  = errors.New("path is not a directory")
	ErrNotRegular    = errors.New("path is not a regular file")
	ErrNotEmpty      = errors.New("directory is not empty")
	ErrTooLarge      = errors.New("file is larger than this operation allows")
	ErrInvalidMode   = errors.New("permissions are not a valid mode")
	ErrUnsafeMode    = errors.New("setuid, setgid and sticky bits cannot be set from the panel")
	ErrProtectedPath = errors.New("this path is managed by the panel and cannot be changed here")
	ErrDestInSource  = errors.New("a directory cannot be copied or moved into itself")
	ErrRootProtected = errors.New("the file manager's own root cannot be removed or moved")
)

// MaxChunkBytes bounds one read or write.
//
// The Agent's socket refuses a request or response over 1 MiB, and file
// content travels base64-encoded, which costs a third more. 256 KiB leaves the
// encoded chunk near 342 KiB and the rest of the budget for the envelope.
const MaxChunkBytes = 256 * 1024

// Listing bounds. A directory can hold hundreds of thousands of entries, and a
// single response has to fit the socket's limit, so a listing is paged.
const (
	DefaultListLimit = 500
	MaxListLimit     = 2000
)

// EditableFileLimit bounds a whole-file read.
//
// This is what a text editor is offered. Beyond it a file is downloadable but
// not editable, which is far better than sending a browser a 2 GB "text file".
const EditableFileLimit = 2 * 1024 * 1024

// newDirMode and newFileMode are what the panel creates.
//
// Group-readable, not world: a document root is served by a web server in the
// site's group, and nothing outside that group has business reading it.
const (
	newDirMode  os.FileMode = 0o750
	newFileMode os.FileMode = 0o640
)

// Entry describes one directory entry.
//
// Symlinks are reported as symlinks and never silently followed: showing a
// link's target as though it were the link's own content is how a file manager
// misleads someone into deleting the wrong thing.
type Entry struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Type     string    `json:"type"`
	Size     int64     `json:"size"`
	Mode     string    `json:"mode"`
	Modified time.Time `json:"modified"`
	Owner    string    `json:"owner"`
	Group    string    `json:"group"`
	UID      int       `json:"uid"`
	GID      int       `json:"gid"`
	// Target is a symlink's destination, empty for everything else.
	Target string `json:"target,omitempty"`
	// TargetInsideRoot says whether a symlink points somewhere the panel is
	// allowed to go. A link out of the root is shown but cannot be followed.
	TargetInsideRoot bool `json:"target_inside_root,omitempty"`
	Editable         bool `json:"editable"`
}

// Entry types.
const (
	TypeFile      = "file"
	TypeDirectory = "directory"
	TypeSymlink   = "symlink"
	TypeOther     = "other"
)

// Listing is one page of a directory.
type Listing struct {
	Path    string  `json:"path"`
	Parent  string  `json:"parent"`
	Entries []Entry `json:"entries"`
	Total   int     `json:"total"`
	Offset  int     `json:"offset"`
	Limit   int     `json:"limit"`
	// Truncated says a further page exists, so a caller knows the view is
	// partial rather than assuming it saw the whole directory.
	Truncated bool `json:"truncated"`
}

// Manager performs file operations inside its allowed roots.
type Manager struct {
	validator *pathsec.Validator
	roots     []string
	// protected are paths the panel manages itself and must not let a caller
	// edit through the file manager.
	protected []string
}

// ManagerOptions configures a Manager.
type ManagerOptions struct {
	// Roots are the directories the file manager may touch. Typically the site
	// root alone: a file manager that can reach / is a root shell with a nicer
	// interface.
	Roots []string
	// Protected are paths inside those roots that the panel owns. A caller can
	// see them but cannot write to or delete them.
	Protected []string
}

// NewManager builds a Manager.
//
// A Manager with no roots is unavailable rather than unrestricted: a
// misconfiguration must fail closed.
func NewManager(opts ManagerOptions) *Manager {
	if len(opts.Roots) == 0 {
		return &Manager{}
	}

	validator, err := pathsec.NewValidator(opts.Roots...)
	if err != nil {
		return &Manager{}
	}

	protected := make([]string, 0, len(opts.Protected))
	for _, path := range opts.Protected {
		protected = append(protected, filepath.Clean(path))
	}

	return &Manager{validator: validator, roots: validator.Roots(), protected: protected}
}

// Available reports whether file management is configured.
func (m *Manager) Available() bool { return m.validator != nil }

// Roots returns the directories this manager may touch.
func (m *Manager) Roots() []string {
	if m.validator == nil {
		return nil
	}
	return m.validator.Roots()
}

// List returns one page of a directory, directories first.
func (m *Manager) List(path string, offset, limit int) (Listing, error) {
	if !m.Available() {
		return Listing{}, ErrUnavailable
	}

	resolved, err := m.validator.ResolveDir(path)
	if err != nil {
		return Listing{}, translate(err)
	}

	raw, err := os.ReadDir(resolved)
	if err != nil {
		return Listing{}, fmt.Errorf("read directory: %w", err)
	}

	entries := make([]Entry, 0, len(raw))
	for _, item := range raw {
		entry, err := m.describe(filepath.Join(resolved, item.Name()))
		if err != nil {
			// An entry that vanished between the listing and the stat is not an
			// error for the whole directory; it is simply no longer there.
			continue
		}
		entries = append(entries, entry)
	}

	// Directories first, then case-insensitive by name: the order a person
	// expects, and stable so paging cannot repeat or skip an entry.
	sort.SliceStable(entries, func(i, j int) bool {
		left, right := entries[i], entries[j]
		if (left.Type == TypeDirectory) != (right.Type == TypeDirectory) {
			return left.Type == TypeDirectory
		}
		return foldLess(left.Name, right.Name)
	})

	total := len(entries)
	offset, limit = pageBounds(offset, limit, total)
	page := entries[offset:min(offset+limit, total)]

	parent := filepath.Dir(resolved)
	if !m.inRoot(parent) {
		// The root itself has no parent the caller may navigate to.
		parent = ""
	}

	return Listing{
		Path:      resolved,
		Parent:    parent,
		Entries:   page,
		Total:     total,
		Offset:    offset,
		Limit:     limit,
		Truncated: offset+len(page) < total,
	}, nil
}

// Stat describes a single path.
func (m *Manager) Stat(path string) (Entry, error) {
	if !m.Available() {
		return Entry{}, ErrUnavailable
	}

	resolved, err := m.validator.ResolveExisting(path)
	if err != nil {
		return Entry{}, translate(err)
	}
	return m.describe(resolved)
}

// Mkdir creates a directory, and its parents when asked.
//
// It is idempotent for an existing directory (CLAUDE.md section 17) but not for
// an existing file: silently succeeding there would tell a caller it had a
// directory when it does not.
func (m *Manager) Mkdir(path string, parents bool) (Entry, error) {
	resolved, err := m.resolveForCreate(path)
	if err != nil {
		return Entry{}, err
	}

	if info, err := os.Lstat(resolved); err == nil {
		if info.IsDir() {
			return m.describe(resolved)
		}
		return Entry{}, ErrExists
	}

	if parents {
		err = os.MkdirAll(resolved, newDirMode)
	} else {
		err = os.Mkdir(resolved, newDirMode)
	}
	if err != nil {
		if os.IsExist(err) {
			return Entry{}, ErrExists
		}
		if os.IsNotExist(err) {
			return Entry{}, ErrNotFound
		}
		return Entry{}, fmt.Errorf("create directory: %w", err)
	}

	// Created as root by default, which would leave a directory inside a
	// customer's site that the site itself cannot write to.
	if err := inheritOwner(resolved); err != nil {
		return Entry{}, err
	}
	return m.describe(resolved)
}

// Create makes an empty file.
//
// It refuses to replace an existing one. Truncating a file because a caller
// asked to "create" it is how a file manager destroys work with one click.
func (m *Manager) Create(path string) (Entry, error) {
	resolved, err := m.resolveForCreate(path)
	if err != nil {
		return Entry{}, err
	}

	handle, err := os.OpenFile(resolved, os.O_CREATE|os.O_EXCL|os.O_WRONLY, newFileMode)
	if err != nil {
		if os.IsExist(err) {
			return Entry{}, ErrExists
		}
		if os.IsNotExist(err) {
			return Entry{}, ErrNotFound
		}
		return Entry{}, fmt.Errorf("create file: %w", err)
	}
	if err := handle.Close(); err != nil {
		return Entry{}, fmt.Errorf("close new file: %w", err)
	}

	if err := inheritOwner(resolved); err != nil {
		return Entry{}, err
	}
	return m.describe(resolved)
}

// Delete removes a file or directory.
//
// A non-empty directory needs recursive set explicitly: one mis-click must not
// be able to erase a site.
func (m *Manager) Delete(path string, recursive bool) error {
	if !m.Available() {
		return ErrUnavailable
	}

	resolved, err := m.validator.ResolveExisting(path)
	if err != nil {
		return translate(err)
	}
	if err := m.checkRemovable(resolved); err != nil {
		return err
	}
	if err := m.checkMutable(resolved); err != nil {
		return err
	}

	info, err := os.Lstat(resolved)
	if err != nil {
		return translate(err)
	}

	if info.IsDir() {
		empty, err := isEmptyDir(resolved)
		if err != nil {
			return err
		}
		if !empty && !recursive {
			return ErrNotEmpty
		}
		if err := os.RemoveAll(resolved); err != nil {
			return fmt.Errorf("delete directory: %w", err)
		}
		return nil
	}

	if err := os.Remove(resolved); err != nil {
		return fmt.Errorf("delete file: %w", err)
	}
	return nil
}

// Chmod changes a path's permission bits.
//
// Ownership is deliberately not changeable here: a panel that can hand any file
// to any account is a privilege escalation tool, and every file this package
// creates already inherits the right owner.
func (m *Manager) Chmod(path string, mode string, recursive bool) (Entry, error) {
	if !m.Available() {
		return Entry{}, ErrUnavailable
	}

	parsed, err := ParseMode(mode)
	if err != nil {
		return Entry{}, err
	}

	resolved, err := m.validator.ResolveExisting(path)
	if err != nil {
		return Entry{}, translate(err)
	}
	if err := m.checkMutable(resolved); err != nil {
		return Entry{}, err
	}

	if !recursive {
		if err := os.Chmod(resolved, parsed); err != nil {
			return Entry{}, fmt.Errorf("change permissions: %w", err)
		}
		return m.describe(resolved)
	}

	err = filepath.WalkDir(resolved, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Symlinks are skipped rather than followed: chmod follows the link,
		// so a link planted in an uploaded archive would otherwise let a
		// recursive change reach a file outside the tree entirely.
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		return os.Chmod(current, parsed)
	})
	if err != nil {
		return Entry{}, fmt.Errorf("change permissions: %w", err)
	}
	return m.describe(resolved)
}

// ParseMode reads an octal permission string such as "0644" or "755".
//
// Only the nine permission bits are accepted. setuid, setgid and the sticky bit
// are refused: a setuid binary written into a document root by a web panel is
// a local root exploit waiting for someone to run it.
func ParseMode(value string) (os.FileMode, error) {
	if value == "" {
		return 0, ErrInvalidMode
	}
	parsed, err := strconv.ParseUint(value, 8, 32)
	if err != nil {
		return 0, ErrInvalidMode
	}
	if parsed&^0o777 != 0 {
		if parsed&0o7000 != 0 && parsed&^0o7777 == 0 {
			return 0, ErrUnsafeMode
		}
		return 0, ErrInvalidMode
	}
	return os.FileMode(parsed), nil
}

// resolveForCreate validates a path that does not exist yet and checks that its
// parent is a directory the caller may write to.
func (m *Manager) resolveForCreate(path string) (string, error) {
	if !m.Available() {
		return "", ErrUnavailable
	}

	resolved, err := m.validator.Resolve(path)
	if err != nil {
		return "", translate(err)
	}
	if err := m.checkMutable(resolved); err != nil {
		return "", err
	}

	// The parent must exist and be a directory, and must itself be inside the
	// root — resolving the child alone would accept a path whose parent is a
	// symlink pointing out of the tree.
	parent := filepath.Dir(resolved)
	if _, err := m.validator.ResolveDir(parent); err != nil {
		return "", translate(err)
	}
	return resolved, nil
}

// checkMutable refuses paths the panel manages itself.
//
// A site's document root can be edited freely; the nginx vhost and the FPM pool
// that make it work cannot, because a file manager is not the place to break
// the web server for every other site on the host.
func (m *Manager) checkMutable(resolved string) error {
	for _, path := range m.protected {
		if resolved == path || isInside(resolved, path) {
			return ErrProtectedPath
		}
	}
	return nil
}

// inRoot reports whether a resolved path is inside an allowed root.
func (m *Manager) inRoot(path string) bool {
	for _, root := range m.roots {
		if path == root || isInside(path, root) {
			return true
		}
	}
	return false
}

// isRoot reports whether a resolved path is one of the allowed roots itself.
func (m *Manager) isRoot(path string) bool {
	for _, root := range m.roots {
		if path == root {
			return true
		}
	}
	return false
}

// checkRemovable refuses an operation that would destroy an allowed root.
//
// pathsec accepts a root as being inside itself, which is right for reading and
// for creating things in it — and catastrophic for delete. `/var/www` passed
// every check and a single recursive delete took every website on the host with
// it. A root is the boundary of what the file manager may touch, not a thing
// inside that boundary.
func (m *Manager) checkRemovable(resolved string) error {
	if m.isRoot(resolved) {
		return ErrRootProtected
	}
	return nil
}

// describe builds an Entry from a path that has already been validated.
func (m *Manager) describe(resolved string) (Entry, error) {
	info, err := os.Lstat(resolved)
	if err != nil {
		return Entry{}, translate(err)
	}

	entry := Entry{
		Name:     filepath.Base(resolved),
		Path:     resolved,
		Size:     info.Size(),
		Mode:     fmt.Sprintf("%04o", info.Mode().Perm()),
		Modified: info.ModTime().UTC(),
	}

	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		entry.Type = TypeSymlink
		if target, err := os.Readlink(resolved); err == nil {
			entry.Target = target
			absolute := target
			if !filepath.IsAbs(absolute) {
				absolute = filepath.Join(filepath.Dir(resolved), target)
			}
			if _, err := m.validator.Resolve(absolute); err == nil {
				entry.TargetInsideRoot = true
			}
		}
	case info.IsDir():
		entry.Type = TypeDirectory
	case info.Mode().IsRegular():
		entry.Type = TypeFile
		entry.Editable = info.Size() <= EditableFileLimit
	default:
		// Devices, sockets and FIFOs are listed so they are visible, but they
		// are never treated as readable content.
		entry.Type = TypeOther
	}

	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		entry.UID = int(stat.Uid)
		entry.GID = int(stat.Gid)
		entry.Owner = lookupUser(entry.UID)
		entry.Group = lookupGroup(entry.GID)
	}
	return entry, nil
}

// inheritOwner gives a newly created path the ownership of its parent.
//
// Without this the panel writes root-owned files into a customer's document
// root: PHP cannot write to them, the site's own account cannot fix them, and
// the failure surfaces later as a permission error nobody can explain.
func inheritOwner(path string) error {
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("stat parent directory: %w", err)
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		// Not Linux. Leaving ownership alone is the honest outcome; the caller
		// is not told a change happened that did not.
		return nil
	}
	if err := os.Lchown(path, int(stat.Uid), int(stat.Gid)); err != nil {
		return fmt.Errorf("set ownership: %w", err)
	}
	return nil
}

// isEmptyDir reports whether a directory has no entries.
func isEmptyDir(path string) (bool, error) {
	handle, err := os.Open(path) //nolint:gosec // path is resolved and root-checked
	if err != nil {
		return false, fmt.Errorf("open directory: %w", err)
	}
	defer func() { _ = handle.Close() }()

	names, err := handle.Readdirnames(1)
	if err != nil && err.Error() != "EOF" {
		if len(names) == 0 {
			return true, nil
		}
		return false, nil
	}
	return len(names) == 0, nil
}

// isInside reports whether path lies within dir.
func isInside(path, dir string) bool {
	if dir == "" {
		return false
	}
	// The separator matters: without it "/var/wwwevil" counts as inside
	// "/var/www".
	return len(path) > len(dir) &&
		path[:len(dir)] == dir &&
		path[len(dir)] == filepath.Separator
}

// pageBounds clamps caller-supplied paging to something sane.
func pageBounds(offset, limit, total int) (int, int) {
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	return offset, limit
}

// translate maps filesystem and pathsec errors onto this package's errors.
//
// Every path failure becomes the same small set: a caller probing for the
// layout must not be able to tell "outside the root" from "does not exist".
func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pathsec.ErrNotFound), os.IsNotExist(err):
		return ErrNotFound
	case errors.Is(err, pathsec.ErrNotDirectory):
		return ErrNotDirectory
	case errors.Is(err, pathsec.ErrNotRegular):
		return ErrNotRegular
	default:
		return err
	}
}

// foldLess compares names case-insensitively without allocating.
func foldLess(left, right string) bool {
	for i := 0; i < len(left) && i < len(right); i++ {
		a, b := lower(left[i]), lower(right[i])
		if a != b {
			return a < b
		}
	}
	return len(left) < len(right)
}

func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// lookupUser resolves a uid to a name, falling back to the number.
//
// A site's account may have been removed while its files remain, and a numeric
// owner is more useful than an empty column.
func lookupUser(uid int) string {
	if found, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		return found.Username
	}
	return strconv.Itoa(uid)
}

func lookupGroup(gid int) string {
	if found, err := user.LookupGroupId(strconv.Itoa(gid)); err == nil {
		return found.Name
	}
	return strconv.Itoa(gid)
}
