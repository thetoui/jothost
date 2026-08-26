package php_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jothost/panel/agent/internal/php"
)

// writeBinary creates a fake FPM binary so Probe can find it.
func writeBinary(t *testing.T, root, path string) {
	t.Helper()

	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

func TestProbeFindsAlpineLayout(t *testing.T) {
	root := t.TempDir()
	writeBinary(t, root, "/usr/sbin/php-fpm83")

	found := php.Probe(root)
	if len(found) != 1 {
		t.Fatalf("probed %d versions, want 1", len(found))
	}

	version := found[0]
	if version.Version != "8.3" {
		t.Fatalf("version = %q, want 8.3", version.Version)
	}
	if version.PoolDir != "/etc/php83/php-fpm.d" {
		t.Fatalf("pool dir = %q, want the Alpine layout", version.PoolDir)
	}
	if version.FPMService != "php-fpm83" {
		t.Fatalf("service = %q", version.FPMService)
	}
}

func TestProbeFindsDebianLayout(t *testing.T) {
	root := t.TempDir()
	writeBinary(t, root, "/usr/sbin/php-fpm8.2")

	found := php.Probe(root)
	if len(found) != 1 {
		t.Fatalf("probed %d versions, want 1", len(found))
	}
	if found[0].PoolDir != "/etc/php/8.2/fpm/pool.d" {
		t.Fatalf("pool dir = %q, want the Debian layout", found[0].PoolDir)
	}
}

// Ordering is numeric, not lexical: a text sort puts "8.9" above "8.10", which
// becomes visibly wrong the first time a PHP 8.10 exists.
func TestProbeOrdersNewestFirst(t *testing.T) {
	root := t.TempDir()
	writeBinary(t, root, "/usr/sbin/php-fpm82")
	writeBinary(t, root, "/usr/sbin/php-fpm84")
	writeBinary(t, root, "/usr/sbin/php-fpm83")

	found := php.Probe(root)
	if len(found) != 3 {
		t.Fatalf("probed %d versions, want 3", len(found))
	}

	want := []string{"8.4", "8.3", "8.2"}
	for i, version := range found {
		if version.Version != want[i] {
			t.Fatalf("position %d = %q, want %q", i, version.Version, want[i])
		}
	}
}

func TestProbeIgnoresADirectoryNamedLikeABinary(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "/usr/sbin/php-fpm83"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if found := php.Probe(root); len(found) != 0 {
		t.Fatalf("probed %d versions, want 0", len(found))
	}
}

func TestCommandSpecsCoverEveryProbedVersion(t *testing.T) {
	root := t.TempDir()
	writeBinary(t, root, "/usr/sbin/php-fpm83")
	writeBinary(t, root, "/usr/sbin/php-fpm84")

	specs := php.CommandSpecs(root)
	if len(specs) != 2 {
		t.Fatalf("built %d specs, want 2", len(specs))
	}

	// Every spec must carry an absolute path: the Runner never consults PATH,
	// and a relative entry would be refused at startup.
	for _, spec := range specs {
		if !filepath.IsAbs(spec.Path) {
			t.Fatalf("spec %q has a relative path %q", spec.Name, spec.Path)
		}
		if !strings.HasPrefix(spec.Name, php.CommandPrefix) {
			t.Fatalf("spec name %q is not namespaced", spec.Name)
		}
	}
}

func TestSocketPathIncludesTheVersion(t *testing.T) {
	// Two versions of one site must not share a socket path, or the second
	// FPM refuses to start and the switch fails.
	first := php.SocketPathFor("web_example", "8.3")
	second := php.SocketPathFor("web_example", "8.4")

	if first == second {
		t.Fatalf("both versions produced the socket %q", first)
	}
	for _, path := range []string{first, second} {
		if !strings.HasPrefix(path, php.SocketDir) {
			t.Fatalf("socket %q is outside %s", path, php.SocketDir)
		}
	}
}

// ------------------------------------------------------------- pool rendering

// validPool returns a pool that renders, for tests that vary one field.
func validPool() php.Pool {
	return php.Pool{
		Name:         "web_example",
		Version:      "8.3",
		User:         "web_example",
		Group:        "web_example",
		SocketPath:   "/run/php-fpm/web_example-83.sock",
		ListenGroup:  "nginx",
		DocumentRoot: "/var/www/example.test/public",
		Settings:     php.DefaultSettings(),
	}
}

func TestRenderPoolProducesTheSecurityDirectives(t *testing.T) {
	rendered, err := php.RenderPool(validPool())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Each of these is load-bearing, and losing one silently weakens every
	// site on the host.
	required := map[string]string{
		"user = web_example":     "workers must run as the site's own account",
		"listen.group = nginx":   "the web server must be able to open the socket",
		"listen.mode = 0660":     "the socket must not be world-writable",
		"open_basedir":           "PHP must not read outside the site",
		"disable_functions":      "shell access must be off",
		"display_errors] = off":  "errors must not reach the response",
		"session.save_path":      "sessions must be per-site",
		"php_admin_value[memory": "limits must be admin values so ini_set cannot raise them",
	}
	for needle, why := range required {
		if !strings.Contains(rendered, needle) {
			t.Fatalf("rendered pool is missing %q (%s)", needle, why)
		}
	}
}

// php_admin_value cannot be overridden by ini_set() from inside the
// application; php_value can. Using the wrong one lets a script raise its own
// memory ceiling or escape open_basedir.
func TestRenderPoolUsesAdminValuesForLimits(t *testing.T) {
	rendered, err := php.RenderPool(validPool())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	for _, setting := range []string{"open_basedir", "memory_limit", "disable_functions"} {
		if strings.Contains(rendered, "php_value["+setting+"]") {
			t.Fatalf("%s is a php_value, so an application could override it", setting)
		}
	}
}

func TestRenderPoolConfinesOpenBasedirToTheSite(t *testing.T) {
	rendered, err := php.RenderPool(validPool())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	for _, line := range strings.Split(rendered, "\n") {
		if !strings.Contains(line, "open_basedir") {
			continue
		}
		if !strings.Contains(line, "/var/www/example.test") {
			t.Fatalf("open_basedir does not name the site: %q", line)
		}
		// A basedir of "/" or "/var/www" would let one site read every other.
		for _, forbidden := range []string{"= /:", "= /var/www:", "= /var/www\n"} {
			if strings.Contains(line, forbidden) {
				t.Fatalf("open_basedir is too broad: %q", line)
			}
		}
		return
	}
	t.Fatal("no open_basedir directive was rendered")
}

// A pool file is line-oriented and section-based. A value carrying a newline
// would close its directive and start another; a bracket could open a whole
// new pool section defining workers that run as any user it names.
func TestRenderPoolRejectsInjectionInPaths(t *testing.T) {
	for _, root := range []string{
		"/var/www/x/public\nuser = root",
		"/var/www/x/public\n[evil]",
		"/var/www/x/public[evil]",
		"/var/www/../../etc",
		"relative/path",
		"/var/www/x/public'",
		"/var/www/x/public$(id)",
		"",
	} {
		pool := validPool()
		pool.DocumentRoot = root
		if _, err := php.RenderPool(pool); err == nil {
			t.Fatalf("document root %q must be rejected", root)
		}
	}
}

func TestRenderPoolRejectsInjectionInSettings(t *testing.T) {
	for _, limit := range []string{
		"256M\nuser = root",
		"256M; evil",
		"256M[x]",
		"not-a-size",
		"999999G",
	} {
		pool := validPool()
		pool.Settings.MemoryLimit = limit
		if _, err := php.RenderPool(pool); err == nil {
			t.Fatalf("memory_limit %q must be rejected", limit)
		}
	}
}

// Without a web server group the socket is unreachable, so the pool starts and
// every request to the site returns 502 with nothing obviously wrong. Refusing
// to render is far easier to diagnose.
func TestRenderPoolRefusesWithoutAWebGroup(t *testing.T) {
	pool := validPool()
	pool.ListenGroup = ""

	if _, err := php.RenderPool(pool); err == nil {
		t.Fatal("a pool with no listen group must be refused")
	}
}

// "nginx" is a reserved account a *website* may never own, but it is exactly
// the group a pool socket must be readable by. Validating it as a system user
// rejects the only correct value.
func TestRenderPoolAcceptsTheReservedWebGroup(t *testing.T) {
	for _, group := range []string{"nginx", "www-data"} {
		pool := validPool()
		pool.ListenGroup = group
		if _, err := php.RenderPool(pool); err != nil {
			t.Fatalf("listen group %q must be accepted: %v", group, err)
		}
	}
}

func TestRenderPoolRejectsAReservedPoolUser(t *testing.T) {
	// The account the workers run as is a different question: a pool running
	// as root would give every PHP script on the site the whole host.
	pool := validPool()
	pool.User = "root"

	if _, err := php.RenderPool(pool); err == nil {
		t.Fatal("a pool running as root must be refused")
	}
}

func TestRenderPoolBoundsMaxChildren(t *testing.T) {
	pool := validPool()
	pool.MaxChildren = php.MaxChildrenLimit + 1

	if _, err := php.RenderPool(pool); err == nil {
		t.Fatal("an unbounded max_children must be refused")
	}
}

func TestRenderPoolAppliesDefaults(t *testing.T) {
	pool := validPool()
	pool.Settings = php.Settings{}
	pool.MaxChildren = 0

	rendered, err := php.RenderPool(pool)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(rendered, php.DefaultMemoryLimit) {
		t.Fatalf("no default memory limit was applied:\n%s", rendered)
	}
	if !strings.Contains(rendered, "pm.max_children = ") {
		t.Fatal("no max_children was rendered")
	}
}

func TestRenderPoolTogglesOPcache(t *testing.T) {
	pool := validPool()
	pool.Settings.OPcacheEnabled = false

	rendered, err := php.RenderPool(pool)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(rendered, "opcache.enable] = off") {
		t.Fatalf("opcache was not turned off:\n%s", rendered)
	}
}

func TestPackageNamesMatchTheDistribution(t *testing.T) {
	alpine, ok := php.PackageFor("8.3", php.ManagerAPK)
	if !ok || alpine != "php83-fpm" {
		t.Fatalf("apk package = %q, want php83-fpm", alpine)
	}

	debian, ok := php.PackageFor("8.3", php.ManagerAPT)
	if !ok || debian != "php8.3-fpm" {
		t.Fatalf("apt package = %q, want php8.3-fpm", debian)
	}

	// A version that does not validate must never reach a package name: the
	// result is an argument to a program running as root.
	if _, ok := php.PackageFor("8.3; rm -rf /", php.ManagerAPK); ok {
		t.Fatal("an invalid version produced a package name")
	}
}
