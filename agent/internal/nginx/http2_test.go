package nginx

import (
	"strings"
	"testing"
)

// Which spelling of the HTTP/2 switch a vhost gets.
//
// nginx moved it in 1.25.1: before that it is a parameter on the listen
// directive, from it a directive of its own, and each version rejects the
// other's outright. Getting it wrong does not produce a site without HTTP/2 —
// nginx refuses the whole configuration, so a reload leaves every site on the
// host serving whatever it had before, and the panel reports the change as
// applied.
//
// Both spellings are live on platforms this panel supports: Debian 12 ships
// 1.22 and Alpine 3.21 ships 1.26. The installer failed on Debian at the step
// that writes its own vhost, and every HTTPS website would have failed the
// same way, until this was version-aware.

func secureSite(legacy bool) SiteConfig {
	return SiteConfig{
		PrimaryDomain: "example.test",
		DocumentRoot:  "/var/www/example.test/public",
		AccessLog:     "/var/www/example.test/logs/access.log",
		ErrorLog:      "/var/www/example.test/logs/error.log",
		LegacyHTTP2:   legacy,
		SSL: &SSLConfig{
			CertificatePath: "/etc/ssl/example.test/fullchain.pem",
			PrivateKeyPath:  "/etc/ssl/example.test/privkey.pem",
		},
	}
}

func TestModernNginxGetsTheDirective(t *testing.T) {
	out, err := Render(secureSite(false))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "http2 on;") {
		t.Fatalf("no http2 directive:\n%s", out)
	}
	if strings.Contains(out, "ssl http2") {
		t.Fatalf("the listen directive also carries http2, which 1.25.1+ warns about:\n%s", out)
	}
}

func TestOlderNginxGetsItOnTheListenDirective(t *testing.T) {
	out, err := Render(secureSite(true))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "listen 443 ssl http2;") {
		t.Fatalf("http2 is not on the listen directive:\n%s", out)
	}
	if !strings.Contains(out, "listen [::]:443 ssl http2;") {
		t.Fatalf("the IPv6 listen directive was left without it:\n%s", out)
	}
	// The one that broke Debian: nginx 1.22 has no such directive and refuses
	// the file.
	if strings.Contains(out, "http2 on;") {
		t.Fatalf("a version that has no http2 directive was given one:\n%s", out)
	}
}

func TestTheVersionBoundaryIsWhereNginxMovedIt(t *testing.T) {
	legacy := []string{"1.18.0", "1.22.1", "1.24.0", "1.25.0", "0.9.9"}
	modern := []string{"1.25.1", "1.25.2", "1.26.3", "1.29.0", "2.0.0"}

	for _, version := range legacy {
		if !olderThan(version, http2DirectiveSince) {
			t.Errorf("%s should use the listen parameter", version)
		}
	}
	for _, version := range modern {
		if olderThan(version, http2DirectiveSince) {
			t.Errorf("%s should use the http2 directive", version)
		}
	}
}

func TestAnUnreadableVersionIsTreatedAsModern(t *testing.T) {
	// The alternative is to assume the old spelling whenever the version
	// cannot be read, which breaks the common case to protect the rare one.
	// Every current distribution and nginx's own packages are past 1.25.1.
	for _, version := range []string{"", "not a version", "1.26", "1"} {
		if olderThan(version, http2DirectiveSince) {
			t.Errorf("%q was treated as an old nginx", version)
		}
	}
}
