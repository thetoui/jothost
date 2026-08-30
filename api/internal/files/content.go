package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/jothost/panel/api/internal/agentclient"
)

// MaxEditableBytes bounds a file the editor will open or save.
//
// It mirrors EditableFileLimit in the Agent's files package. Both ends need it:
// the Agent so it can mark an entry editable in a listing, the API so a save is
// refused before the whole body has been read. Beyond this a file is still
// downloadable — it is simply not something a browser-based editor should try
// to hold in a textarea and diff on every keystroke.
const MaxEditableBytes int64 = 2 * 1024 * 1024

// Content errors a caller can act on.
var (
	ErrFileTooLarge = errors.New("this file is too large to edit")
	ErrFileBinary   = errors.New("this file is not text")
	ErrStaleWrite   = errors.New("the file changed since it was opened")
)

// Content is a file as the editor sees it.
type Content struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Size     int64  `json:"size"`
	Mode     string `json:"mode"`
	Modified string `json:"modified"`
	Owner    string `json:"owner"`
	// Checksum identifies exactly this content. A save carries it back so a
	// second editor cannot silently overwrite the first one's work.
	Checksum string `json:"checksum"`
	// Language is a hint for the editor's syntax highlighting, derived from the
	// file name rather than sniffed from the content.
	Language string `json:"language"`
	// EndOfLine is what the file already uses, so saving does not rewrite every
	// line of a CRLF file that nobody meant to convert.
	EndOfLine string `json:"end_of_line"`
}

// End-of-line kinds.
const (
	EOLUnix    = "lf"
	EOLWindows = "crlf"
)

// ReadContent loads a whole file for editing.
//
// Unlike Download this does accumulate, because an editor needs the whole file
// at once. That is exactly why it is capped: the cap is what stops "open this
// file" from being a way to allocate a gigabyte in the API.
func (s *Service) ReadContent(ctx context.Context, requestID, path string) (Content, error) {
	entry, err := s.agent.FileStat(ctx, requestID, path)
	if err != nil {
		return Content{}, err
	}
	if entry.Type != "file" {
		return Content{}, ErrFileBinary
	}
	if entry.Size > MaxEditableBytes {
		return Content{}, fmt.Errorf("%w: %d bytes, the limit is %d",
			ErrFileTooLarge, entry.Size, MaxEditableBytes)
	}

	var buffer bytes.Buffer
	buffer.Grow(int(entry.Size))
	if _, err := s.Download(ctx, requestID, path, &buffer); err != nil {
		return Content{}, err
	}
	raw := buffer.Bytes()

	// A NUL byte means this is not text. Opening it in an editor and saving it
	// back would corrupt the file, and the editor would show mojibake in the
	// meantime.
	if bytes.IndexByte(raw, 0) >= 0 || !utf8.Valid(raw) {
		return Content{}, ErrFileBinary
	}

	eol := EOLUnix
	if bytes.Contains(raw, []byte("\r\n")) {
		eol = EOLWindows
	}

	return Content{
		Path:      entry.Path,
		Content:   string(raw),
		Size:      entry.Size,
		Mode:      entry.Mode,
		Modified:  entry.Modified.UTC().Format("2006-01-02T15:04:05Z07:00"),
		Owner:     entry.Owner + ":" + entry.Group,
		Checksum:  checksum(raw),
		Language:  LanguageFor(entry.Name),
		EndOfLine: eol,
	}, nil
}

// WriteRequest describes a save.
type WriteContentRequest struct {
	Path    string
	Content string
	// Checksum is what the editor loaded. An empty value means the caller is
	// creating the file or has chosen to overwrite regardless.
	Checksum string
	// Force saves even though the file changed underneath. The panel asks
	// before setting it; it is never the default.
	Force bool
}

// WriteContent saves a file, refusing to silently overwrite someone else's
// change.
//
// Two people editing one file is not exotic on a hosting panel — it is two tabs
// open on the same wp-config.php. Without a check the second save destroys the
// first with no sign that anything was lost, which is the same class of failure
// as overwriting a valid backup (CLAUDE.md section 18).
func (s *Service) WriteContent(ctx context.Context, requestID string, req WriteContentRequest, actor Actor) (Content, error) {
	body := []byte(req.Content)
	if int64(len(body)) > MaxEditableBytes {
		return Content{}, fmt.Errorf("%w: %d bytes, the limit is %d",
			ErrFileTooLarge, len(body), MaxEditableBytes)
	}
	if bytes.IndexByte(body, 0) >= 0 {
		return Content{}, ErrFileBinary
	}

	if req.Checksum != "" && !req.Force {
		if err := s.verifyUnchanged(ctx, requestID, req.Path, req.Checksum); err != nil {
			return Content{}, err
		}
	}

	if err := s.writeAll(ctx, requestID, req.Path, body); err != nil {
		return Content{}, err
	}

	s.record(ctx, actor, ActionWrite, req.Path, map[string]any{
		"bytes": len(body),
		"force": req.Force,
	})

	entry, err := s.agent.FileStat(ctx, requestID, req.Path)
	if err != nil {
		return Content{}, err
	}

	return Content{
		Path:     entry.Path,
		Size:     entry.Size,
		Mode:     entry.Mode,
		Modified: entry.Modified.UTC().Format("2006-01-02T15:04:05Z07:00"),
		Owner:    entry.Owner + ":" + entry.Group,
		Checksum: checksum(body),
		Language: LanguageFor(entry.Name),
	}, nil
}

// verifyUnchanged reports whether the file still holds what the editor loaded.
//
// This is a check, not a lock: the file could change between here and the write
// below. It closes the window from "however long someone left a tab open" to
// "the length of one request", which is the difference between losing an
// afternoon's work and losing a race that essentially never happens.
func (s *Service) verifyUnchanged(ctx context.Context, requestID, path, expected string) error {
	entry, err := s.agent.FileStat(ctx, requestID, path)
	if err != nil {
		// A file that is gone cannot have been changed by someone else in a way
		// this check should block. Creating it is the caller's intent.
		var failure *agentclient.ErrOperationFailed
		if errors.As(err, &failure) && failure.Code == "NOT_FOUND" {
			return nil
		}
		return err
	}
	if entry.Size > MaxEditableBytes {
		return ErrStaleWrite
	}

	var buffer bytes.Buffer
	if _, err := s.Download(ctx, requestID, path, &buffer); err != nil {
		return err
	}
	if checksum(buffer.Bytes()) != expected {
		return ErrStaleWrite
	}
	return nil
}

// writeAll sends a whole file to the Agent in chunks.
func (s *Service) writeAll(ctx context.Context, requestID, path string, body []byte) error {
	// An empty file still needs one write, or saving a file empty would leave
	// its previous contents in place.
	if len(body) == 0 {
		_, err := s.agent.FileWrite(ctx, requestID, agentclient.FileWriteRequest{
			Path: path, Offset: 0, Data: nil, Truncate: true, Final: true,
		})
		return err
	}

	var offset int64
	for offset < int64(len(body)) {
		end := offset + agentclient.MaxChunkBytes
		if end > int64(len(body)) {
			end = int64(len(body))
		}

		_, err := s.agent.FileWrite(ctx, requestID, agentclient.FileWriteRequest{
			Path:     path,
			Offset:   offset,
			Data:     body[offset:end],
			Truncate: offset == 0,
			Final:    end == int64(len(body)),
		})
		if err != nil {
			return err
		}
		offset = end
	}
	return nil
}

// checksum identifies a file's exact content.
func checksum(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// LanguageFor maps a file name to an editor language.
//
// Derived from the name, never sniffed from the content: a syntax mode guessed
// from a file's first line is wrong often enough to be worse than none, and the
// editor has to pick something before the file has finished loading anyway.
func LanguageFor(name string) string {
	for suffix, language := range languageByExactName {
		if name == suffix {
			return language
		}
	}

	dot := -1
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			dot = i
			break
		}
	}
	if dot < 0 || dot == len(name)-1 {
		return "plaintext"
	}

	if language, ok := languageByExtension[lowerASCII(name[dot+1:])]; ok {
		return language
	}
	return "plaintext"
}

// languageByExactName covers the dotfiles and bare names a hosting panel meets,
// which have no extension to key off.
var languageByExactName = map[string]string{
	".htaccess":          "apache",
	".env":               "ini",
	"Dockerfile":         "dockerfile",
	"Makefile":           "makefile",
	"nginx.conf":         "nginx",
	"composer.json":      "json",
	"package.json":       "json",
	"tsconfig.json":      "json",
	".gitignore":         "plaintext",
	"robots.txt":         "plaintext",
	".editorconfig":      "ini",
	"php.ini":            "ini",
	"my.cnf":             "ini",
	"crontab":            "plaintext",
	"docker-compose.yml": "yaml",
}

var languageByExtension = map[string]string{
	"php":  "php",
	"js":   "javascript",
	"mjs":  "javascript",
	"cjs":  "javascript",
	"jsx":  "javascript",
	"ts":   "typescript",
	"tsx":  "typescript",
	"html": "html",
	"htm":  "html",
	"twig": "html",
	"vue":  "html",
	"css":  "css",
	"scss": "scss",
	"less": "less",
	"json": "json",
	"yml":  "yaml",
	"yaml": "yaml",
	"xml":  "xml",
	"svg":  "xml",
	"md":   "markdown",
	"sql":  "sql",
	"sh":   "shell",
	"bash": "shell",
	"zsh":  "shell",
	"ini":  "ini",
	"conf": "ini",
	"cnf":  "ini",
	"toml": "ini",
	"py":   "python",
	"rb":   "ruby",
	"go":   "go",
	"rs":   "rust",
	"java": "java",
	"c":    "c",
	"h":    "c",
	"cpp":  "cpp",
	"txt":  "plaintext",
	"log":  "plaintext",
	"lock": "plaintext",
}

func lowerASCII(value string) string {
	out := []byte(value)
	for i, c := range out {
		if c >= 'A' && c <= 'Z' {
			out[i] = c + ('a' - 'A')
		}
	}
	return string(out)
}
