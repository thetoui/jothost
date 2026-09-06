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

	override := read(t, dir, OverrideFile)
	section := override[strings.Index(override, "[auth.anonymous]"):]
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

	override := read(t, dir, OverrideFile)
	if !strings.Contains(override, "http_addr = 127.0.0.1") {
		t.Fatalf("Grafana must listen on loopback only:\n%s", override)
	}
}

// TestEmbeddingIsEnabled. Without it Grafana sends X-Frame-Options: deny and
// every embedded panel is a blank box — which looks like the panel is broken.
func TestEmbeddingIsEnabled(t *testing.T) {
	dir, err := provisionInto(t, &stubServices{})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if !strings.Contains(read(t, dir, OverrideFile), "allow_embedding = true") {
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

	info, err := os.Stat(filepath.Join(dir, DatasourceFile))
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

	if !strings.Contains(read(t, dir, DatasourceFile), "editable: false") {
		t.Error("the datasource can be edited in Grafana and would then differ from this file")
	}
	if !strings.Contains(read(t, dir, DashboardFile), "allowUiUpdates: false") {
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
	dir := t.TempDir()
	manager := NewManager(Options{ConfigDir: dir, Services: &stubServices{}})

	if err := manager.Provision(context.Background(), testConfig(), nil); err != nil {
		t.Fatalf("first provision: %v", err)
	}
	first := read(t, dir, OverrideFile)

	if err := manager.Provision(context.Background(), testConfig(), nil); err != nil {
		t.Fatalf("second provision: %v", err)
	}
	if second := read(t, dir, OverrideFile); second != first {
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
