package logs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jothost/panel/agent/internal/pathsec"
)

// Bounds. A log file is unbounded and a response is not, so every read here is
// capped in three directions at once: how far back into the file it may look,
// how many lines it may return, and how long any one of them may be.
const (
	// MaxScanBytes is how much of the file's tail is examined for one request.
	// Chosen against the 1 MiB socket frame: a scan that filled the frame with
	// matches would fail to send, so the scan window is a quarter of it.
	MaxScanBytes = 256 << 10
	// MaxLines is the most lines one response may carry.
	MaxLines = 2000
	// DefaultLines is what a caller gets without asking.
	DefaultLines = 200
	// MaxLineBytes truncates a single enormous line. A stack trace written
	// without newlines is one line, and one line should not be able to fill a
	// response on its own.
	MaxLineBytes = 4096
	// MaxSearchLength bounds the needle. It exists so a caller cannot make the
	// Agent do unbounded work per line.
	MaxSearchLength = 200
)

// AllowedRoots are the directories a catalogued log may resolve inside.
//
// The catalogue already decides which files exist, so this is the second lock
// rather than the first: it is what stops a *symlinked* log path from reaching
// outside the places logs live. /var/www is included because a Node
// application's output and a site's PHP error log are written there.
var AllowedRoots = []string{"/var/log", "/var/www"}

// Provider reads log files.
type Provider struct {
	validator *pathsec.Validator
	log       *slog.Logger
}

// NewProvider builds a Provider confined to roots.
//
// A Provider whose validator could not be built reads nothing, rather than
// reading everything: the failure mode of a misconfigured path check must be
// refusal.
func NewProvider(log *slog.Logger, roots ...string) *Provider {
	if log == nil {
		log = slog.Default()
	}
	if len(roots) == 0 {
		roots = AllowedRoots
	}

	validator, err := pathsec.NewValidator(roots...)
	if err != nil {
		log.Error("log roots could not be validated; log reading is disabled",
			"error", err.Error())
		validator = nil
	}
	return &Provider{validator: validator, log: log}
}

// Available reports whether this Provider can read anything at all.
func (p *Provider) Available() bool { return p != nil && p.validator != nil }

// Detected is one source as this host actually has it.
type Detected struct {
	Source
	// Present is whether one of the candidate paths exists here.
	Present bool `json:"present"`
	// Size is the file's current size in bytes, and Modified when it last
	// changed. Both are what an operator uses to decide whether a log is worth
	// opening: an error log that has not changed in a week is not where the
	// problem is.
	Size     int64      `json:"size"`
	Modified *time.Time `json:"modified"`
}

// Detect reports every catalogued log, and which of them this host has.
//
// Sources that are absent are still listed, unlike the service catalogue's
// absent units. The difference is what the absence means: a service that is not
// installed is not this host's business, while a log file that does not exist
// yet is normal — nginx creates its error log on the first error — and a picker
// that hid it would make "there are no errors" indistinguishable from "this
// panel does not offer that log".
func (p *Provider) Detect(extra []Source) []Detected {
	sources := append(Catalogue(), extra...)
	sortSources(sources)

	detected := make([]Detected, 0, len(sources))
	for _, source := range sources {
		entry := Detected{Source: source}
		entry.Source.Path = ""

		if path, info, err := p.resolve(source); err == nil {
			modified := info.ModTime().UTC()
			entry.Present = true
			entry.Size = info.Size()
			entry.Modified = &modified
			entry.Source.Path = path
		}
		detected = append(detected, entry)
	}
	return detected
}

// resolve finds the candidate path this host has, checked and confined.
func (p *Provider) resolve(source Source) (string, os.FileInfo, error) {
	if !p.Available() {
		return "", nil, ErrNotPresent
	}

	for _, candidate := range source.Paths {
		// ResolveFile follows symlinks and requires the result to be a regular
		// file inside an allowed root. Both halves matter: a symlink out of
		// /var/log is how this becomes a general file reader, and a FIFO where
		// a log is expected is how a read blocks forever.
		resolved, err := p.validator.ResolveFile(candidate)
		if err != nil {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil {
			continue
		}
		return resolved, info, nil
	}
	return "", nil, fmt.Errorf("%w: %s", ErrNotPresent, source.Key)
}

// Options select what a read returns.
type Options struct {
	// Lines is how many matching lines to return, most recent last.
	Lines int
	// Search keeps only lines containing this text, compared without case.
	Search string
	// Level keeps only lines of this severity. A line whose level could not be
	// read is never removed by this: see format.go.
	Level string
	// After is a byte offset from a previous read. Only what was appended
	// since is returned, which is what makes following a log cheap.
	After int64
}

// Line is one log line as the panel shows it.
type Line struct {
	// Offset is where this line starts in the file, which is what makes a line
	// identifiable across polls.
	Offset int64  `json:"offset"`
	Text   string `json:"text"`
	// Level is the severity read from the line, or empty where the format has
	// none.
	Level string `json:"level"`
	// Truncated marks a line that was too long to return whole.
	Truncated bool `json:"truncated,omitempty"`
}

// Result is one read of a log.
type Result struct {
	Key   string `json:"key"`
	Path  string `json:"path"`
	Lines []Line `json:"lines"`
	// Offset is where the next read should continue from. It is the end of the
	// last *complete* line, not the end of the file: a line still being written
	// would otherwise be returned in halves.
	Offset int64 `json:"offset"`
	// Size is the file's size at the moment of the read.
	Size int64 `json:"size"`
	// Rotated reports that the file shrank since the caller's offset, which
	// means it was rotated or truncated underneath them. The panel says so
	// rather than showing a gap and letting an operator conclude the log lost
	// entries.
	Rotated bool `json:"rotated"`
	// Scanned is how many bytes were examined, and Partial says the window did
	// not reach the caller's offset — there was more new output than one read
	// may carry, so the oldest of it is not here.
	Scanned int64 `json:"scanned"`
	Partial bool  `json:"partial"`
	// Filtered is how many lines the search or level filter removed, so the
	// page can say "showing 12 of 400" rather than implying the log is empty.
	Filtered int    `json:"filtered"`
	Format   string `json:"format"`
}

// ErrInvalidOption means a caller asked for something outside the bounds.
var ErrInvalidOption = errors.New("invalid log option")

// Tail returns the end of a log, or what is new since a previous read.
func (p *Provider) Tail(source Source, opts Options) (Result, error) {
	if len(opts.Search) > MaxSearchLength {
		return Result{}, fmt.Errorf("%w: the search text is too long", ErrInvalidOption)
	}
	if !ValidLevel(opts.Level) {
		return Result{}, fmt.Errorf("%w: %q is not a level", ErrInvalidOption, opts.Level)
	}
	if opts.Lines <= 0 {
		opts.Lines = DefaultLines
	}
	if opts.Lines > MaxLines {
		opts.Lines = MaxLines
	}
	if opts.After < 0 {
		return Result{}, fmt.Errorf("%w: a negative offset", ErrInvalidOption)
	}

	path, info, err := p.resolve(source)
	if err != nil {
		return Result{}, err
	}

	result := Result{Key: source.Key, Path: path, Size: info.Size(),
		Format: source.Format, Lines: []Line{}}

	file, err := os.Open(path) //nolint:gosec // path came from the catalogue and through pathsec
	if err != nil {
		return Result{}, fmt.Errorf("open log: %w", err)
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			p.log.Warn("closing a log file failed", "path", path, "error", cerr.Error())
		}
	}()

	start, rotated, partial := p.window(opts, result.Size)
	result.Rotated = rotated
	result.Partial = partial

	if start >= result.Size {
		// Nothing new. The offset is returned unchanged so a follower does not
		// lose its place.
		result.Offset = result.Size
		return result, nil
	}

	buf := make([]byte, result.Size-start)
	read, err := file.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return Result{}, fmt.Errorf("read log: %w", err)
	}
	buf = buf[:read]
	result.Scanned = int64(read)

	lines, consumed := splitLines(buf, start, start > 0 && opts.After == 0)
	result.Offset = start + consumed

	kept, filtered := filterLines(lines, source.Format, opts)
	result.Filtered = filtered
	if len(kept) > opts.Lines {
		kept = kept[len(kept)-opts.Lines:]
	}
	result.Lines = kept
	return result, nil
}

// window decides which part of the file to read.
//
// Three cases, and they are different enough to be worth naming: a first read
// looks at the tail; a follow-up reads what was appended; and a follow-up whose
// offset is past the end of the file means the file was rotated or truncated
// under the caller, which is reported rather than silently reinterpreted.
func (p *Provider) window(opts Options, size int64) (start int64, rotated, partial bool) {
	if opts.After > 0 {
		if opts.After > size {
			// The file is smaller than where the caller was reading. Their
			// place no longer exists.
			return max(int64(0), size-MaxScanBytes), true, false
		}
		start = opts.After
		if size-start > MaxScanBytes {
			// More arrived than one response may carry. The newest is what an
			// operator following a log needs, and the gap is declared.
			return size - MaxScanBytes, false, true
		}
		return start, false, false
	}

	if size > MaxScanBytes {
		return size - MaxScanBytes, false, true
	}
	return 0, false, false
}

// splitLines turns a byte window into lines.
//
// dropFirst discards the first line when the window started mid-file, because
// that "line" is the tail of one whose beginning was not read. consumed is how
// many bytes of the window ended in a newline, which is what the next read
// continues from — a line still being written is left for next time rather than
// shown in halves.
func splitLines(buf []byte, base int64, dropFirst bool) (lines []Line, consumed int64) {
	offset := base
	lines = make([]Line, 0, 64)

	for len(buf) > 0 {
		at := bytes.IndexByte(buf, '\n')
		if at < 0 {
			// A trailing fragment with no newline: not a complete line yet.
			break
		}

		text := string(buf[:at])
		lines = append(lines, Line{Offset: offset, Text: text})

		advance := int64(at + 1)
		offset += advance
		consumed += advance
		buf = buf[at+1:]
	}

	if dropFirst && len(lines) > 0 {
		lines = lines[1:]
	}
	return lines, consumed
}

// filterLines applies the search and level filters and tidies each line.
func filterLines(lines []Line, format string, opts Options) ([]Line, int) {
	needle := strings.ToLower(opts.Search)
	level := strings.ToLower(opts.Level)

	kept := make([]Line, 0, len(lines))
	filtered := 0

	for _, line := range lines {
		// Carriage returns and the escape sequences a colourising daemon writes
		// are removed here rather than in the browser: they are noise in a
		// viewer, and an escape sequence that reaches a terminal — an operator
		// piping the download through `cat` — can move the cursor and rewrite
		// what was already printed.
		line.Text = sanitise(line.Text)

		if len(line.Text) > MaxLineBytes {
			line.Text = line.Text[:MaxLineBytes]
			line.Truncated = true
		}
		line.Level = levelOf(format, line.Text)

		if needle != "" && !strings.Contains(strings.ToLower(line.Text), needle) {
			filtered++
			continue
		}
		// An unreadable level is never filtered out: hiding a line because this
		// package failed to recognise its shape is how a viewer starts lying.
		if level != "" && line.Level != "" && line.Level != level {
			filtered++
			continue
		}

		kept = append(kept, line)
	}
	return kept, filtered
}

// sanitise strips control characters that have no business in a viewer.
//
// Tabs are kept, because they are how many logs align their fields. Everything
// else below space goes, along with the ESC that begins an ANSI sequence.
func sanitise(text string) string {
	if !strings.ContainsFunc(text, isControl) {
		return text
	}

	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if isControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isControl(r rune) bool {
	return (r < 0x20 && r != '\t') || r == 0x7f
}

// Chunk returns raw bytes of a log for download.
//
// Raw rather than the parsed lines above: a download is for feeding to
// something else — grep, an incident report, a colleague — and a file that has
// been reformatted on the way out is not the file the daemon wrote.
func (p *Provider) Chunk(source Source, offset int64, length int) ([]byte, bool, int64, error) {
	if offset < 0 {
		return nil, false, 0, fmt.Errorf("%w: a negative offset", ErrInvalidOption)
	}
	if length <= 0 || length > MaxChunkBytes {
		length = MaxChunkBytes
	}

	path, info, err := p.resolve(source)
	if err != nil {
		return nil, false, 0, err
	}
	size := info.Size()

	if offset >= size {
		return nil, true, size, nil
	}

	if int64(length) > size-offset {
		length = int(size - offset)
	}

	file, err := os.Open(path) //nolint:gosec // path came from the catalogue and through pathsec
	if err != nil {
		return nil, false, 0, fmt.Errorf("open log: %w", err)
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			p.log.Warn("closing a log file failed", "path", path, "error", cerr.Error())
		}
	}()

	buf := make([]byte, length)
	read, err := file.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, 0, fmt.Errorf("read log: %w", err)
	}

	return buf[:read], offset+int64(read) >= size, size, nil
}

// MaxChunkBytes is the most one download chunk may carry. It matches the file
// manager's chunk size, and both sit under the socket's frame limit.
const MaxChunkBytes = 512 << 10
