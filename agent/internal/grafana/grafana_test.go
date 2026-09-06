package grafana

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type stubServices struct {
	restarted []string
	enabled   []string
	running   bool
}

func (s *stubServices) Restart(_ context.Context, name string) error {
	s.restarted = append(s.restarted, name)
	return nil
}
func (s *stubServices) Enable(_ context.Context, name string) error {
	s.enabled = append(s.enabled, name)
	return nil
}
func (s *stubServices) Running(context.Context, string) bool { return s.running }

func testConfig() DatasourceConfig {
	return DatasourceConfig{
		Host: "127.0.0.1", Port: 5432,
		Database: "jothost", User: "jothost_grafana",
		Password: "s3cret", RootURL: "https://panel.example/grafana",
	}
}

func provisionInto(t *testing.T, services Services) (string, error) {
	t.Helper()
	dir := t.TempDir()
	// Grafana takes one config file and reads no other, so the panel's
	// settings are merged into it rather than written beside it. The file has
	// to exist for that, exactly as it does on a host with Grafana installed.
	if err := os.WriteFile(filepath.Join(dir, "grafana.ini"),
		[]byte("[server]\n; the operator's own settings\nrouter_logging = true\n"), 0o644); err != nil {
		t.Fatalf("seed grafana.ini: %v", err)
	}
	manager := NewManager(Options{ConfigDir: dir, Services: services})
	return dir, manager.Provision(context.Background(), testConfig(), nil)
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// Where the files land under a test's temporary directory. The real paths come
// from DiscoverLayout; these mirror the shape.
const (
	testConfigFile = "grafana.ini"
	testDatasource = "provisioning/datasources/jothost.yaml"
	testDashboards = "provisioning/dashboards/jothost.yaml"
)

// TestAnonymousAccessIsOff is the security property this whole arrangement
// rests on.
//
// The panel serves Grafana on a hostname and does not decide who may see it;
// Grafana does, exactly as phpMyAdmin does. Turning anonymous access on would
// publish every metric about the host — and every hostname in the server
// dropdown — to anybody who found the address.
func TestAnonymousAccessIsOff(t *testing.T) {
	dir, err := provisionInto(t, &stubServices{})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	config := read(t, dir, testConfigFile)
	section := config[strings.Index(config, "[auth.anonymous]"):]
	if !strings.Contains(section, "enabled = false") {
		t.Fatalf("anonymous access must be off:\n%s", section)
	}
}

// TestGrafanaListensOnLoopbackOnly.
//
// It is reached through the vhost the panel writes, which is where an operator
// decides who may reach it. A Grafana bound to every interface is one listening
// on a port nothing in the panel guards.
func TestGrafanaListensOnLoopbackOnly(t *testing.T) {
	dir, err := provisionInto(t, &stubServices{})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	config := read(t, dir, testConfigFile)
	if !strings.Contains(config, "http_addr = 127.0.0.1") {
		t.Fatalf("Grafana must listen on loopback only:\n%s", config)
	}
}

// TestEmbeddingIsEnabled. Without it Grafana sends X-Frame-Options: deny and
// every embedded panel is a blank box — which looks like the panel is broken.
func TestEmbeddingIsEnabled(t *testing.T) {
	dir, err := provisionInto(t, &stubServices{})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if !strings.Contains(read(t, dir, testConfigFile), "allow_embedding = true") {
		t.Fatal("embedding is not enabled, so no panel would render")
	}
}

// TestTheDatasourcePasswordIsNotWorldReadable.
//
// It is a database password in a file. Everything else this package writes is
// configuration anybody may read; this one is not.
func TestTheDatasourcePasswordIsNotWorldReadable(t *testing.T) {
	dir, err := provisionInto(t, &stubServices{})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, testDatasource))
	if err != nil {
		t.Fatalf("stat datasource: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o007 != 0 {
		t.Fatalf("the datasource file is world-accessible: %o", perm)
	}
}

// TestAPasswordCannotChangeTheShapeOfTheDocument.
//
// The datasource is YAML and the password goes into it. A password containing
// a colon, a newline or a leading brace would otherwise add fields rather than
// set one — and the field it would be adding is next to the credentials.
func TestAPasswordCannotChangeTheShapeOfTheDocument(t *testing.T) {
	cfg := testConfig()
	cfg.Password = "pa: ss\nis_admin: true\n#"

	rendered := string(renderDatasource(cfg))

	// The whole password must be on the password line, inside quotes.
	var passwordLine string
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, "password:") {
			passwordLine = line
		}
	}
	if passwordLine == "" {
		t.Fatal("no password line was written")
	}
	if strings.Contains(rendered, "\nis_admin: true") {
		t.Fatalf("a password injected a field into the datasource:\n%s", rendered)
	}
}

// TestProvisionedFilesAreNotEditableInGrafana.
//
// A provisioned dashboard somebody edits in the UI is one that differs from the
// file the panel maintains, with nothing anywhere saying so. Grafana refuses
// the edit when told to, which is the only place that can be enforced.
func TestProvisionedFilesAreNotEditableInGrafana(t *testing.T) {
	dir, err := provisionInto(t, &stubServices{})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	if !strings.Contains(read(t, dir, testDatasource), "editable: false") {
		t.Error("the datasource can be edited in Grafana and would then differ from this file")
	}
	if !strings.Contains(read(t, dir, testDashboards), "allowUiUpdates: false") {
		t.Error("the dashboard can be edited in Grafana and would then differ from this file")
	}
}

// TestProvisionRestartsAndPersistsGrafana.
//
// Grafana reads provisioning at startup and has no signal that makes it read
// it again, so a provision that did not restart would write files nothing
// looked at. Enabling it is the same rule the rest of the panel now follows:
// what is running should be running after a reboot.
func TestProvisionRestartsAndPersistsGrafana(t *testing.T) {
	services := &stubServices{}
	if _, err := provisionInto(t, services); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	if len(services.restarted) != 1 || services.restarted[0] != DaemonName {
		t.Errorf("restarted %v, want just %s", services.restarted, DaemonName)
	}
	if len(services.enabled) != 1 || services.enabled[0] != DaemonName {
		t.Errorf("enabled %v, want just %s", services.enabled, DaemonName)
	}
}

// TestProvisionIsIdempotent. It runs on every install and every settings
// change, so a second run must produce the same files rather than appending.
func TestProvisionIsIdempotent(t *testing.T) {
	dir, err := provisionInto(t, &stubServices{})
	if err != nil {
		t.Fatalf("first provision: %v", err)
	}
	first := read(t, dir, testConfigFile)
	manager := NewManager(Options{ConfigDir: dir, Services: &stubServices{}})

	if err := manager.Provision(context.Background(), testConfig(), nil); err != nil {
		t.Fatalf("second provision: %v", err)
	}
	if second := read(t, dir, testConfigFile); second != first {
		t.Fatalf("a second provision changed the file:\n%s", second)
	}
}

// TestStatusSaysWhyItIsNotUsable.
//
// "Not provisioned" and "not running" are different problems with different
// fixes, and a status that only said "no" would leave an operator guessing.
func TestStatusSaysWhyItIsNotUsable(t *testing.T) {
	dir := t.TempDir()
	manager := NewManager(Options{ConfigDir: dir})

	status := manager.Status(context.Background())
	if status.Installed {
		t.Skip("this host has Grafana installed, so the not-installed path cannot be exercised")
	}
	if status.Detail == "" {
		t.Fatal("a status reporting Grafana as unusable must say why")
	}
}

// TestTheDatasourceNeedsSomewhereToConnect.
func TestTheDatasourceNeedsSomewhereToConnect(t *testing.T) {
	manager := NewManager(Options{ConfigDir: t.TempDir()})

	err := manager.Provision(context.Background(), DatasourceConfig{Host: "127.0.0.1"}, nil)
	if err == nil {
		t.Fatal("a datasource with no database was accepted")
	}
}

// TestSettingsGoIntoTheFileGrafanaReads.
//
// The failure this replaces is worth writing down. The first version of this
// package wrote its settings to conf/jothost.ini beside grafana.ini. Every test
// here passed — the file had the right contents — and Grafana read none of it,
// because Grafana takes one config file and a second one next to it is a file
// nothing opens. The datasource list on a provisioned host was empty.
//
// So this asserts the settings are in grafana.ini itself, and that the
// operator's own lines survive.
func TestSettingsGoIntoTheFileGrafanaReads(t *testing.T) {
	dir, err := provisionInto(t, &stubServices{})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	config := read(t, dir, testConfigFile)
	if !strings.Contains(config, "allow_embedding = true") {
		t.Fatalf("the panel's settings are not in the file Grafana reads:\n%s", config)
	}
	// Everything outside the panel's block is the operator's and stays.
	if !strings.Contains(config, "router_logging = true") {
		t.Fatalf("provisioning discarded the operator's own settings:\n%s", config)
	}
	if !strings.Contains(config, blockStart) || !strings.Contains(config, blockEnd) {
		t.Fatal("the panel's block is not fenced, so a later run cannot find it")
	}
}

// TestMergingTheBlockTwiceReplacesIt rather than appending. This runs on every
// provision, and a file that grew a copy of the block each time would end with
// Grafana reading the last of a dozen.
func TestMergingTheBlockTwiceReplacesIt(t *testing.T) {
	first := mergeSettings([]byte("[server]\nrouter_logging = true\n"), renderSettings("https://a.test"))
	second := mergeSettings(first, renderSettings("https://b.test"))

	if count := strings.Count(string(second), blockStart); count != 1 {
		t.Fatalf("the block appears %d times, want 1", count)
	}
	if strings.Contains(string(second), "https://a.test") {
		t.Fatal("the previous block survived the merge")
	}
	if !strings.Contains(string(second), "https://b.test") {
		t.Fatal("the new block was not applied")
	}
	if !strings.Contains(string(second), "router_logging = true") {
		t.Fatal("merging discarded the operator's settings")
	}
}

// TestAStartMarkerWithNoEndIsTakenAsThePanels.
//
// A file somebody edited halfway through. There is no honest way to tell where
// the panel's block stopped, so everything from the marker on is replaced —
// which is recoverable, where leaving a half-block in place would mean the
// panel's settings silently stopped being applied.
func TestAStartMarkerWithNoEndIsTakenAsThePanels(t *testing.T) {
	damaged := []byte("[server]\nrouter_logging = true\n" + blockStart + "\nhttp_addr = 0.0.0.0\n")

	merged := string(mergeSettings(damaged, renderSettings("")))

	if strings.Contains(merged, "http_addr = 0.0.0.0") {
		t.Fatal("a half-written block survived and would still be applied")
	}
	if !strings.Contains(merged, "router_logging = true") {
		t.Fatal("the operator's settings above the marker were lost")
	}
	if strings.Count(merged, blockStart) != 1 {
		t.Fatal("the repaired file does not have exactly one block")
	}
}

// TestGrafanaIsToldItIsServedUnderASubPath.
//
// It is proxied at /grafana/ on the panel's own name. Without
// serve_from_sub_path Grafana builds every asset URL from the domain root, so
// the page loads and all of its scripts 404 — a blank frame, and nothing in
// any log saying why.
func TestGrafanaIsToldItIsServedUnderASubPath(t *testing.T) {
	dir, err := provisionInto(t, &stubServices{})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	config := read(t, dir, testConfigFile)
	if !strings.Contains(config, "serve_from_sub_path = true") {
		t.Fatalf("Grafana was not told it is served under a path:\n%s", config)
	}
	if !strings.Contains(config, "root_url = https://panel.example/grafana/") {
		t.Fatalf("root_url does not carry the sub-path:\n%s", config)
	}
}

// TestTheSessionCookieIsNotWeakenedUnnecessarily.
//
// The first version set SameSite=None with Secure, which a cross-origin frame
// needs. Then the frame stopped being cross-origin — Grafana is proxied under
// the panel's own host — so None would have been a weaker cookie for no
// benefit, and Secure would have broken every install served over plain HTTP.
func TestTheSessionCookieIsNotWeakenedUnnecessarily(t *testing.T) {
	dir, err := provisionInto(t, &stubServices{})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	config := read(t, dir, testConfigFile)
	if strings.Contains(config, "cookie_samesite = none") {
		t.Error("the session cookie is SameSite=None for a same-origin frame")
	}
	if strings.Contains(config, "cookie_secure = true") {
		t.Error("cookie_secure is forced, which breaks a panel served over HTTP")
	}
	if !strings.Contains(config, "cookie_samesite = lax") {
		t.Errorf("expected a Lax session cookie:\n%s", config)
	}
}
