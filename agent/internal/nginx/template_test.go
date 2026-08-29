package nginx_test

import (
	"strings"
	"testing"

	"github.com/jothost/panel/agent/internal/nginx"
)

// staticSite returns a vhost config that renders, for tests that vary a field.
func staticSite() nginx.SiteConfig {
	return nginx.SiteConfig{
		PrimaryDomain: "example.test",
		DocumentRoot:  "/var/www/example.test/public",
		AccessLog:     "/var/www/example.test/logs/access.log",
		ErrorLog:      "/var/www/example.test/logs/error.log",
	}
}

func phpSite() nginx.SiteConfig {
	site := staticSite()
	site.PHPSocket = "/run/php-fpm/web_example-83.sock"
	return site
}

// ------------------------------------------------------ the PHP execution guard

// This is the most important test in the PHP work.
//
// Without `try_files $uri =404` inside the .php location, nginx passes
// /uploads/avatar.jpg/x.php to FPM, FPM walks back to the real file, and an
// uploaded image is executed as PHP. That is remote code execution, and it is
// the *default* behaviour of the obvious configuration.
func TestPHPLocationRefusesToPassAMissingScript(t *testing.T) {
	rendered, err := nginx.Render(phpSite())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	block := phpLocation(t, rendered)

	guard := strings.Index(block, "try_files $uri =404")
	if guard < 0 {
		t.Fatalf("the .php location has no try_files guard:\n%s", block)
	}

	pass := strings.Index(block, "fastcgi_pass")
	if pass < 0 {
		t.Fatalf("the .php location does not pass to FPM:\n%s", block)
	}

	// Order matters as much as presence: a guard after the pass never runs.
	if guard > pass {
		t.Fatalf("try_files comes after fastcgi_pass, so it never guards it:\n%s", block)
	}
}

// SCRIPT_FILENAME must be built from the resolved root and the matched script
// name. Deriving it from $request_filename or from PATH_INFO is the other half
// of the same remote code execution.
func TestPHPScriptFilenameIsNotClientControlled(t *testing.T) {
	rendered, err := nginx.Render(phpSite())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	block := phpLocation(t, rendered)

	if !strings.Contains(block, "fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name") {
		t.Fatalf("SCRIPT_FILENAME is not built from the resolved root:\n%s", block)
	}
	if strings.Contains(block, "$request_filename") {
		t.Fatalf("SCRIPT_FILENAME uses $request_filename, which follows path info:\n%s", block)
	}
	// Nothing served here needs PATH_INFO, and splitting it reintroduces
	// exactly the parsing that makes the attack possible.
	if strings.Contains(block, "fastcgi_split_path_info") {
		t.Fatalf("path info splitting is enabled:\n%s", block)
	}
}

// A site with PHP off must not serve .php as text: a site that had PHP and was
// switched off still has its config.php, with the database password in it.
func TestStaticSiteDeniesPHPSourceRatherThanServingIt(t *testing.T) {
	rendered, err := nginx.Render(staticSite())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if strings.Contains(rendered, "fastcgi_pass") {
		t.Fatalf("a static site must not pass to FPM:\n%s", rendered)
	}

	block := phpLocation(t, rendered)
	if !strings.Contains(block, "deny all") {
		t.Fatalf("a static site serves .php as source:\n%s", block)
	}
}

func TestStaticSiteOmitsPHPFromTheIndex(t *testing.T) {
	rendered, err := nginx.Render(staticSite())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, line := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "index ") &&
			strings.Contains(line, "index.php") {
			t.Fatalf("a static site lists index.php: %q", line)
		}
	}
}

func TestPHPSiteServesIndexPHP(t *testing.T) {
	rendered, err := nginx.Render(phpSite())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(rendered, "index index.php") {
		t.Fatalf("a PHP site does not serve index.php:\n%s", rendered)
	}
	if !strings.Contains(rendered, "unix:/run/php-fpm/web_example-83.sock") {
		t.Fatalf("the vhost does not point at the pool socket:\n%s", rendered)
	}
}

// ------------------------------------------------------------------ injection

// A config is executed by a process running as root, so a value that could
// close a directive must never reach the template.
func TestRenderRejectsInjectionInTheSocketPath(t *testing.T) {
	for _, socket := range []string{
		"/run/php-fpm/x.sock;\n    root /etc;",
		"/run/php-fpm/x.sock}\nserver{",
		"/run/php-fpm/../../etc/passwd",
		"relative.sock",
	} {
		site := phpSite()
		site.PHPSocket = socket
		if _, err := nginx.Render(site); err == nil {
			t.Fatalf("socket %q must be rejected", socket)
		}
	}
}

func TestRenderRejectsInjectionInDomains(t *testing.T) {
	for _, domain := range []string{
		"example.test;\n    root /etc;",
		"example.test }\nserver {",
		"localhost",
		"",
	} {
		site := phpSite()
		site.PrimaryDomain = domain
		if _, err := nginx.Render(site); err == nil {
			t.Fatalf("domain %q must be rejected", domain)
		}
	}
}

// ------------------------------------------------------------ static behaviour

func TestEverySiteDeniesDotfiles(t *testing.T) {
	for name, site := range map[string]nginx.SiteConfig{
		"static": staticSite(),
		"php":    phpSite(),
	} {
		rendered, err := nginx.Render(site)
		if err != nil {
			t.Fatalf("%s: render: %v", name, err)
		}
		if !strings.Contains(rendered, `location ~ /\.`) {
			t.Fatalf("%s site does not deny dotfiles:\n%s", name, rendered)
		}
	}
}

func TestAliasesBecomeServerNames(t *testing.T) {
	site := phpSite()
	site.Aliases = []string{"www.example.test", "other.test"}

	rendered, err := nginx.Render(site)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(rendered, "server_name example.test www.example.test other.test;") {
		t.Fatalf("aliases are not in server_name:\n%s", rendered)
	}
}

func TestRedirectRefusesToPointAtItself(t *testing.T) {
	// nginx accepts this and serves an infinite redirect loop.
	_, err := nginx.RenderRedirect(nginx.Redirect{
		Domain: "example.test",
		Target: "example.test",
	})
	if err == nil {
		t.Fatal("a self-redirect must be refused")
	}
}

// phpLocation extracts the `location ~ \.php$` block from a rendered config.
func phpLocation(t *testing.T, rendered string) string {
	t.Helper()

	const marker = `location ~ \.php$ {`
	start := strings.Index(rendered, marker)
	if start < 0 {
		t.Fatalf("no .php location block was rendered:\n%s", rendered)
	}

	// The block has no nested braces, so the first closing brace ends it.
	end := strings.Index(rendered[start:], "\n    }")
	if end < 0 {
		t.Fatalf("the .php location block is not closed:\n%s", rendered[start:])
	}
	return rendered[start : start+end]
}

// -------------------------------------------------------------------- HTTPS

func sslSite() nginx.SiteConfig {
	site := staticSite()
	site.SSL = &nginx.SSLConfig{
		CertificatePath: "/etc/jothost/ssl/example.test/fullchain.pem",
		PrivateKeyPath:  "/etc/jothost/ssl/example.test/privkey.pem",
		RedirectToHTTPS: true,
		ChallengeRoot:   "/var/www/.acme-challenge",
	}
	return site
}

// This is the most important test in the SSL work.
//
// The ACME challenge is fetched over plain HTTP by the certificate authority,
// which does not follow a redirect to a certificate it has not issued yet. If
// the redirect catches the challenge path, issuance fails now and renewal fails
// silently sixty days later — when the site goes down on expiry.
func TestChallengePathIsNotRedirected(t *testing.T) {
	rendered, err := nginx.Render(sslSite())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	challenge := strings.Index(rendered, "location ^~ /.well-known/acme-challenge/")
	if challenge < 0 {
		t.Fatalf("no ACME challenge block was rendered:\n%s", rendered)
	}

	redirect := strings.Index(rendered, "return 301 https://")
	if redirect < 0 {
		t.Fatalf("no HTTPS redirect was rendered:\n%s", rendered)
	}

	// nginx matches the most specific prefix first, but ordering also has to
	// read correctly to anyone auditing the file.
	if challenge > redirect {
		t.Fatalf("the challenge block comes after the redirect:\n%s", rendered)
	}
}

// The challenge lives under /.well-known/, which matches `location ~ /\.` —
// the dotfile deny rule. Only the `^~` prefix form takes precedence over a
// regex location and stops the deny rule from being consulted at all.
func TestChallengePathUsesAPrefixMatchToBeatTheDotfileRule(t *testing.T) {
	rendered, err := nginx.Render(sslSite())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(rendered, "location ^~ /.well-known/acme-challenge/") {
		t.Fatalf("the challenge block is not a `^~` prefix location:\n%s", rendered)
	}
	// The dotfile rule must still be there for everything else.
	if !strings.Contains(rendered, `location ~ /\.`) {
		t.Fatalf("the dotfile deny rule is missing:\n%s", rendered)
	}
}

func TestHTTPSBlockUsesModernTLSOnly(t *testing.T) {
	rendered, err := nginx.Render(sslSite())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(rendered, "ssl_protocols TLSv1.2 TLSv1.3;") {
		t.Fatalf("TLS protocols are not restricted to 1.2 and 1.3:\n%s", rendered)
	}
	// Deprecated and prohibited for anything handling card data.
	for _, deprecated := range []string{"TLSv1.0", "TLSv1.1", "SSLv3"} {
		if strings.Contains(rendered, deprecated) {
			t.Fatalf("%s is offered:\n%s", deprecated, rendered)
		}
	}
	// Forward secrecy and AEAD only.
	if strings.Contains(rendered, "ssl_ciphers") && !strings.Contains(rendered, "ECDHE") {
		t.Fatalf("the cipher list has no forward-secret suites:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Strict-Transport-Security") {
		t.Fatalf("HSTS is not set:\n%s", rendered)
	}
	if !strings.Contains(rendered, "ssl_session_tickets off") {
		t.Fatalf("session tickets are on, which undermines forward secrecy:\n%s", rendered)
	}
}

func TestHTTPSBlockNamesTheCertificate(t *testing.T) {
	rendered, err := nginx.Render(sslSite())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(rendered, "ssl_certificate /etc/jothost/ssl/example.test/fullchain.pem;") {
		t.Fatalf("the certificate path is missing:\n%s", rendered)
	}
	if !strings.Contains(rendered, "ssl_certificate_key /etc/jothost/ssl/example.test/privkey.pem;") {
		t.Fatalf("the private key path is missing:\n%s", rendered)
	}
	if !strings.Contains(rendered, "listen 443 ssl;") {
		t.Fatalf("the site does not listen on 443:\n%s", rendered)
	}
}

// A vhost naming a certificate that is not there stops nginx from starting at
// all, which takes down every other site on the host.
func TestSiteWithoutSSLHasNoHTTPSBlock(t *testing.T) {
	rendered, err := nginx.Render(staticSite())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	for _, directive := range []string{"listen 443", "ssl_certificate", "ssl_protocols"} {
		if strings.Contains(rendered, directive) {
			t.Fatalf("a site with no certificate emitted %q:\n%s", directive, rendered)
		}
	}
}

// Without the redirect the site must still serve its content over HTTP rather
// than only offering HTTPS.
func TestHTTPSWithoutRedirectStillServesOverHTTP(t *testing.T) {
	site := sslSite()
	site.SSL.RedirectToHTTPS = false

	rendered, err := nginx.Render(site)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(rendered, "return 301 https://") {
		t.Fatalf("a redirect was rendered when none was asked for:\n%s", rendered)
	}
	if !strings.Contains(rendered, "listen 443 ssl;") {
		t.Fatalf("the HTTPS block is missing:\n%s", rendered)
	}
}

// An ssl_certificate line is read by a root process and names the file that is
// the site's identity.
func TestRenderRejectsInjectionInCertificatePaths(t *testing.T) {
	for _, path := range []string{
		"/etc/ssl/cert.pem;\n    root /etc;",
		"/etc/ssl/cert.pem}\nserver{",
		"/etc/ssl/../../etc/shadow",
		"relative.pem",
		"",
	} {
		site := sslSite()
		site.SSL.CertificatePath = path
		if _, err := nginx.Render(site); err == nil {
			t.Fatalf("certificate path %q must be rejected", path)
		}

		site = sslSite()
		site.SSL.PrivateKeyPath = path
		if _, err := nginx.Render(site); err == nil {
			t.Fatalf("private key path %q must be rejected", path)
		}
	}
}

// A PHP application behind HTTPS has to know it is behind HTTPS, or it builds
// http:// URLs and sets cookies without the secure flag.
func TestHTTPSPHPBlockAnnouncesHTTPS(t *testing.T) {
	site := sslSite()
	site.PHPSocket = "/run/php-fpm/web_example-83.sock"

	rendered, err := nginx.Render(site)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(rendered, "fastcgi_param HTTPS on;") {
		t.Fatalf("the HTTPS FastCGI parameter is missing:\n%s", rendered)
	}
	// The execution guard must be present in the HTTPS block too, not only the
	// HTTP one.
	if strings.Count(rendered, "try_files $uri =404;") < 1 {
		t.Fatalf("the PHP execution guard is missing from the HTTPS block:\n%s", rendered)
	}
}
