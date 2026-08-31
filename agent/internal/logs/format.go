package logs

import (
	"strconv"
	"strings"
)

// Reading a severity out of a log line.
//
// Every daemon writes its own format, and the panel offers one filter across
// all of them. That filter is only worth having if "errors only" means the same
// thing on each page, so each format is parsed for the word it actually uses
// and mapped onto one small set.
//
// The set is deliberately small — error, warn, info, debug — because it is what
// an operator filters on. syslog's eight priorities and nginx's eight levels
// collapse into it without losing the distinction anyone acts upon: something
// is broken, something might be, or something happened.
//
// A line whose level cannot be read gets an empty one, and an empty level is
// never filtered out by a level filter. Guessing would be worse: hiding a line
// because this file failed to recognise its shape is how an operator concludes
// the panel is lying to them.
const (
	LevelError = "error"
	LevelWarn  = "warn"
	LevelInfo  = "info"
	LevelDebug = "debug"
)

// Levels is the filter vocabulary, most severe first.
func Levels() []string { return []string{LevelError, LevelWarn, LevelInfo, LevelDebug} }

// ValidLevel reports whether a filter value is one this package understands.
func ValidLevel(level string) bool {
	switch strings.ToLower(level) {
	case "", LevelError, LevelWarn, LevelInfo, LevelDebug:
		return true
	}
	return false
}

// levelOf reads the severity of one line in a given format.
func levelOf(format, line string) string {
	switch format {
	case FormatNginxError:
		return nginxErrorLevel(line)
	case FormatNginxAccess:
		return accessLevel(line)
	case FormatPHP:
		return phpLevel(line)
	case FormatJSON:
		return jsonLevel(line)
	case FormatAudit:
		return auditLevel(line)
	case FormatSyslog:
		return syslogLevel(line)
	default:
		return ""
	}
}

// nginxErrorLevel reads the bracketed level from an nginx error line:
//
//	2026/08/31 00:34:01 [error] 12#12: *3 open() "/var/www/x" failed
//
// Apache's error log is close enough to share this: its level is bracketed the
// same way, as [error] or [proxy:error].
func nginxErrorLevel(line string) string {
	// Every bracketed group is tried, not just the first. nginx puts the level
	// in the first one; Apache puts a timestamp there and the level in the
	// second — [Sun Aug 31 00:34:01 2026] [proxy:error] [pid 42] — and reading
	// only the first would find no level in any Apache line ever written.
	rest := line
	for {
		open := strings.IndexByte(rest, '[')
		if open < 0 {
			return ""
		}
		shut := strings.IndexByte(rest[open:], ']')
		if shut < 0 {
			return ""
		}

		word := rest[open+1 : open+shut]
		// Apache qualifies the level with the module that raised it:
		// [proxy:error].
		if colon := strings.LastIndexByte(word, ':'); colon >= 0 {
			word = word[colon+1:]
		}

		switch strings.ToLower(strings.TrimSpace(word)) {
		case "emerg", "alert", "crit", "error":
			return LevelError
		case "warn", "warning":
			return LevelWarn
		case "notice", "info":
			return LevelInfo
		case "debug", "trace1", "trace2", "trace3":
			return LevelDebug
		}

		rest = rest[open+shut:]
	}
}

// accessLevel derives a level from the response status.
//
// An access log has no severity of its own, which makes "show me the errors"
// unanswerable on the one log an operator most often wants to ask it about. The
// status code is the honest answer: a 500 is an error and a 404 is a warning,
// whatever the daemon calls them.
//
// The status is read as the token after the quoted request, which is where both
// the combined and common formats put it:
//
//	1.2.3.4 - - [31/Aug/2026:00:34:01 +0000] "GET / HTTP/1.1" 200 612 "-" "curl"
func accessLevel(line string) string {
	end := strings.LastIndexByte(line, '"')
	if end < 0 {
		return ""
	}
	// Walk back to the quote that opened the request. Scanning from the right
	// would find the user agent's quotes instead.
	open := strings.IndexByte(line, '"')
	if open < 0 || open >= end {
		return ""
	}
	closing := strings.IndexByte(line[open+1:], '"')
	if closing < 0 {
		return ""
	}

	rest := strings.TrimLeft(line[open+1+closing+1:], " ")
	field, _, _ := strings.Cut(rest, " ")
	status, err := strconv.Atoi(field)
	if err != nil {
		return ""
	}

	switch {
	case status >= 500:
		return LevelError
	case status >= 400:
		return LevelWarn
	case status >= 100:
		return LevelInfo
	}
	return ""
}

// phpLevel reads PHP's own severity words.
//
// FPM writes them after the timestamp:
//
//	[31-Aug-2026 00:34:01] WARNING: [pool web] child 42 exited on signal 11
//
// and the engine writes them inside the message:
//
//	[31-Aug-2026 00:34:01 UTC] PHP Fatal error:  Uncaught Error: ...
func phpLevel(line string) string {
	rest := line
	if shut := strings.IndexByte(line, ']'); shut >= 0 {
		rest = line[shut+1:]
	}
	rest = strings.ToLower(strings.TrimSpace(rest))
	rest = strings.TrimPrefix(rest, "php ")

	switch {
	case strings.HasPrefix(rest, "fatal"),
		strings.HasPrefix(rest, "error"),
		strings.HasPrefix(rest, "parse error"),
		strings.HasPrefix(rest, "alert"),
		strings.HasPrefix(rest, "emergency"):
		return LevelError
	case strings.HasPrefix(rest, "warning"),
		strings.HasPrefix(rest, "deprecated"):
		return LevelWarn
	case strings.HasPrefix(rest, "notice"),
		strings.HasPrefix(rest, "info"):
		return LevelInfo
	case strings.HasPrefix(rest, "debug"):
		return LevelDebug
	}
	return ""
}

// jsonLevel reads the level field of a structured line.
//
// The panel's own logs are JSON (ARCHITECTURE section 12), and the field is
// found textually rather than by decoding: a log file being read for display
// should not be able to cost a full JSON parse per line, and a line that is
// truncated or interleaved — which is what a crash looks like — still yields
// its level this way.
func jsonLevel(line string) string {
	const marker = `"level":`
	at := strings.Index(line, marker)
	if at < 0 {
		return ""
	}

	rest := strings.TrimLeft(line[at+len(marker):], " ")
	rest = strings.TrimPrefix(rest, `"`)
	end := strings.IndexAny(rest, `",}`)
	if end < 0 {
		return ""
	}

	switch strings.ToLower(rest[:end]) {
	case "error", "fatal", "panic", "critical":
		return LevelError
	case "warn", "warning":
		return LevelWarn
	case "info", "notice":
		return LevelInfo
	case "debug", "trace":
		return LevelDebug
	}
	return ""
}

// auditLevel reads the Agent's audit log, which records an outcome rather than
// a severity.
//
// The distinction is the point of the file: every line is a privileged
// operation that was attempted, and what an operator wants from it is the ones
// that failed. Without this, "errors only" on the panel's own audit log would
// return nothing at all, which reads as "nothing has ever gone wrong".
func auditLevel(line string) string {
	if level := jsonLevel(line); level != "" {
		return level
	}

	const marker = `"status":`
	at := strings.Index(line, marker)
	if at < 0 {
		return ""
	}

	rest := strings.TrimLeft(line[at+len(marker):], " ")
	rest = strings.TrimPrefix(rest, `"`)
	end := strings.IndexAny(rest, `",}`)
	if end < 0 {
		return ""
	}

	switch strings.ToUpper(rest[:end]) {
	case "FAILURE", "FAILED", "ERROR":
		return LevelError
	case "SUCCESS", "OK":
		return LevelInfo
	}
	return ""
}

// syslogLevel reads what a syslog-style line says about itself.
//
// Traditional syslog files carry no priority — it was consumed by the daemon
// that decided which file to write to — so there is nothing to parse but the
// message. Rather than pretend otherwise, this looks for the words daemons
// actually write, and returns nothing when they are absent.
func syslogLevel(line string) string {
	// The message begins after "host program[pid]:", and searching only there
	// keeps a hostname like "error-node-1" from marking every line an error.
	message := line
	if colon := strings.Index(line, ": "); colon >= 0 {
		message = line[colon+2:]
	}
	lower := strings.ToLower(message)

	switch {
	case containsWord(lower, "error"), containsWord(lower, "failed"),
		containsWord(lower, "failure"), containsWord(lower, "fatal"),
		containsWord(lower, "panic"):
		return LevelError
	case containsWord(lower, "warning"), containsWord(lower, "warn"):
		return LevelWarn
	}
	return ""
}

// containsWord reports whether s contains word bounded by non-letters, so
// "errors" matches and "terrorist" does not.
func containsWord(s, word string) bool {
	from := 0
	for {
		at := strings.Index(s[from:], word)
		if at < 0 {
			return false
		}
		at += from

		beforeOK := at == 0 || !isLetter(s[at-1])
		after := at + len(word)
		afterOK := after >= len(s) || !isLetter(s[after])
		if beforeOK && afterOK {
			return true
		}
		from = at + 1
	}
}

func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
