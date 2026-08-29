package files

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrEmptyQuery is returned for a search with nothing to look for.
//
// An empty query matches every file on the host, which is a denial of service
// dressed up as a feature.
var ErrEmptyQuery = errors.New("a search needs something to search for")

// Search bounds. A document root can hold a million files, and an unbounded
// walk pegs a core and holds a job open until it times out.
const (
	DefaultSearchLimit = 200
	MaxSearchLimit     = 1000
	MaxSearchDepth     = 12
	// MaxContentSearchBytes bounds how much of one file is scanned for a
	// content match. Beyond this a file is almost certainly not source.
	MaxContentSearchBytes int64 = 2 * 1024 * 1024
)

// Match is one search result.
type Match struct {
	Entry Entry `json:"entry"`
	// Line is the first matching line for a content search, empty for a name
	// search.
	Line string `json:"line,omitempty"`
	// LineNumber is 1-based, zero for a name search.
	LineNumber int `json:"line_number,omitempty"`
}

// SearchRequest describes a search.
type SearchRequest struct {
	Path  string
	Query string
	// Content searches inside files rather than matching their names.
	Content bool
	Limit   int
}

// SearchResult is a bounded set of matches.
type SearchResult struct {
	Path    string  `json:"path"`
	Query   string  `json:"query"`
	Matches []Match `json:"matches"`
	// Truncated says the walk stopped at a limit rather than at the end of the
	// tree, so a caller does not read "12 results" as "12 files exist".
	Truncated bool `json:"truncated"`
	// Scanned is how many entries were examined, which explains a truncated
	// result to whoever is looking at it.
	Scanned int `json:"scanned"`
}

// Search walks a directory for names or content matching a query.
//
// The context is honoured on every entry: a search is the one file operation
// whose cost is set by the tree rather than by the request, so it has to be
// cancellable by the job timeout rather than running to completion regardless.
func (m *Manager) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	if !m.Available() {
		return SearchResult{}, ErrUnavailable
	}

	query := strings.TrimSpace(req.Query)
	if query == "" {
		return SearchResult{}, ErrEmptyQuery
	}

	limit := req.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}

	root, err := m.validator.ResolveDir(req.Path)
	if err != nil {
		return SearchResult{}, translate(err)
	}

	needle := strings.ToLower(query)
	result := SearchResult{Path: root, Query: query, Matches: make([]Match, 0, 16)}

	walkErr := filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			// One unreadable directory must not end the whole search.
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if len(result.Matches) >= limit {
			result.Truncated = true
			return fs.SkipAll
		}
		if depthOf(root, current) > MaxSearchDepth {
			return fs.SkipDir
		}

		// Symlinks are never descended. A link back to an ancestor turns the
		// walk into an infinite loop.
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}

		result.Scanned++

		if req.Content {
			if entry.IsDir() {
				return nil
			}
			match, line, number, err := scanFile(current, needle)
			if err != nil || !match {
				return nil //nolint:nilerr // an unreadable file is not a match
			}
			described, err := m.describe(current)
			if err != nil {
				return nil //nolint:nilerr // vanished mid-walk
			}
			result.Matches = append(result.Matches,
				Match{Entry: described, Line: line, LineNumber: number})
			return nil
		}

		if current == root {
			return nil
		}
		if !strings.Contains(strings.ToLower(entry.Name()), needle) {
			return nil
		}
		described, err := m.describe(current)
		if err != nil {
			return nil //nolint:nilerr // vanished mid-walk
		}
		result.Matches = append(result.Matches, Match{Entry: described})
		return nil
	})

	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		if errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
			// A cancelled search returns what it found, marked incomplete.
			// Discarding the results because time ran out helps nobody.
			result.Truncated = true
			return result, nil
		}
		return SearchResult{}, walkErr
	}
	return result, nil
}

// scanFile looks for a needle inside a file, reading a bounded prefix.
func scanFile(path, needle string) (bool, string, int, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false, "", 0, err
	}

	handle, err := os.Open(path) //nolint:gosec // inside a resolved tree
	if err != nil {
		return false, "", 0, err
	}
	defer func() { _ = handle.Close() }()

	content, err := io.ReadAll(io.LimitReader(handle, MaxContentSearchBytes))
	if err != nil {
		return false, "", 0, err
	}
	// A binary file has no lines worth showing, and returning a slice of one to
	// a browser produces mojibake at best.
	if bytes.IndexByte(content, 0) >= 0 {
		return false, "", 0, nil
	}

	lowered := strings.ToLower(string(content))
	index := strings.Index(lowered, needle)
	if index < 0 {
		return false, "", 0, nil
	}

	number := strings.Count(lowered[:index], "\n") + 1
	start := strings.LastIndexByte(lowered[:index], '\n') + 1
	end := strings.IndexByte(lowered[index:], '\n')
	if end < 0 {
		end = len(lowered)
	} else {
		end += index
	}

	line := strings.TrimRight(string(content)[start:end], "\r")
	const maxLine = 400
	if len(line) > maxLine {
		line = line[:maxLine]
	}
	return true, line, number, nil
}

// depthOf counts directory levels between a root and a path.
func depthOf(root, path string) int {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return 0
	}
	if relative == "." {
		return 0
	}
	return strings.Count(relative, string(filepath.Separator)) + 1
}
