package apache

import (
	"strings"
	"testing"
)

func validSite() SiteConfig {
	return SiteConfig{
		PrimaryDomain: "example.test",
		Aliases:       []string{"www.example.test"},
		DocumentRoot:  "/var/www/example.test/public",
		BackendPort:   7080,
		AccessLog:     "/var/www/example.test/logs/apache-access.log",
		ErrorLog:      "/var/www/example.test/logs/apache-error.log",
		PHPSocket:     "/run/php-fpm/web_example-83.sock",
		AllowOverride: true,
	}
}

func TestRenderProducesAVhostOnTheLoopbackOnly(t *testing.T) {
	out, err := Render(validSite())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// The whole safety of the arrangement: nginx holds the public port, and
	// nothing outside the host can reach Apache directly.
	if !strings.Contains(out, "Listen 127.0.0.1:7080") {
		t.Errorf("the vhost must listen on the loopback only:\n%s", out)
	}
	if !strings.Contains(out, "<VirtualHost 127.0.0.1:7080>") {
		t.Errorf("the vhost is not bound to the loopback:\n%s", out)
	}
	if strings.Contains(out, "Listen 7080\n") || strings.Contains(out, "*:7080") {
		t.Errorf("the vhost listens on every address:\n%s", out)
	}

	if !strings.Contains(out, "ServerName example.test") {
		t.Error("the vhost has no ServerName, so nginx cannot select it")
	}
	// An alias missing here lands on whichever vhost is first on the port.
	if !strings.Contains(out, "ServerAlias www.example.test") {
		t.Error("aliases must be listed, or Apache serves them from the wrong site")
	}
}

// mod_php would run every site's code inside Apache as one shared account,
// which is the arrangement per-site FPM pools exist to avoid.
func TestRenderRunsPHPThroughFPMAndNotInProcess(t *testing.T) {
	out, err := Render(validSite())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(out, `SetHandler "proxy:unix:/run/php-fpm/web_example-83.sock|fcgi://localhost"`) {
		t.Errorf("PHP is not proxied to the site's own pool:\n%s", out)
	}
	if strings.Contains(out, "php_admin_value") || strings.Contains(out, "mod_php") {
		t.Errorf("the vhost uses in-process PHP:\n%s", out)
	}
}

// The path-info hole in its Apache form: without the file check, a request for
// /uploads/avatar.jpg/x.php reaches FPM, which walks back to the real file and
// executes an uploaded image.
func TestRenderOnlyHandsExistingFilesToPHP(t *testing.T) {
	out, err := Render(validSite())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(out, "-f %{REQUEST_FILENAME}") {
		t.Errorf("PHP is handed requests for files that do not exist:\n%s", out)
	}
	handler := strings.Index(out, "SetHandler")
	guard := strings.Index(out, "-f %{REQUEST_FILENAME}")
	if guard == -1 || handler == -1 || guard > handler {
		t.Error("the file check must run before the handler is set")
	}
}

func TestRenderDeniesPHPWhenTheSiteHasNone(t *testing.T) {
	cfg := validSite()
	cfg.PHPSocket = ""

	out, err := Render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if strings.Contains(out, "SetHandler") {
		t.Errorf("a static site must not name a PHP handler:\n%s", out)
	}
	// A site with PHP switched off still has its config.php.
	if !strings.Contains(out, `<FilesMatch "\.php$">`) ||
		!strings.Contains(out, "Require all denied") {
		t.Errorf("a static site must refuse .php rather than serve the source:\n%s", out)
	}
}

func TestRenderProtectsDotfilesAndTheFilesystem(t *testing.T) {
	out, err := Render(validSite())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// The file that protects a directory must not be served by the server that
	// reads it.
	if !strings.Contains(out, `<FilesMatch "^\.">`) {
		t.Errorf("dotfiles are servable:\n%s", out)
	}
	// Apache's default is to allow the whole filesystem unless told otherwise.
	if !strings.Contains(out, "<Directory />") {
		t.Errorf("the filesystem root is not denied:\n%s", out)
	}
}

func TestRenderControlsHtaccessPerSite(t *testing.T) {
	cfg := validSite()
	out, err := Render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "AllowOverride All") {
		t.Errorf(".htaccess is not enabled when it was asked for:\n%s", out)
	}

	cfg.AllowOverride = false
	out, err = Render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "AllowOverride None") {
		t.Errorf(".htaccess is enabled when it was not asked for:\n%s", out)
	}
}

// This is the last point before text becomes configuration a root process
// executes.
func TestRenderRefusesValuesThatWouldEscapeADirective(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*SiteConfig)
	}{
		{"a domain with a space", func(c *SiteConfig) { c.PrimaryDomain = "exa mple.test" }},
		{"a domain with a newline", func(c *SiteConfig) { c.PrimaryDomain = "a.test\nListen 80" }},
		{"an alias with a newline", func(c *SiteConfig) { c.Aliases = []string{"a.test\n</VirtualHost>"} }},
		{"a relative document root", func(c *SiteConfig) { c.DocumentRoot = "var/www" }},
		{"a traversing document root", func(c *SiteConfig) { c.DocumentRoot = "/var/www/../etc" }},
		{"a quoted path", func(c *SiteConfig) { c.DocumentRoot = `/var/www/a" + "b` }},
		{"a path with a newline", func(c *SiteConfig) { c.ErrorLog = "/var/log/x\nListen 80" }},
		{"a socket with a newline", func(c *SiteConfig) { c.PHPSocket = "/run/x\n" }},
		{"a privileged port", func(c *SiteConfig) { c.BackendPort = 80 }},
		{"an ephemeral port", func(c *SiteConfig) { c.BackendPort = 40000 }},
		{"no port at all", func(c *SiteConfig) { c.BackendPort = 0 }},
		{"a negative body limit", func(c *SiteConfig) { c.MaxBodySize = -1 }},
	}

	for _, tc := range cases {
		cfg := validSite()
		tc.mutate(&cfg)
		if _, err := Render(cfg); err == nil {
			t.Errorf("%s was accepted", tc.name)
		}
	}
}

// A wildcard site is served by an alias, because Apache will not have one as a
// ServerName.
//
// This test previously asserted the opposite — "ServerName *.example.test" —
// and passed, which is how the defect survived: the assertion and the template
// agreed with each other and neither had ever been shown to Apache, which
// answers
//
//	Invalid ServerName "*.example.test" use ServerAlias to set multiple server
//	names.
//
// and refuses to start. The suite that catches it now is the hybrid-mode half
// of tests/integration/phase41_subdomains.sh, which puts the file in front of
// the real httpd.
func TestRenderServesAWildcardThroughAnAliasNotAServerName(t *testing.T) {
	cfg := validSite()
	cfg.PrimaryDomain = "*.example.test"

	out, err := Render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if strings.Contains(out, "ServerName *.") {
		t.Errorf("Apache refuses a wildcard ServerName and will not start:\n%s", out)
	}
	if !strings.Contains(out, "ServerName example.test\n") {
		t.Errorf("the wildcard's base name should be the ServerName:\n%s", out)
	}
	// The matching behaviour has to be unchanged: the alias is what catches
	// anything.example.test.
	if !strings.Contains(out, "ServerAlias *.example.test") {
		t.Errorf("the wildcard is not served at all:\n%s", out)
	}
	// And the site's other names are still there.
	if !strings.Contains(out, "ServerAlias www.example.test") {
		t.Errorf("an alias was dropped when the names were split:\n%s", out)
	}
	// ServerName is no longer the name the visitor asked for, so a redirect
	// must come from the request instead — or every wildcard site would bounce
	// its visitors to the parent domain.
	if !strings.Contains(out, "UseCanonicalName Off") {
		t.Errorf("self-referential URLs would come from ServerName:\n%s", out)
	}
}

// An ordinary site is untouched by the split: its canonical name is still its
// ServerName, and it gains no alias it did not have.
func TestRenderLeavesAnOrdinaryNameAlone(t *testing.T) {
	out, err := Render(validSite())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "ServerName example.test\n") {
		t.Errorf("the canonical name is not the ServerName:\n%s", out)
	}
	if strings.Count(out, "ServerAlias ") != 1 {
		t.Errorf("expected exactly the one configured alias:\n%s", out)
	}
}

// The split is a function, and these are the cases worth pinning about it.
func TestServerNames(t *testing.T) {
	cases := []struct {
		name        string
		primary     string
		aliases     []string
		wantName    string
		wantAliases []string
	}{
		{
			name:        "an ordinary site is unchanged",
			primary:     "example.test",
			aliases:     []string{"www.example.test"},
			wantName:    "example.test",
			wantAliases: []string{"www.example.test"},
		},
		{
			name:        "a wildcard becomes its base plus an alias",
			primary:     "*.example.test",
			aliases:     []string{"www.example.test"},
			wantName:    "example.test",
			wantAliases: []string{"*.example.test", "www.example.test"},
		},
		{
			// The base is the ServerName, so listing it again as an alias
			// would be Apache being told the same thing twice.
			name:        "the base is not repeated as an alias",
			primary:     "*.example.test",
			aliases:     []string{"example.test", "www.example.test"},
			wantName:    "example.test",
			wantAliases: []string{"*.example.test", "www.example.test"},
		},
		{
			name:        "a wildcard with no other names",
			primary:     "*.example.test",
			aliases:     nil,
			wantName:    "example.test",
			wantAliases: []string{"*.example.test"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotName, gotAliases := serverNames(tc.primary, tc.aliases)
			if gotName != tc.wantName {
				t.Errorf("ServerName = %q, want %q", gotName, tc.wantName)
			}
			if len(gotAliases) != len(tc.wantAliases) {
				t.Fatalf("aliases = %v, want %v", gotAliases, tc.wantAliases)
			}
			for i, want := range tc.wantAliases {
				if gotAliases[i] != want {
					t.Errorf("alias %d = %q, want %q", i, gotAliases[i], want)
				}
			}
		})
	}
}

func TestRenderBaseCarriesTheGroupAndTheRealClientAddress(t *testing.T) {
	out, err := RenderBase(BaseConfig{WebGroup: "nginx"})
	if err != nil {
		t.Fatalf("render base: %v", err)
	}

	// Without the group, every request is a 403 on a site that looks right.
	if !strings.Contains(out, "Group nginx") {
		t.Errorf("the web group is missing:\n%s", out)
	}
	// Without mod_remoteip, every log line and every IP rule sees the proxy.
	if !strings.Contains(out, "LoadModule remoteip_module") ||
		!strings.Contains(out, "RemoteIPHeader X-Forwarded-For") {
		t.Errorf("the real client address is not restored:\n%s", out)
	}
	// And believing that header from anywhere would let a client claim any
	// address it liked.
	if !strings.Contains(out, "RemoteIPInternalProxy 127.0.0.1") {
		t.Errorf("the trusted proxy list is missing:\n%s", out)
	}
	// mod_proxy is loaded for FastCGI; left open it relays other people's
	// traffic.
	if !strings.Contains(out, "ProxyRequests Off") {
		t.Errorf("Apache is willing to act as a forward proxy:\n%s", out)
	}
}

func TestRenderBaseRefusesAnUnusableGroupOrProxy(t *testing.T) {
	if _, err := RenderBase(BaseConfig{WebGroup: ""}); err == nil {
		t.Error("an empty group was accepted")
	}
	if _, err := RenderBase(BaseConfig{WebGroup: "ngin x"}); err == nil {
		t.Error("a group with a space was accepted")
	}
	if _, err := RenderBase(BaseConfig{
		WebGroup:       "nginx",
		TrustedProxies: []string{"127.0.0.1\nListen 80"},
	}); err == nil {
		t.Error("a proxy address with a newline was accepted")
	}
}

// Neither edit can be made from an included file: a second Listen 80 is still
// a Listen 80, and a second LoadModule for an MPM is a fatal error.
func TestMainConfigEditsTakePortEightyAndSwapTheMPM(t *testing.T) {
	original := strings.Join([]string{
		"ServerRoot /var/www",
		"Listen 80",
		"#LoadModule mpm_event_module modules/mod_mpm_event.so",
		"LoadModule mpm_prefork_module modules/mod_mpm_prefork.so",
		"User apache",
	}, "\n")

	updated, changed := applyMainConfigEdits(original)
	if !changed {
		t.Fatal("the file was left as it was")
	}

	if strings.Contains(updated, "\nListen 80") {
		t.Errorf("Apache still holds port 80:\n%s", updated)
	}
	if !strings.Contains(updated, "#Listen 80") {
		t.Errorf("the original line was removed rather than commented:\n%s", updated)
	}
	if !strings.Contains(updated, "LoadModule mpm_event_module modules/mod_mpm_event.so") {
		t.Errorf("the event MPM is not loaded:\n%s", updated)
	}
	if !strings.Contains(updated, "#LoadModule mpm_prefork_module") {
		t.Errorf("two MPMs are loaded, which Apache refuses to start with:\n%s", updated)
	}
	// An operator reading httpd.conf should see who changed it and why.
	if !strings.Contains(updated, editMarker) {
		t.Errorf("the edits are unmarked:\n%s", updated)
	}
	// Nothing else is touched.
	if !strings.Contains(updated, "ServerRoot /var/www") ||
		!strings.Contains(updated, "User apache") {
		t.Errorf("unrelated lines were changed:\n%s", updated)
	}
}

func TestMainConfigEditsAreIdempotent(t *testing.T) {
	original := "Listen 80\nLoadModule mpm_prefork_module modules/mod_mpm_prefork.so\n"

	once, _ := applyMainConfigEdits(original)
	twice, changed := applyMainConfigEdits(once)
	if changed {
		t.Fatalf("a prepared file was changed again:\n%s", twice)
	}
	if once != twice {
		t.Fatalf("the second pass differs:\n%s", twice)
	}
}

// The panel's own site files carry a loopback Listen. Commenting those out
// would leave Apache with nothing to listen on at all.
func TestMainConfigEditsLeaveLoopbackListensAlone(t *testing.T) {
	updated, changed := applyMainConfigEdits("Listen 127.0.0.1:7080\n")
	if changed || strings.Contains(updated, "#Listen") {
		t.Fatalf("a loopback Listen was commented out:\n%s", updated)
	}
}
