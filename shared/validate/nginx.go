package validate

import (
	"errors"
	"fmt"
	"strings"
)

// Errors returned by nginx directive validation.
var (
	// ErrDirectivesTooLong covers a block past the size a vhost should carry.
	ErrDirectivesTooLong = errors.New("nginx directives are too long")
	// ErrDirectivesUnbalanced covers braces that do not close where they
	// opened — the shape that lets a block escape the server it sits in.
	ErrDirectivesUnbalanced = errors.New("nginx directives have unbalanced braces")
	// ErrDirectiveForbidden covers a directive the panel will not write.
	ErrDirectiveForbidden = errors.New("nginx directive is not allowed here")
	// ErrDirectivesInvalidCharacter covers bytes that have no business in a
	// configuration file.
	ErrDirectivesInvalidCharacter = errors.New("nginx directives contain an invalid character")
)

// MaxDirectivesLength bounds the block.
//
// Generous enough for the rewrites, headers and caching rules people actually
// add, and small enough that a vhost stays something a person can read. A
// configuration measured in megabytes is not a directive block; it is a
// different way of asking for a file upload.
const MaxDirectivesLength = 16 << 10

// forbiddenDirectives are refused wherever they appear in the block.
//
// This is a short list on purpose. The real control on this feature is who may
// use it — writing nginx configuration is server administration, and it is
// gated on the permission that says so. Somebody with that permission can
// already edit nginx by hand over SSH, so a long denylist here would be
// security theatre that mostly gets in the way of legitimate configuration.
//
// What is refused is the small set that would make the panel's own guarantees
// untrue, or that hides what the configuration does:
//
//   - include pulls in a file the panel cannot see. Everything below is
//     reviewable in the panel and in the vhost; an include is a pointer to
//     something that is neither, and it can be swapped out afterwards by
//     anything that can write that path.
//   - load_module loads native code into the web server.
//   - Anything that closes over the whole server rather than this site —
//     `http`, `events`, `stream`, `user`, `pid`, `worker_processes` — belongs
//     to the main configuration, and a site's block is not where a host-wide
//     setting should be smuggled in.
var forbiddenDirectives = []string{
	"include",
	"load_module",
	"http",
	"events",
	"stream",
	"mail",
	"user",
	"pid",
	"worker_processes",
	"worker_connections",
	"daemon",
	"master_process",
	"lua_package_path",
	"lua_shared_dict",
}

// NginxDirectives checks a per-site additional configuration block.
//
// What this can and cannot promise is worth being plain about. It cannot make
// arbitrary nginx configuration safe — a `root /` inside a valid server block
// serves the filesystem, and no parser written here would call that a syntax
// error. What it does is stop the block being something other than what it
// looks like: a fragment that stays inside the server it was written for, with
// no indirection the panel cannot see.
//
// The rest of the safety is elsewhere and is where it belongs: the permission
// that gates the feature, the audit entry recording who changed it, nginx's own
// `-t` before the change is applied, and the rollback that puts the previous
// configuration back when it is refused.
func NginxDirectives(directives string) error {
	if strings.TrimSpace(directives) == "" {
		return nil
	}
	if len(directives) > MaxDirectivesLength {
		return fmt.Errorf("%w: %d bytes, limit is %d",
			ErrDirectivesTooLong, len(directives), MaxDirectivesLength)
	}

	// A NUL truncates the file for everything that reads it as a C string,
	// which is a way to make what nginx parses differ from what the panel
	// shows. A carriage return is allowed — editors produce them — but nothing
	// else outside printable ASCII plus tab and newline.
	for i, r := range directives {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
		case r < 0x20 || r == 0x7f:
			return fmt.Errorf("%w: byte %d at offset %d", ErrDirectivesInvalidCharacter, r, i)
		}
	}

	if err := checkBalance(directives); err != nil {
		return err
	}
	return checkForbidden(directives)
}

// checkBalance walks the block counting braces outside strings and comments.
//
// The depth must never go negative and must end at zero. Negative is the
// attack: a leading `}` closes the server block this text is written into, and
// everything after it is then top-level configuration — a second server block
// claiming another site's name, or a directive applying to the whole host.
// Ending above zero is the mistake: an unclosed brace swallows the rest of the
// vhost, including the closing brace of the server itself.
func checkBalance(directives string) error {
	depth := 0
	inSingle, inDouble, inComment := false, false, false

	for i := 0; i < len(directives); i++ {
		c := directives[i]

		switch {
		case inComment:
			if c == '\n' {
				inComment = false
			}
		case inSingle:
			if c == '\\' {
				i++ // an escaped byte is content, whatever it is
			} else if c == '\'' {
				inSingle = false
			}
		case inDouble:
			if c == '\\' {
				i++
			} else if c == '"' {
				inDouble = false
			}
		default:
			switch c {
			case '#':
				inComment = true
			case '\'':
				inSingle = true
			case '"':
				inDouble = true
			case '{':
				depth++
			case '}':
				depth--
				if depth < 0 {
					return fmt.Errorf("%w: a closing brace at offset %d would end the "+
						"site's own server block", ErrDirectivesUnbalanced, i)
				}
			}
		}
	}

	if inSingle || inDouble {
		return fmt.Errorf("%w: an unterminated quote leaves the rest of the "+
			"configuration inside a string", ErrDirectivesUnbalanced)
	}
	if depth != 0 {
		return fmt.Errorf("%w: %d block(s) are left open", ErrDirectivesUnbalanced, depth)
	}
	return nil
}

// checkForbidden refuses the directives above, wherever they appear.
//
// Matched at the start of a statement rather than anywhere in the text, so a
// header value or a rewrite that happens to contain the word "include" is not
// refused. A statement starts at the beginning of a line or after ; { or }.
func checkForbidden(directives string) error {
	for _, statement := range splitStatements(directives) {
		name := firstWord(statement)
		if name == "" {
			continue
		}
		for _, forbidden := range forbiddenDirectives {
			if strings.EqualFold(name, forbidden) {
				return fmt.Errorf("%w: %q", ErrDirectiveForbidden, name)
			}
		}
	}
	return nil
}

// splitStatements breaks the block where a new directive can begin, ignoring
// comments and the insides of quoted strings.
func splitStatements(directives string) []string {
	var statements []string
	var current strings.Builder
	inSingle, inDouble, inComment := false, false, false

	flush := func() {
		if text := strings.TrimSpace(current.String()); text != "" {
			statements = append(statements, text)
		}
		current.Reset()
	}

	for i := 0; i < len(directives); i++ {
		c := directives[i]

		switch {
		case inComment:
			if c == '\n' {
				inComment = false
			}
		case inSingle:
			current.WriteByte(c)
			if c == '\\' && i+1 < len(directives) {
				i++
				current.WriteByte(directives[i])
			} else if c == '\'' {
				inSingle = false
			}
		case inDouble:
			current.WriteByte(c)
			if c == '\\' && i+1 < len(directives) {
				i++
				current.WriteByte(directives[i])
			} else if c == '"' {
				inDouble = false
			}
		default:
			switch c {
			case '#':
				inComment = true
				flush()
			case '\'':
				inSingle = true
				current.WriteByte(c)
			case '"':
				inDouble = true
				current.WriteByte(c)
			case ';', '{', '}', '\n':
				flush()
			default:
				current.WriteByte(c)
			}
		}
	}
	flush()
	return statements
}

// firstWord returns the directive name a statement begins with.
func firstWord(statement string) string {
	fields := strings.Fields(statement)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
