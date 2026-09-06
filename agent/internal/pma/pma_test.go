package pma

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jothost/panel/agent/internal/nginx"
)

// These cover the vhost bookkeeping, which is where this package went wrong.
//
// Installing phpMyAdmin under a second name wrote a second server block and
// left the first published: the panel then reported one name and served two,
// and uninstalling removed whichever the filesystem happened to list first,
// leaving a database console reachable on a name the operator believed they had
// removed. It survived because nothing here had a test at all.

// managerOn returns a Manager whose nginx sites directory is a temporary one.
func managerOn(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	return &Manager{
		nginx: nginx.NewProvider(nginx.Options{SitesDir: dir}),
	}, dir
}

// publish writes a site file, with the marker when it is one of ours.
func publish(t *testing.T, dir, name string, ours bool) {
	t.Helper()
	body := "server { server_name " + name + "; }\n"
	if ours {
		body = vhostMarker + "\n" + body
	}
	if err := os.WriteFile(filepath.Join(dir, name+".conf"), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestOnlyTheSitesThisPackageOwnsAreFound(t *testing.T) {
	manager, dir := managerOn(t)

	publish(t, dir, "shop.example", false)
	publish(t, dir, "pma.example", true)
	publish(t, dir, "blog.example", false)

	names, err := manager.servedNames()
	if err != nil {
		t.Fatalf("servedNames: %v", err)
	}
	if len(names) != 1 || names[0] != "pma.example" {
		t.Fatalf("expected only pma.example, got %v", names)
	}
}

func TestEverySiteThisPackageOwnsIsReported(t *testing.T) {
	manager, dir := managerOn(t)

	// The state a host reached in practice, by installing twice under
	// different names.
	publish(t, dir, "pma.example", true)
	publish(t, dir, "phpmyadmin.example", true)

	names, err := manager.servedNames()
	if err != nil {
		t.Fatalf("servedNames: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("expected both sites, got %v", names)
	}

	// Directory order, so the same host answers the same way twice. A caller
	// that wants one name must not get a different one on the next call.
	first, err := manager.servedName()
	if err != nil {
		t.Fatalf("servedName: %v", err)
	}
	again, err := manager.servedName()
	if err != nil {
		t.Fatalf("servedName: %v", err)
	}
	if first != again {
		t.Fatalf("two calls disagreed: %q then %q", first, again)
	}
}

func TestInstallingUnderANewNameRemovesTheOldSite(t *testing.T) {
	manager, dir := managerOn(t)

	publish(t, dir, "pma.example", true)
	publish(t, dir, "shop.example", false)

	if err := manager.removeOtherVhosts(context.Background(), "phpmyadmin.example"); err != nil {
		t.Fatalf("removeOtherVhosts: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "pma.example.conf")); !os.IsNotExist(err) {
		t.Fatal("the previous phpMyAdmin site is still published")
	}
	// Somebody else's website is not this package's to remove, marker or no
	// marker.
	if _, err := os.Stat(filepath.Join(dir, "shop.example.conf")); err != nil {
		t.Fatalf("an unrelated site was removed: %v", err)
	}
}

func TestReinstallingUnderTheSameNameKeepsIt(t *testing.T) {
	manager, dir := managerOn(t)

	publish(t, dir, "pma.example", true)

	// Idempotency: installing twice under one name must not remove the site
	// between the two writes and leave a window with nothing published.
	if err := manager.removeOtherVhosts(context.Background(), "pma.example"); err != nil {
		t.Fatalf("removeOtherVhosts: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pma.example.conf")); err != nil {
		t.Fatalf("the site being reinstalled was removed: %v", err)
	}
}

func TestNoSitesIsNotAnError(t *testing.T) {
	manager, _ := managerOn(t)

	name, err := manager.servedName()
	if err != nil {
		t.Fatalf("servedName on an empty directory: %v", err)
	}
	if name != "" {
		t.Fatalf("expected no name, got %q", name)
	}
	if err := manager.removeOtherVhosts(context.Background(), "pma.example"); err != nil {
		t.Fatalf("removeOtherVhosts on an empty directory: %v", err)
	}
}

func TestTheConfigNamesThePathThePanelProxies(t *testing.T) {
	config := renderConfig("secret")

	// The mount appears here, in the panel's nginx configuration and in the
	// frontend. phpMyAdmin builds its redirects from this one, and a
	// disagreement sends the operator to the panel's own router mid-login.
	if !strings.Contains(config, "$cfg['PmaAbsoluteUri'] = '"+BaseURI+"';") {
		t.Fatalf("the configuration does not carry %s:\n%s", BaseURI, config)
	}
	// The refusals that make reaching the page harmless.
	for _, required := range []string{
		"$cfg['Servers'][$i]['auth_type'] = 'cookie';",
		"$cfg['Servers'][$i]['AllowNoPassword'] = false;",
		"$cfg['Servers'][$i]['AllowRoot'] = false;",
	} {
		if !strings.Contains(config, required) {
			t.Fatalf("the configuration is missing %q", required)
		}
	}
}
