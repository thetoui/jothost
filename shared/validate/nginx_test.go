package validate_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jothost/panel/shared/validate"
)

// TestDirectivesAcceptWhatPeopleActuallyWrite.
//
// A validator that refuses ordinary configuration is one somebody works around
// by editing the vhost by hand, which is worse than not having the feature.
func TestDirectivesAcceptWhatPeopleActuallyWrite(t *testing.T) {
	cases := map[string]string{
		"nothing at all":     "",
		"only whitespace":    "   \n\t\n  ",
		"a single directive": `add_header X-Robots-Tag "noindex" always;`,
		"a location block": `
location /assets/ {
    expires 30d;
    add_header Cache-Control "public, immutable";
}`,
		"nested blocks": `
location /api/ {
    if ($request_method = OPTIONS) {
        return 204;
    }
    proxy_read_timeout 120s;
}`,
		"a rewrite with braces in a regex": `
rewrite "^/old/([a-z]{2,4})/(.*)$" /new/$1/$2 permanent;`,
		"a comment mentioning a closing brace": `
# closing the block with } would be bad
add_header X-Test "1";`,
		"a quoted string containing braces": `
add_header Content-Security-Policy "default-src 'self'; script-src 'self'";`,
		"a header whose value contains the word include": `
add_header Strict-Transport-Security "max-age=63072000; includeSubDomains" always;`,
	}

	for name, directives := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validate.NginxDirectives(directives); err != nil {
				t.Fatalf("refused legitimate configuration: %v", err)
			}
		})
	}
}

// TestDirectivesRefuseEscapingTheServerBlock is the attack this exists for.
//
// The text is written inside the site's `server { ... }`. A leading `}` closes
// it, and everything after is top-level configuration: a second server block
// answering for somebody else's domain, or a setting applied to the whole host.
func TestDirectivesRefuseEscapingTheServerBlock(t *testing.T) {
	escapes := map[string]string{
		"a bare closing brace": `}`,
		"closing then opening a new server": `
}
server {
    listen 80;
    server_name victim.example;
    root /etc;
}
server {`,
		"closing inside otherwise valid config": `
add_header X-Fine "1";
}
server { listen 8080; root /; }
server {`,
		"a closing brace hidden after a comment line": `
# harmless
}
server { root /; }
server {`,
	}

	for name, directives := range escapes {
		t.Run(name, func(t *testing.T) {
			err := validate.NginxDirectives(directives)
			if !errors.Is(err, validate.ErrDirectivesUnbalanced) {
				t.Fatalf("a block that escapes its server must be refused, got %v", err)
			}
		})
	}
}

// TestDirectivesRefuseAnUnclosedBlock.
//
// The mistake rather than the attack, and just as damaging: an unclosed brace
// swallows the closing brace of the server itself, so the next site's block
// becomes part of this one.
func TestDirectivesRefuseAnUnclosedBlock(t *testing.T) {
	err := validate.NginxDirectives("location /x {\n    expires 1d;\n")
	if !errors.Is(err, validate.ErrDirectivesUnbalanced) {
		t.Fatalf("an unclosed block must be refused, got %v", err)
	}
}

// TestDirectivesRefuseAnUnterminatedQuote.
//
// Everything after an unterminated quote is inside a string as far as nginx is
// concerned, which means the braces the panel counted are not the braces nginx
// sees. A validator that got this wrong would be counting a different file.
func TestDirectivesRefuseAnUnterminatedQuote(t *testing.T) {
	err := validate.NginxDirectives(`add_header X-Bad "unterminated;`)
	if !errors.Is(err, validate.ErrDirectivesUnbalanced) {
		t.Fatalf("an unterminated quote must be refused, got %v", err)
	}
}

// TestDirectivesRefuseIndirectionThePanelCannotSee.
func TestDirectivesRefuseForbiddenDirectives(t *testing.T) {
	forbidden := map[string]string{
		"include":           "include /etc/nginx/anything.conf;",
		"include, indented": "    include   /tmp/evil.conf ;",
		"include in a nested block": `
location /x/ {
    include /tmp/evil.conf;
}`,
		"load_module":        "load_module modules/ngx_http_lua_module.so;",
		"a whole http block": "http { server { listen 81; } }",
		"user":               "user root;",
		"worker_processes":   "worker_processes 1;",
	}

	for name, directives := range forbidden {
		t.Run(name, func(t *testing.T) {
			err := validate.NginxDirectives(directives)
			if !errors.Is(err, validate.ErrDirectiveForbidden) {
				t.Fatalf("expected a refusal, got %v", err)
			}
		})
	}
}

// TestDirectivesMatchTheDirectiveNotTheWord.
//
// "include" appears inside includeSubDomains, and a validator matching
// substrings would refuse the single most common security header there is.
func TestDirectivesMatchTheDirectiveNotTheWord(t *testing.T) {
	fine := []string{
		`add_header Strict-Transport-Security "max-age=31536000; includeSubDomains";`,
		`add_header X-Note "please include this";`,
		`set $mode "user";`,
	}
	for _, directives := range fine {
		if err := validate.NginxDirectives(directives); err != nil {
			t.Errorf("refused %q: %v", directives, err)
		}
	}
}

// TestDirectivesRefuseControlCharacters.
//
// A NUL truncates the file for anything reading it as a C string, which is a
// way to make what nginx parses differ from what the panel displays.
func TestDirectivesRefuseControlCharacters(t *testing.T) {
	err := validate.NginxDirectives("add_header X-A \"1\";\x00}\nserver { root /; }")
	if !errors.Is(err, validate.ErrDirectivesInvalidCharacter) {
		t.Fatalf("a NUL byte must be refused, got %v", err)
	}
}

func TestDirectivesAreBounded(t *testing.T) {
	huge := strings.Repeat("add_header X-Pad \"x\";\n", 2000)
	err := validate.NginxDirectives(huge)
	if !errors.Is(err, validate.ErrDirectivesTooLong) {
		t.Fatalf("an oversized block must be refused, got %v", err)
	}
}

// TestDirectivesDoNotClaimToMakeConfigurationSafe.
//
// Recorded as a test because it is the thing most likely to be misunderstood
// about this validator. `root /` inside a valid block is accepted here: it is
// not a syntax error, and no parser could call it one. What stops it is the
// permission that gates the feature and the audit entry naming who used it —
// not this function. A future change that "hardened" the validator into a
// denylist of dangerous-looking settings would be adding the appearance of
// safety without the substance.
func TestDirectivesDoNotClaimToMakeConfigurationSafe(t *testing.T) {
	if err := validate.NginxDirectives("root /;"); err != nil {
		t.Fatalf("this validator checks structure, not intent: %v", err)
	}
}
