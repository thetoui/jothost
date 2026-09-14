package validate_test

import (
	"path"
	"strings"
	"testing"

	"github.com/jothost/panel/shared/validate"
)

// The validators here all guard the same shape of danger: a value that is
// concatenated into a path, a shell-free but still string-built config file, or
// an SQL identifier. Each fuzz target states the property that the concatenation
// stays safe, and checks it against an independent reading rather than against
// the validator itself.

// FuzzDocumentRoot: an accepted document root, joined onto the site directory,
// must stay inside that directory. This is the traversal boundary for every
// site's files, so an accepted value that escaped it would reach another site.
func FuzzDocumentRoot(f *testing.F) {
	for _, s := range []string{"", "public", "public/dist", "a/b/c", "../etc", "/etc/passwd", "a/../b", "a\\b", "."} {
		f.Add(s)
	}
	const siteDir = "/var/www/site.example"

	f.Fuzz(func(t *testing.T, relative string) {
		cleaned, err := validate.DocumentRoot(relative)
		if err != nil {
			return
		}
		// An accepted value is returned already clean and relative.
		if cleaned != "" {
			if path.IsAbs(cleaned) || cleaned != path.Clean(cleaned) {
				t.Fatalf("accepted a document root that is not clean-relative: %q -> %q", relative, cleaned)
			}
			for _, seg := range strings.Split(cleaned, "/") {
				if seg == ".." {
					t.Fatalf("accepted a document root with a traversal segment: %q", cleaned)
				}
			}
		}
		// And composed onto the site directory, it stays underneath it.
		full := validate.DocumentRootPath(siteDir, cleaned)
		if full != siteDir && !strings.HasPrefix(full, siteDir+"/") {
			t.Fatalf("document root %q escaped the site directory: %q", relative, full)
		}
	})
}

// FuzzBackupKey: an accepted key must be a normalised relative path with no
// segment that could escape a bucket prefix, an SFTP path or a local directory.
func FuzzBackupKey(f *testing.F) {
	for _, s := range []string{"a", "a/b/c.tar.gz", "../x", "/x", "a//b", "a/./b", "a\\b", "-rf", ".."} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, key string) {
		if validate.BackupKey(key) != nil {
			return
		}
		if strings.HasPrefix(key, "/") || strings.HasPrefix(key, "-") {
			t.Fatalf("accepted a key that starts with / or -: %q", key)
		}
		if strings.ContainsAny(key, "\\\x00") {
			t.Fatalf("accepted a key with a backslash or null byte: %q", key)
		}
		if path.Clean(key) != key {
			t.Fatalf("accepted a key that is not normalised: %q", key)
		}
		for _, seg := range strings.Split(key, "/") {
			if seg == "" || seg == "." || seg == ".." {
				t.Fatalf("accepted a key with an unsafe segment %q: %q", seg, key)
			}
		}
	})
}

// FuzzDatabaseIdentifier: an accepted identifier is safe to quote into a
// statement — lowercase letters, digits and underscores, starting with a
// letter, and never a pg_ name.
func FuzzDatabaseIdentifier(f *testing.F) {
	for _, s := range []string{"shop", "a_b_1", "1abc", "pg_x", "a-b", "DROP", "a;b", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if validate.DatabaseIdentifier(name) != nil {
			return
		}
		if name == "" || strings.HasPrefix(name, "pg_") {
			t.Fatalf("accepted a reserved or empty identifier: %q", name)
		}
		for i, r := range name {
			ok := r == '_' || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9')
			if !ok {
				t.Fatalf("accepted an identifier with %q at %d: %q", r, i, name)
			}
		}
	})
}

// FuzzDomain: an accepted domain carries nothing that would break out of an
// nginx server_name, a certbot argument or a URL — it is letters, digits, dots
// and hyphens, with real labels.
func FuzzDomain(f *testing.F) {
	for _, s := range []string{"example.com", "a.b.example.com", "xn--e1afmkfd.xn--p1ai", "-a.com", "a..b", "a b.com", "a.com/x", "*.example.com"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, domain string) {
		if validate.Domain(domain) != nil {
			return
		}
		for _, r := range domain {
			ok := r == '.' || r == '-' || (r >= 'a' && r <= 'z') ||
				(r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			if !ok {
				t.Fatalf("accepted a domain with %q: %q", r, domain)
			}
		}
		if strings.Contains(domain, "..") || strings.HasPrefix(domain, ".") || strings.HasPrefix(domain, "-") {
			t.Fatalf("accepted a malformed domain: %q", domain)
		}
	})
}
