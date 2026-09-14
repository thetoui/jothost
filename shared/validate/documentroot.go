package validate

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// Errors returned when a document root is refused.
var (
	// ErrInvalidDocumentRoot covers a path that is not a usable subdirectory.
	ErrInvalidDocumentRoot = errors.New("invalid document root")
	// ErrDocumentRootReserved covers a directory the panel owns.
	ErrDocumentRootReserved = errors.New("that directory belongs to the panel")
)

// DocumentRootMaxDepth bounds how deep a document root may be nested.
//
// Six is past anything real — "public/dist/browser" is three — and a bound
// exists so that a pasted path cannot produce a directory tree nobody meant to
// create.
const DocumentRootMaxDepth = 6

// DocumentRootMaxLength bounds the whole path.
const DocumentRootMaxLength = 200

// reservedRoots are directories inside a site that the panel writes and the
// site's own content must not be served from.
//
// logs is the important one: serving it would publish every request anybody
// has ever made to the site, including the query strings.
var reservedRoots = []string{"logs"}

// DocumentRoot checks a document root offered for a website, and returns it in
// the form the panel stores.
//
// The path is *relative to the site's own directory*, and that is the point.
// An operator setting "public/dist" cannot name another site's files, cannot
// name /etc, and cannot escape with "../" — because they are not naming a
// directory at all, only a subpath of the one that is already theirs. Taking an
// absolute path here and checking it afterwards would mean trusting a check to
// catch every way a path can be written; taking a relative one means there is
// nothing to catch.
//
// Returns the cleaned path. An empty input is the site's own root, which is a
// legitimate choice for a site whose index.php sits at the top.
func DocumentRoot(relative string) (string, error) {
	trimmed := strings.TrimSpace(relative)

	// An absolute path is refused rather than reinterpreted. Stripping the
	// leading slash would turn "/etc/passwd" into the site's own etc/passwd —
	// harmless, since it stays inside the site, and wrong, because the
	// operator asked for something else and would be told nothing.
	if strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf(
			"%w: give a path inside the site, such as public/dist, not one starting with /",
			ErrInvalidDocumentRoot)
	}
	// A trailing slash is only ever a typing habit.
	trimmed = strings.TrimRight(trimmed, "/")

	if trimmed == "" {
		// The site's own directory. Not an error: a site with no build step
		// often has its index at the top.
		return "", nil
	}

	if len(trimmed) > DocumentRootMaxLength {
		return "", fmt.Errorf("%w: longer than %d characters",
			ErrInvalidDocumentRoot, DocumentRootMaxLength)
	}

	// Refused before cleaning, not after. path.Clean resolves "a/../b" to "b",
	// which would silently accept a path the operator did not write and make
	// the stored value differ from what they typed.
	if strings.Contains(trimmed, `\`) {
		return "", fmt.Errorf("%w: use / to separate directories", ErrInvalidDocumentRoot)
	}
	if strings.ContainsRune(trimmed, 0) {
		return "", fmt.Errorf("%w: it contains a null byte", ErrInvalidDocumentRoot)
	}

	segments := strings.Split(trimmed, "/")
	if len(segments) > DocumentRootMaxDepth {
		return "", fmt.Errorf("%w: no more than %d directories deep",
			ErrInvalidDocumentRoot, DocumentRootMaxDepth)
	}

	for _, segment := range segments {
		switch segment {
		case "":
			return "", fmt.Errorf("%w: it has an empty directory name", ErrInvalidDocumentRoot)
		case ".", "..":
			return "", fmt.Errorf("%w: %q is not a directory name", ErrInvalidDocumentRoot, segment)
		}
		if strings.HasPrefix(segment, "-") {
			// A leading dash is how a directory name becomes a command-line
			// option somewhere downstream.
			return "", fmt.Errorf("%w: a directory name may not begin with a dash",
				ErrInvalidDocumentRoot)
		}
		if err := documentRootSegment(segment); err != nil {
			return "", err
		}
	}

	cleaned := path.Clean(trimmed)
	// Clean cannot have changed anything, given the checks above. Asserted
	// rather than assumed: if it did, the stored path is not the typed one.
	if cleaned != trimmed {
		return "", fmt.Errorf("%w: write the path plainly, without . or ..",
			ErrInvalidDocumentRoot)
	}

	for _, reserved := range reservedRoots {
		if segments[0] == reserved {
			return "", fmt.Errorf("%w: %s holds the site's own logs",
				ErrDocumentRootReserved, reserved)
		}
	}

	return cleaned, nil
}

// documentRootSegment checks one directory name.
//
// Deliberately narrow: letters, digits, dot, dash and underscore. A document
// root is a directory somebody deploys into, not a place for spaces and
// quotes, and every character allowed here is one more that has to survive a
// shell-free but still string-built nginx configuration file.
func documentRootSegment(segment string) error {
	if len(segment) > 64 {
		return fmt.Errorf("%w: %q is too long for a directory name",
			ErrInvalidDocumentRoot, segment)
	}
	for _, r := range segment {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_':
		default:
			return fmt.Errorf("%w: %q may contain only letters, digits, dot, dash and underscore",
				ErrInvalidDocumentRoot, segment)
		}
	}
	return nil
}

// DocumentRootPath composes the absolute path a relative document root means.
//
// One function, so the panel and the Agent cannot disagree about where a
// site's "public/dist" actually is.
func DocumentRootPath(siteDir, relative string) string {
	if relative == "" {
		return path.Clean(siteDir)
	}
	return path.Join(siteDir, relative)
}
