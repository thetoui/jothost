package files

import (
	"errors"
	"path"
	"strings"
)

// Path validation errors.
var (
	ErrPathNotAbsolute = errors.New("path must be absolute")
	ErrPathTraversal   = errors.New("path must not contain a traversal segment")
	ErrPathNullByte    = errors.New("path must not contain a null byte")
	ErrPathTooLong     = errors.New("path is too long")
	ErrNameInvalid     = errors.New("a name must be a single path segment")
)

// maxPathLength matches Linux's PATH_MAX. A longer value is malformed or an
// attempt to exhaust memory before anything gets a chance to reject it.
const maxPathLength = 4096

// maxNameLength matches Linux's NAME_MAX.
const maxNameLength = 255

// ValidatePath checks the shape of a caller-supplied path.
//
// The Agent is the authority on which roots exist and re-validates everything
// through pathsec, including symlink resolution, which only the host can do.
// This is the cheap edge check: it rejects the obviously hostile shapes before
// they cost a socket round trip, and gives a clearer message than a generic
// refusal from the far side.
//
// It deliberately does not know where the site root is. Duplicating that here
// would let the two ends disagree, and the end that owns the disk is the one
// that must win.
func ValidatePath(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", ErrPathRequired
	}
	if len(value) > maxPathLength {
		return "", ErrPathTooLong
	}
	if strings.ContainsRune(value, '\x00') {
		// A null byte truncates a path in any C API the kernel reaches, so a
		// naive check on the whole string can pass while the kernel sees only
		// the prefix.
		return "", ErrPathNullByte
	}
	if !strings.HasPrefix(value, "/") {
		return "", ErrPathNotAbsolute
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return "", ErrPathTraversal
		}
	}
	return path.Clean(value), nil
}

// SafeName validates a single path segment, such as an uploaded filename.
//
// A browser controls this value completely. Anything but a plain name is
// refused rather than sanitised: a caller sending "../../etc/cron.d/x" is not
// making a typo, and silently rewriting it hides an attempt worth seeing.
func SafeName(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", ErrNameInvalid
	}
	if len(trimmed) > maxNameLength {
		return "", ErrPathTooLong
	}
	if strings.ContainsRune(trimmed, '\x00') {
		return "", ErrPathNullByte
	}
	// Some browsers send a full client-side path. Only the base name is ever
	// meaningful, but taking it silently would also accept "/etc/passwd" as
	// "passwd", so the whole value has to be a single segment already.
	if strings.ContainsAny(trimmed, "/\\") {
		return "", ErrNameInvalid
	}
	if trimmed == "." || trimmed == ".." {
		return "", ErrNameInvalid
	}
	return trimmed, nil
}

// JoinPath appends a validated name to a directory.
func JoinPath(directory, name string) string {
	return path.Join(directory, name)
}
