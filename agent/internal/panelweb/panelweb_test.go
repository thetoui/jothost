package panelweb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What these hold down is the separation itself.
//
// Every one of them corresponds to something that was wrong when the panel's
// applications shared the website system's nginx and PHP: a path resolved
// against the wrong prefix, a directory the web server could not traverse, a
// configuration that read somebody else's sites. None of it was visible in a
// status endpoint — the panel reported phpMyAdmin installed and served while
// it answered 502 to everything.

func managerIn(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	return NewManager(Options{
		Root:     filepath.Join(dir, "web"),
		RunDir:   filepath.Join(dir, "run"),
		WebGroup: "nginx",
	})
}

func TestThePanelReadsOnlyItsOwnSites(t *testing.T) {
	manager := managerIn(t)
	config := manager.renderNginxConfig()

	if !strings.Contains(config, filepath.Join(manager.root, "sites.d")+"/*.conf") {
		t.Fatalf("the panel's nginx does not include its own sites:\n%s", config)
	}
	// The whole point. An include of the websites' directory would put every
	// customer site back inside the panel's instance.
	if strings.Contains(config, "/etc/nginx/conf.d") {
		t.Fatalf("the panel's nginx reads the websites' directory:\n%s", config)
	}
}

func TestEveryWritablePathIsOutsideTheHostNginxTree(t *testing.T) {
	manager := managerIn(t)
	config := manager.renderNginxConfig()

	// nginx resolves a relative path against its prefix, and this instance's
	// prefix is the panel's tree — so a relative temp path would have it
	// writing inside the panel's configuration directory, and a relative log
	// path made it refuse to start at all.
	for _, directive := range []string{
		"client_body_temp_path", "proxy_temp_path", "fastcgi_temp_path",
		"uwsgi_temp_path", "scgi_temp_path", "access_log", "error_log", "pid",
	} {
		line := directiveValue(config, directive)
		if line == "" {
			t.Fatalf("%s is not set; nginx would fall back to a path inside its prefix", directive)
		}
		if !strings.HasPrefix(line, "/") {
			t.Fatalf("%s is relative (%q), so it resolves against the panel's own tree", directive, line)
		}
	}
}

func TestThePanelRefusesAHostItDoesNotServe(t *testing.T) {
	manager := managerIn(t)
	config := manager.renderNginxConfig()

	// Without a default server, a request with an unexpected Host is answered
	// by whichever application sorts first — which is how one panel
	// application ends up serving another's URLs.
	if !strings.Contains(config, "default_server") || !strings.Contains(config, "return 444") {
		t.Fatalf("the panel's nginx has no refusing default server:\n%s", config)
	}
}

func TestTheWorkersRunAsTheGroupThatOwnsTheSocket(t *testing.T) {
	manager := managerIn(t)
	config := manager.renderNginxConfig()

	// Left to nginx's compiled-in default, the workers may not be able to open
	// the panel's PHP socket, and every request is a 502 against a socket
	// sitting there with exactly the right owner and mode.
	if !strings.Contains(config, "user nginx;") {
		t.Fatalf("the panel's nginx does not name a user:\n%s", config)
	}
}

func TestThePHPMasterReadsOnlyThePanelPools(t *testing.T) {
	manager := managerIn(t)
	if err := manager.EnsureLayout(); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}
	if err := manager.writeFPMConfig("8.4"); err != nil {
		t.Fatalf("writeFPMConfig: %v", err)
	}

	content, err := os.ReadFile(FPMConfigPathIn(manager.root))
	if err != nil {
		t.Fatalf("read the FPM config: %v", err)
	}
	config := string(content)

	if !strings.Contains(config, manager.PoolDir()) {
		t.Fatalf("the panel's PHP master does not read the panel's pools:\n%s", config)
	}
	// A website's pool that will not load must not be able to stop this master
	// starting, which it could if this master read the websites' directory.
	if strings.Contains(config, "/etc/php") {
		t.Fatalf("the panel's PHP master reads the websites' pool directory:\n%s", config)
	}
}

func TestTheRunDirectoryIsNotInsideTheAgentSocketDirectory(t *testing.T) {
	// /run/jothost is 0750 root:jothost so only the Agent's account reaches
	// the privileged socket in it. nginx is not in that group, so a run
	// directory underneath it was one the panel's nginx could not traverse —
	// and the fix must never be to loosen the socket's directory.
	if strings.HasPrefix(RunDir, "/run/jothost/") {
		t.Fatalf("the panel's run directory is inside the Agent's socket directory: %s", RunDir)
	}
}

func TestTheLayoutIsIdempotent(t *testing.T) {
	manager := managerIn(t)

	for attempt := 1; attempt <= 3; attempt++ {
		if err := manager.EnsureLayout(); err != nil {
			t.Fatalf("EnsureLayout attempt %d: %v", attempt, err)
		}
	}

	// Called on every Agent start, not only at install: /run and /var are
	// emptied by reboots and cleaners.
	for _, path := range []string{
		manager.root,
		filepath.Join(manager.root, "sites.d"),
		manager.PoolDir(),
		manager.runDir,
		ConfigPathIn(manager.root),
		manager.FastCGIParams(),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s was not created: %v", path, err)
		}
	}
}

func TestNothingIsRunningBeforeAnythingStarts(t *testing.T) {
	manager := managerIn(t)

	// An empty or missing pid file is not a running process. Reading one as
	// "running" produced a reload against a pid file holding nothing, which
	// failed an install that had otherwise worked.
	if manager.NginxRunning() || manager.FPMRunning() {
		t.Fatal("a process was reported running with no pid file")
	}

	if err := manager.EnsureLayout(); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(manager.runDir, "nginx.pid"), []byte(""), 0o644); err != nil {
		t.Fatalf("write an empty pid file: %v", err)
	}
	if manager.NginxRunning() {
		t.Fatal("an empty pid file was read as a running process")
	}
}

// directiveValue returns the argument of a top-level nginx directive.
func directiveValue(config, directive string) string {
	for _, line := range strings.Split(config, "\n") {
		fields := strings.Fields(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ";")))
		if len(fields) >= 2 && fields[0] == directive {
			return fields[1]
		}
	}
	return ""
}
