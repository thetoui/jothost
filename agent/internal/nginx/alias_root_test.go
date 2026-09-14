package nginx

import (
	"strings"
	"testing"
)

// A name served from a directory of its own gets a server block of its own,
// because nginx has one root per server block.
//
// Those blocks are produced by running the site template again rather than by
// a second template, and that is the property most worth pinning: the PHP
// location block is the one place in this package where a divergence turns an
// uploaded image into executable code, so every block a site produces must
// carry the identical guards.

func siteWithAlias() SiteConfig {
	return SiteConfig{
		PrimaryDomain: "example.test",
		Aliases:       []string{"www.example.test"},
		AliasRoots: []AliasRoot{
			{Domain: "shop.example.test", DocumentRoot: "/var/www/example.test/shop"},
		},
		DocumentRoot: "/var/www/example.test/public",
		AccessLog:    "/var/www/example.test/logs/access.log",
		ErrorLog:     "/var/www/example.test/logs/error.log",
		PHPSocket:    "/run/php/example.sock",
	}
}

func TestAnAliasWithItsOwnRootGetsItsOwnServerBlock(t *testing.T) {
	out, err := Render(siteWithAlias())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if blocks := strings.Count(out, "\nserver {"); blocks != 2 {
		t.Fatalf("got %d server blocks, want 2:\n%s", blocks, out)
	}
	if !strings.Contains(out, "server_name shop.example.test;") {
		t.Fatalf("the alias has no server block of its own:\n%s", out)
	}
	if !strings.Contains(out, "root /var/www/example.test/shop;") {
		t.Fatalf("the alias is not served from its own directory:\n%s", out)
	}
	// And the site's own block still serves the site.
	if !strings.Contains(out, "server_name example.test www.example.test;") {
		t.Fatalf("the site's own names changed:\n%s", out)
	}
}

func TestTheAliasBlockCarriesTheSamePHPGuards(t *testing.T) {
	// The reason the alias block is rendered from the site template rather
	// than written out a second time. Without `try_files $uri =404` in front
	// of fastcgi_pass, nginx hands /uploads/avatar.jpg/x.php to FPM, FPM walks
	// back to the real file, and an uploaded image runs as code. A second copy
	// of this block is a second place for that line to go missing.
	out, err := Render(siteWithAlias())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	for _, guard := range []string{
		"try_files $uri =404;",
		"fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;",
	} {
		if got := strings.Count(out, guard); got != 2 {
			t.Fatalf("%q appears %d times, want once per server block:\n%s", guard, got, out)
		}
	}
	// The parsing that makes the attack possible stays off in every block.
	if strings.Contains(out, "fastcgi_split_path_info") {
		t.Fatalf("path info parsing was enabled:\n%s", out)
	}
}

func TestTheAliasBlockDoesNotAlsoClaimTheSitesNames(t *testing.T) {
	// Two server blocks answering for one name is a configuration whose
	// behaviour depends on which nginx read first. The site would work, and
	// would be served from whichever root won, which is not a thing anybody
	// could debug from the panel.
	out, err := Render(siteWithAlias())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Count(out, "example.test www.example.test") != 1 {
		t.Fatalf("the site's names appear in more than one block:\n%s", out)
	}
	for _, block := range strings.Split(out, "\nserver {")[1:] {
		names := block[strings.Index(block, "server_name"):]
		names = names[:strings.Index(names, ";")]
		if strings.Contains(names, "shop.example.test") && strings.Contains(names, "example.test w") {
			t.Fatalf("the alias block also answers for the site:\n%s", block)
		}
	}
}

func TestAnAliasNamedAsTheSiteIsRefused(t *testing.T) {
	cfg := siteWithAlias()
	cfg.AliasRoots = []AliasRoot{
		{Domain: "example.test", DocumentRoot: "/var/www/example.test/other"},
	}
	if _, err := Render(cfg); err == nil {
		t.Fatal("an alias root on the site's own name was accepted")
	}
}

func TestAnAliasRootThatCouldCloseTheDirectiveIsRefused(t *testing.T) {
	// This string becomes an nginx root directive read by a process running as
	// root. A value carrying a semicolon or a newline would end the directive
	// and let whatever follows be configuration.
	for _, root := range []string{
		"/var/www/x; root /etc",
		"/var/www/x\nroot /etc;",
		"/var/www/x\"",
	} {
		cfg := siteWithAlias()
		cfg.AliasRoots = []AliasRoot{{Domain: "shop.example.test", DocumentRoot: root}}
		if _, err := Render(cfg); err == nil {
			t.Fatalf("a document root of %q was accepted", root)
		}
	}
}

func TestAnAliasNameThatCouldCloseTheDirectiveIsRefused(t *testing.T) {
	for _, name := range []string{
		"shop.example.test; root /etc",
		"shop.example.test\nroot /etc;",
		"*",
	} {
		cfg := siteWithAlias()
		cfg.AliasRoots = []AliasRoot{
			{Domain: name, DocumentRoot: "/var/www/example.test/shop"},
		}
		if _, err := Render(cfg); err == nil {
			t.Fatalf("an alias named %q was accepted", name)
		}
	}
}

func TestASiteWithNoAliasRootsIsUnchanged(t *testing.T) {
	// The whole feature must be invisible to a site that does not use it: this
	// renders every existing vhost on every host that upgrades.
	cfg := siteWithAlias()
	cfg.AliasRoots = nil

	out, err := Render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if blocks := strings.Count(out, "\nserver {"); blocks != 1 {
		t.Fatalf("got %d server blocks, want 1:\n%s", blocks, out)
	}
}
