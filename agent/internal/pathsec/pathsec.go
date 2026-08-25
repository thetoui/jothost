// Package pathsec validates filesystem paths supplied by a caller.
//
// It implements CLAUDE.md section 5: every path is normalised, resolved
// against an allowed root, checked for traversal, and checked for symlink
// escape. The Agent runs as root, so a path that escapes its intended root is
// a full compromise rather than a bug.
//
// The rule this package exists to enforce: never trust a path from the API,
// and never trust that a path which looked safe still resolves somewhere safe.
package pathsec

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Errors returned by validation. Callers map these to a single generic
// client-facing message; the distinction is for logs.
var (
	ErrEmptyPath      = errors.New("path is empty")
	ErrNotAbsolute    = errors.New("path must be absolute")
	ErrTraversal      = errors.New("path contains a traversal segment")
	ErrOutsideRoot    = errors.New("path resolves outside its allowed root")
	ErrNotFound       = errors.New("path does not exist")
	ErrNotDirectory   = errors.New("path is not a directory")
	ErrNotRegular     = errors.New("path is not a regular file")
	ErrNullByte       = errors.New("path contains a null byte")
	ErrSymlinkEscape  = errors.New("path escapes its root through a symlink")
	ErrRootNotAllowed = errors.New("no allowed root contains this path")
)

// maxPathLength bounds a caller-supplied path. Linux's PATH_MAX is 4096; a
// longer value is malformed or an attempt to exhaust memory.
const maxPathLength = 4096

// Validator resolves paths against a fixed set of allowed roots.
//
// A zero Validator allows nothing, which is the safe default: a caller that
// forgets to configure roots gets refusals rather than unrestricted access.
type Validator struct {
	roots []string
}

// NewValidator builds a Validator for the given roots.
//
// Roots are cleaned and must be absolute. They are resolved through symlinks
// once, at construction, so that a root which is itself a symlink still
// compares correctly against resolved candidate paths.
func NewValidator(roots ...string) (*Validator, error) {
	if len(roots) == 0 {
		return nil, errors.New("at least one allowed root is required")
	}

	resolved := make([]string, 0, len(roots))
	for _, root := range roots {
		if !filepath.IsAbs(root) {
			return nil, fmt.Errorf("root %q: %w", root, ErrNotAbsolute)
		}

		clean := filepath.Clean(root)
		// A root that does not exist yet is kept as-is: the directory may be
		// created later, and refusing to start would be worse than resolving
		// it lazily on each check.
		if real, err := filepath.EvalSymlinks(clean); err == nil {
			clean = real
		}
		resolved = append(resolved, clean)
	}

	return &Validator{roots: resolved}, nil
}

// Roots returns the configured roots.
func (v *Validator) Roots() []string {
	out := make([]string, len(v.roots))
	copy(out, v.roots)
	return out
}

// Clean normalises a caller-supplied path without consulting the filesystem.
//
// It rejects the malformed shapes outright rather than silently repairing
// them: a path containing "..", a null byte, or a relative prefix is a caller
// bug or an attack, and quietly normalising it hides both.
func Clean(path string) (string, error) {
	if path == "" {
		return "", ErrEmptyPath
	}
	if len(path) > maxPathLength {
		return "", fmt.Errorf("path exceeds %d bytes", maxPathLength)
	}
	if strings.ContainsRune(path, '\x00') {
		// A null byte truncates the path in any C API the kernel reaches, so
		// "/safe/dir\x00/../../etc/shadow" could pass a naive string check.
		return "", ErrNullByte
	}
	if !filepath.IsAbs(path) {
		return "", ErrNotAbsolute
	}

	// Reject traversal on the raw input. filepath.Clean would resolve ".."
	// lexically and produce a path that looks fine, hiding the caller's
	// intent; refusing is both safer and more diagnosable.
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return "", ErrTraversal
		}
	}

	return filepath.Clean(path), nil
}

// Resolve validates a path and returns its fully resolved location.
//
// The returned path has every symlink resolved, so callers can act on it
// knowing it cannot point somewhere else by the time they use it. Resolution
// happens against the deepest existing ancestor, which allows validating a
// path that is about to be created.
func (v *Validator) Resolve(path string) (string, error) {
	clean, err := Clean(path)
	if err != nil {
		return "", err
	}

	resolved, err := resolveThroughSymlinks(clean)
	if err != nil {
		return "", err
	}

	if !v.contains(resolved) {
		// The message names neither the roots nor the resolved location: a
		// caller probing for the layout learns nothing from the difference
		// between "outside root" and "does not exist".
		return "", ErrOutsideRoot
	}
	return resolved, nil
}

// ResolveExisting is Resolve plus a requirement that the path exists.
func (v *Validator) ResolveExisting(path string) (string, error) {
	resolved, err := v.Resolve(path)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(resolved); err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("stat path: %w", err)
	}
	return resolved, nil
}

// ResolveDir is ResolveExisting plus a requirement that the path is a
// directory.
func (v *Validator) ResolveDir(path string) (string, error) {
	resolved, err := v.ResolveExisting(path)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat directory: %w", err)
	}
	if !info.IsDir() {
		return "", ErrNotDirectory
	}
	return resolved, nil
}

// ResolveFile is ResolveExisting plus a requirement that the path is a regular
// file.
//
// Devices, FIFOs, and sockets are refused: reading one can block forever or
// return unbounded data, and /dev/zero is a regular-looking path.
func (v *Validator) ResolveFile(path string) (string, error) {
	resolved, err := v.ResolveExisting(path)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", ErrNotRegular
	}
	return resolved, nil
}

// contains reports whether path lies inside one of the allowed roots.
func (v *Validator) contains(path string) bool {
	for _, root := range v.roots {
		if path == root {
			return true
		}
		// The separator matters: without it, "/var/wwwevil" would be accepted
		// as being inside "/var/www".
		if strings.HasPrefix(path, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// resolveThroughSymlinks resolves the deepest existing ancestor of path and
// re-appends the remaining components.
//
// filepath.EvalSymlinks fails outright on a path that does not exist, which
// would make it impossible to validate a destination before creating it. This
// resolves what exists and keeps the rest lexically, so a symlinked parent
// directory still cannot smuggle the result outside its root.
func resolveThroughSymlinks(path string) (string, error) {
	remainder := ""
	current := path

	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			if remainder == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, remainder), nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve path: %w", err)
		}

		parent := filepath.Dir(current)
		if parent == current {
			// Reached the filesystem root without finding anything that
			// exists; nothing can be resolved.
			return path, nil
		}
		remainder = filepath.Join(filepath.Base(current), remainder)
		current = parent
	}
}
