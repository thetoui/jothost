package logs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The reader is tested against real files in a temporary root rather than a
// filesystem abstraction, because most of what can go wrong here is about
// actual files: a line still being written, a file replaced under a reader, a
// symlink pointing somewhere it should not.

// provider builds a Provider confined to a temporary directory, and returns a
// source naming one file inside it.
func provider(t *testing.T, name, format string, content string) (*Provider, Source, string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("the path checks resolve Unix symlinks")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write log: %v", err)
		}
	}

	p := NewProvider(nil, dir)
	if !p.Available() {
		t.Fatal("the provider reports itself unavailable for a directory that exists")
	}
	return p, Source{Key: "test", Label: "Test", Format: format, Paths: []string{path}}, path
}

func texts(lines []Line) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, line.Text)
	}
	return out
}

func TestTailReturnsTheEndOfTheFile(t *testing.T) {
	p, source, _ := provider(t, "test.log", FormatPlain, "one\ntwo\nthree\nfour\n")

	result, err := p.Tail(source, Options{Lines: 2})
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}

	got := texts(result.Lines)
	if len(got) != 2 || got[0] != "three" || got[1] != "four" {
		t.Fatalf("lines = %q, want the last two", got)
	}
	if result.Offset != result.Size {
		t.Fatalf("offset = %d, want the end of the file (%d)", result.Offset, result.Size)
	}
}

// A log is being written while it is being read. The half-written last line
// must not be shown, and must not be skipped either — it belongs to the next
// read, whole.
func TestTailLeavesAPartialLineForTheNextRead(t *testing.T) {
	p, source, path := provider(t, "test.log", FormatPlain, "complete\npart")

	first, err := p.Tail(source, Options{})
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if got := texts(first.Lines); len(got) != 1 || got[0] != "complete" {
		t.Fatalf("lines = %q, want only the complete line", got)
	}
	if first.Offset != int64(len("complete\n")) {
		t.Fatalf("offset = %d, want the end of the complete line", first.Offset)
	}

	// The writer finishes the line.
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := file.WriteString("ial\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := p.Tail(source, Options{After: first.Offset})
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if got := texts(second.Lines); len(got) != 1 || got[0] != "partial" {
		t.Fatalf("lines = %q, want the line that was half-written, whole", got)
	}
}

func TestTailFollowsFromAnOffset(t *testing.T) {
	p, source, path := provider(t, "test.log", FormatPlain, "first\n")

	first, err := p.Tail(source, Options{})
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}

	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0o644); err != nil {
		t.Fatalf("append: %v", err)
	}

	second, err := p.Tail(source, Options{After: first.Offset})
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if got := texts(second.Lines); len(got) != 1 || got[0] != "second" {
		t.Fatalf("lines = %q, want only what was appended", got)
	}
	if second.Rotated {
		t.Fatal("a file that grew was reported as rotated")
	}
}

// Rotation is the case a follower must not get wrong. The panel says the file
// was replaced rather than showing what looks like a gap.
func TestTailReportsRotation(t *testing.T) {
	p, source, path := provider(t, "test.log", FormatPlain,
		"long line one\nlong line two\nlong line three\n")

	first, err := p.Tail(source, Options{})
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}

	// logrotate moves the file aside and the daemon opens a new, shorter one.
	if err := os.WriteFile(path, []byte("fresh\n"), 0o644); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	second, err := p.Tail(source, Options{After: first.Offset})
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if !second.Rotated {
		t.Fatal("a file that shrank under the caller was not reported as rotated")
	}
	if got := texts(second.Lines); len(got) != 1 || got[0] != "fresh" {
		t.Fatalf("lines = %q, want the new file's contents", got)
	}
}

func TestTailSearchesWithoutCase(t *testing.T) {
	p, source, _ := provider(t, "test.log", FormatPlain,
		"GET /index.php\nGET /favicon.ico\nPOST /INDEX.php\n")

	result, err := p.Tail(source, Options{Search: "index.php"})
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if got := texts(result.Lines); len(got) != 2 {
		t.Fatalf("lines = %q, want both spellings of index.php", got)
	}
	if result.Filtered != 1 {
		t.Fatalf("filtered = %d, want 1: the page says how much was hidden", result.Filtered)
	}
}

func TestTailFiltersByLevel(t *testing.T) {
	// Real nginx error lines.
	content := `2026/08/31 00:34:01 [error] 12#12: *3 open() "/var/www/x" failed (2: No such file or directory)
2026/08/31 00:34:02 [warn] 12#12: *4 upstream server temporarily disabled
2026/08/31 00:34:03 [notice] 12#12: signal process started
`
	p, source, _ := provider(t, "error.log", FormatNginxError, content)

	result, err := p.Tail(source, Options{Level: LevelError})
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if got := texts(result.Lines); len(got) != 1 || !strings.Contains(got[0], "[error]") {
		t.Fatalf("lines = %q, want only the error", got)
	}
}

// A line whose level cannot be read must never be hidden by a level filter.
// Hiding it would mean the panel decides what an operator may see based on
// whether this package recognised the shape of the line.
func TestLevelFilterKeepsLinesWithNoReadableLevel(t *testing.T) {
	content := "2026/08/31 00:34:01 [error] something broke\na line in no particular format\n"
	p, source, _ := provider(t, "error.log", FormatNginxError, content)

	result, err := p.Tail(source, Options{Level: LevelError})
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if got := texts(result.Lines); len(got) != 2 {
		t.Fatalf("lines = %q, want the unrecognised line kept alongside the error", got)
	}
}

func TestTailTruncatesAnEnormousLine(t *testing.T) {
	huge := strings.Repeat("x", MaxLineBytes*2)
	p, source, _ := provider(t, "test.log", FormatPlain, huge+"\n")

	result, err := p.Tail(source, Options{})
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(result.Lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(result.Lines))
	}
	if got := len(result.Lines[0].Text); got != MaxLineBytes {
		t.Fatalf("line length = %d, want it capped at %d", got, MaxLineBytes)
	}
	if !result.Lines[0].Truncated {
		t.Fatal("a truncated line did not say so")
	}
}

// The scan window is what stops a month of logging from being loaded into
// memory to show the last twenty lines.
func TestTailBoundsHowFarBackItReads(t *testing.T) {
	line := strings.Repeat("y", 999) + "\n"
	content := strings.Repeat(line, (MaxScanBytes/len(line))+50)
	p, source, _ := provider(t, "big.log", FormatPlain, content)

	result, err := p.Tail(source, Options{Lines: 10})
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if result.Scanned > MaxScanBytes {
		t.Fatalf("scanned %d bytes, want at most %d", result.Scanned, MaxScanBytes)
	}
	if !result.Partial {
		t.Fatal("a read that could not reach the start of the file did not say so")
	}
	if len(result.Lines) != 10 {
		t.Fatalf("lines = %d, want 10", len(result.Lines))
	}
}

// Control characters are stripped rather than passed through: an escape
// sequence reaching a terminal can rewrite what was already printed there.
func TestTailStripsControlCharacters(t *testing.T) {
	p, source, _ := provider(t, "test.log", FormatPlain,
		"before\x1b[2Jafter\ttab\r\n")

	result, err := p.Tail(source, Options{})
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	text := result.Lines[0].Text
	if strings.ContainsAny(text, "\x1b\r") {
		t.Fatalf("line = %q, want the control characters gone", text)
	}
	if !strings.Contains(text, "\t") {
		t.Fatalf("line = %q, want the tab kept: logs align fields with it", text)
	}
}

func TestTailReportsAnAbsentLog(t *testing.T) {
	p, source, path := provider(t, "test.log", FormatPlain, "x\n")
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if _, err := p.Tail(source, Options{}); err == nil {
		t.Fatal("reading a log that does not exist was not an error")
	}
}

// The one that matters most: a log path that is a symlink out of the allowed
// roots is refused. nginx's access log is writable by the account nginx runs
// as on many hosts, so this is the path from "the web server was compromised"
// to "the panel reads any file on the machine".
func TestASymlinkOutOfTheRootsIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ")
	}

	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("a private key\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	link := filepath.Join(dir, "access.log")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	p := NewProvider(nil, dir)
	source := Source{Key: "nginx.access", Format: FormatPlain, Paths: []string{link}}

	if _, err := p.Tail(source, Options{}); err == nil {
		t.Fatal("a log symlinked outside the allowed roots was read")
	}

	detected := p.Detect([]Source{source})
	for _, entry := range detected {
		if entry.Key == "nginx.access" && entry.Present {
			t.Fatal("a log symlinked outside the allowed roots was reported as present")
		}
	}
}

func TestChunkReturnsRawBytes(t *testing.T) {
	p, source, _ := provider(t, "test.log", FormatPlain, "alpha\nbeta\n")

	data, eof, size, err := p.Chunk(source, 0, 6)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if string(data) != "alpha\n" {
		t.Fatalf("data = %q, want the first six bytes verbatim", data)
	}
	if eof {
		t.Fatal("a chunk that stopped short of the end reported EOF")
	}
	if size != 11 {
		t.Fatalf("size = %d, want 11", size)
	}

	data, eof, _, err = p.Chunk(source, 6, MaxChunkBytes)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if string(data) != "beta\n" || !eof {
		t.Fatalf("data = %q eof = %v, want the rest and EOF", data, eof)
	}
}

func TestLookupRejectsAnythingNotInTheCatalogue(t *testing.T) {
	for _, key := range []string{
		"", "nginx", "../../etc/passwd", "/var/log/messages",
		"nginx.access ", "NGINX.ACCESS", "a..b", strings.Repeat("x", 200),
	} {
		if _, err := Lookup(key, nil); err == nil {
			t.Fatalf("%q was accepted as a log key", key)
		}
	}

	if _, err := Lookup("nginx.access", nil); err != nil {
		t.Fatalf("a catalogued key was refused: %v", err)
	}
}

func TestDetectListsAbsentLogsToo(t *testing.T) {
	p, _, _ := provider(t, "test.log", FormatPlain, "x\n")

	detected := p.Detect(nil)
	if len(detected) != len(Catalogue()) {
		t.Fatalf("detected %d sources, want all %d catalogued",
			len(detected), len(Catalogue()))
	}
	for _, entry := range detected {
		// Nothing in the real catalogue resolves inside the temporary root, so
		// every entry should be listed and absent — which is the point: the
		// picker shows what the panel offers, and says which of it is here.
		if entry.Present {
			t.Fatalf("%s was reported present outside the allowed root", entry.Key)
		}
		if entry.Path != "" {
			t.Fatalf("%s reported a path while absent: %q", entry.Key, entry.Path)
		}
	}
}

func TestPHPSourcesRefuseAVersionThatIsNotOne(t *testing.T) {
	sources := PHPSources([]string{"8.4", "../../etc", "8.4; rm -rf /", ""})
	if len(sources) != 1 {
		t.Fatalf("got %d sources, want only the real version", len(sources))
	}
	if sources[0].Key != "php.8.4" {
		t.Fatalf("key = %q, want php.8.4", sources[0].Key)
	}
}

func TestLevelReading(t *testing.T) {
	cases := []struct {
		name   string
		format string
		line   string
		want   string
	}{
		{
			"nginx error",
			FormatNginxError,
			`2026/08/31 00:34:01 [error] 12#12: *3 open() failed`,
			LevelError,
		},
		{
			"apache qualifies the level with its module",
			FormatNginxError,
			`[Sun Aug 31 00:34:01 2026] [proxy:error] [pid 42] AH00898: read failed`,
			LevelError,
		},
		{
			"an access log's level is its status",
			FormatNginxAccess,
			`1.2.3.4 - - [31/Aug/2026:00:34:01 +0000] "GET / HTTP/1.1" 500 612 "-" "curl/8.5.0"`,
			LevelError,
		},
		{
			"a 404 is a warning",
			FormatNginxAccess,
			`1.2.3.4 - - [31/Aug/2026:00:34:01 +0000] "GET /nope HTTP/1.1" 404 153 "-" "curl/8.5.0"`,
			LevelWarn,
		},
		{
			"a 200 is information",
			FormatNginxAccess,
			`1.2.3.4 - - [31/Aug/2026:00:34:01 +0000] "GET / HTTP/1.1" 200 612 "-" "curl/8.5.0"`,
			LevelInfo,
		},
		{
			"fpm's own words",
			FormatPHP,
			`[31-Aug-2026 00:34:01] WARNING: [pool web] child 42 exited on signal 11`,
			LevelWarn,
		},
		{
			"the engine's words",
			FormatPHP,
			`[31-Aug-2026 00:34:01 UTC] PHP Fatal error:  Uncaught Error: Call to undefined`,
			LevelError,
		},
		{
			"the panel's own structured logs",
			FormatJSON,
			`{"time":"2026-08-31T00:34:01Z","level":"ERROR","msg":"operation failed"}`,
			LevelError,
		},
		{
			"the agent's audit log records an outcome, not a level",
			FormatAudit,
			`{"timestamp":"2026-08-31T03:27:20Z","operation":"website.create","status":"FAILURE"}`,
			LevelError,
		},
		{
			"and a successful operation is information",
			FormatAudit,
			`{"timestamp":"2026-08-31T03:27:20Z","operation":"agent.ping","status":"SUCCESS"}`,
			LevelInfo,
		},
		{
			"a syslog line that says it failed",
			FormatSyslog,
			`Aug 31 00:34:01 host crond[42]: (root) FAILED to open PAM security session`,
			LevelError,
		},
		{
			"a hostname containing a level word is not a level",
			FormatSyslog,
			`Aug 31 00:34:01 error-node-1 crond[42]: (root) CMD (run-parts /etc/cron.hourly)`,
			"",
		},
		{
			"a word merely containing a level word is not one",
			FormatSyslog,
			`Aug 31 00:34:01 host app[42]: terrorist attack ad from a scanner`,
			"",
		},
		{
			"plain output has no level to read",
			FormatPlain,
			`Listening on port 3000`,
			"",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := levelOf(tc.format, tc.line); got != tc.want {
				t.Fatalf("level = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTailRefusesOptionsOutsideItsBounds(t *testing.T) {
	p, source, _ := provider(t, "test.log", FormatPlain, "x\n")

	if _, err := p.Tail(source, Options{Search: strings.Repeat("x", MaxSearchLength+1)}); err == nil {
		t.Fatal("an oversized search was accepted")
	}
	if _, err := p.Tail(source, Options{Level: "critical"}); err == nil {
		t.Fatal("a level outside the vocabulary was accepted")
	}
	if _, err := p.Tail(source, Options{After: -1}); err == nil {
		t.Fatal("a negative offset was accepted")
	}

	// And the line count is clamped rather than refused: asking for more than
	// the maximum is a caller wanting "as much as possible".
	result, err := p.Tail(source, Options{Lines: MaxLines * 10})
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(result.Lines) > MaxLines {
		t.Fatalf("returned %d lines, want at most %d", len(result.Lines), MaxLines)
	}
}
