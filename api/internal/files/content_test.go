package files_test

import (
	"strings"
	"testing"

	"github.com/jothost/panel/api/internal/files"
)

// Syntax highlighting is chosen from the name, never sniffed from the content:
// a mode guessed from a first line is wrong often enough to be worse than none,
// and the editor has to choose before the file has finished loading.
func TestLanguageForExtensions(t *testing.T) {
	cases := map[string]string{
		"index.php":        "php",
		"app.js":           "javascript",
		"main.tsx":         "typescript",
		"style.css":        "css",
		"site.scss":        "scss",
		"page.html":        "html",
		"data.json":        "json",
		"config.yml":       "yaml",
		"config.yaml":      "yaml",
		"README.md":        "markdown",
		"dump.sql":         "sql",
		"deploy.sh":        "shell",
		"settings.ini":     "ini",
		"script.py":        "python",
		"main.go":          "go",
		"notes.txt":        "plaintext",
		"access.log":       "plaintext",
		"archive.tar.gz":   "plaintext",
		"UPPER.PHP":        "php",
		"no-extension":     "plaintext",
		"trailing.":        "plaintext",
		"":                 "plaintext",
		"weird.unknownxyz": "plaintext",
	}

	for name, want := range cases {
		if got := files.LanguageFor(name); got != want {
			t.Errorf("LanguageFor(%q) = %q, want %q", name, got, want)
		}
	}
}

// The files a hosting panel actually meets often have no extension at all.
func TestLanguageForBareNames(t *testing.T) {
	cases := map[string]string{
		".htaccess":  "apache",
		".env":       "ini",
		"Dockerfile": "dockerfile",
		"Makefile":   "makefile",
		"nginx.conf": "nginx",
		"php.ini":    "ini",
		"robots.txt": "plaintext",
	}

	for name, want := range cases {
		if got := files.LanguageFor(name); got != want {
			t.Errorf("LanguageFor(%q) = %q, want %q", name, got, want)
		}
	}
}

// An exact-name match must win over the extension: nginx.conf is nginx
// configuration, not a generic .conf ini file.
func TestExactNamesBeatExtensions(t *testing.T) {
	if got := files.LanguageFor("nginx.conf"); got != "nginx" {
		t.Fatalf("nginx.conf = %q, want nginx", got)
	}
	if got := files.LanguageFor("other.conf"); got != "ini" {
		t.Fatalf("other.conf = %q, want ini", got)
	}
}

// The editor limit has to match the Agent's, or the panel offers an "edit"
// button on a file the save will then refuse.
func TestEditableLimitIsTwoMegabytes(t *testing.T) {
	if files.MaxEditableBytes != 2*1024*1024 {
		t.Fatalf("MaxEditableBytes = %d, want 2 MiB to match the agent",
			files.MaxEditableBytes)
	}
}

// A path still has to be a path, even on the content endpoints.
func TestContentPathsAreValidatedLikeAnyOther(t *testing.T) {
	for _, path := range []string{
		"",
		"../../etc/passwd",
		"/var/www/../../etc/shadow",
		"relative/file.php",
		"/var/www/site\x00.php",
		"/" + strings.Repeat("a", 5000),
	} {
		if _, err := files.ValidatePath(path); err == nil {
			t.Errorf("ValidatePath(%q) was accepted on the content path", path)
		}
	}
}
