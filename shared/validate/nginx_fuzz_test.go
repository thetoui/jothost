package validate_test

import (
	"strings"
	"testing"

	"github.com/jothost/panel/shared/validate"
)

// FuzzNginxDirectives holds the invariant the balancer exists for: if the
// validator accepts a block, that block placed inside a `server { ... }` must
// not change the number of blocks, or close the server it sits in.
//
// The oracle is a second, independent brace counter written to nginx's own
// tokenizer rule — a quote or comment is syntax only at the start of a token.
// It is deliberately not the implementation under test: a fuzzer comparing a
// function to itself proves nothing. If the two ever disagree about an accepted
// input, one of them is wrong, and the whole point of this check is that the
// one nginx agrees with wins.
func FuzzNginxDirectives(f *testing.F) {
	seeds := []string{
		"",
		`add_header X "y";`,
		"location /a { expires 1d; }",
		`}`,
		`add_header X a";` + "\n}\nserver {\n",
		"# comment }\nadd_header X 1;",
		`rewrite "^/x([a-z]{2})$" /y permanent;`,
		"set $x a#b;",
		"location / { if ($x = \"1\") { return 204; } }",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, directives string) {
		if validate.NginxDirectives(directives) != nil {
			return // refused: the panel never writes it, so nothing to check.
		}

		// An accepted block must be brace-neutral by nginx's own reading, and
		// must never dip below the server block it is written inside.
		net, minDepth, ok := braceDepth(directives)
		if !ok {
			t.Fatalf("accepted a block with an unterminated quote by nginx's reading:\n%q", directives)
		}
		if net != 0 {
			t.Fatalf("accepted a block that is not brace-neutral (net %d):\n%q", net, directives)
		}
		if minDepth < 0 {
			t.Fatalf("accepted a block that closes its own server block (min depth %d):\n%q",
				minDepth, directives)
		}
	})
}

// braceDepth counts braces the way nginx tokenizes: quotes and comments are
// honoured only at the start of a token. It returns the net change, the lowest
// depth reached (relative to the start), and whether every quote closed.
func braceDepth(s string) (net int, minDepth int, quotesClosed bool) {
	inSingle, inDouble, inComment := false, false, false
	boundary := true
	depth := 0

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inComment:
			if c == '\n' {
				inComment = false
				boundary = true
			}
		case inSingle:
			if c == '\\' {
				i++
			} else if c == '\'' {
				inSingle = false
				boundary = false
			}
		case inDouble:
			if c == '\\' {
				i++
			} else if c == '"' {
				inDouble = false
				boundary = false
			}
		default:
			switch c {
			case '#':
				if boundary {
					inComment = true
				}
				boundary = false
			case '\'':
				if boundary {
					inSingle = true
				}
				boundary = false
			case '"':
				if boundary {
					inDouble = true
				}
				boundary = false
			case '{':
				depth++
				boundary = true
			case '}':
				depth--
				if depth < minDepth {
					minDepth = depth
				}
				boundary = true
			case ' ', '\t', '\n', '\r', ';':
				boundary = true
			default:
				boundary = false
			}
		}
	}
	return depth, minDepth, !inSingle && !inDouble
}

// TestBraceDepthOracleMatchesNginxOnKnownCases anchors the oracle to the real
// nginx behaviour recorded when the escape was found, so the fuzz check is not
// resting on an oracle that drifts.
func TestBraceDepthOracleMatchesNginxOnKnownCases(t *testing.T) {
	// nginx 1.26 loaded a second server block from this; the oracle must see
	// the closing brace that the old validator hid.
	if _, minDepth, _ := braceDepth(`add_header X a";` + "\n}\nserver {\n"); minDepth >= 0 {
		t.Fatal("the oracle failed to see the brace a mid-token quote hides")
	}
	// A genuinely quoted brace is not a brace.
	if net, _, ok := braceDepth(`add_header X "a}b";`); !ok || net != 0 {
		t.Fatalf("the oracle miscounted a quoted brace: net %d, closed %v", net, ok)
	}
	if !strings.Contains("sanity", "an") {
		t.Fatal("unreachable")
	}
}
