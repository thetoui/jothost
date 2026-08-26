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
