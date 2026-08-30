package services

import (
	"context"
	"errors"
	"testing"
)

// fakeProcesses stands in for the process table.
type fakeProcesses map[string]int

func (f fakeProcesses) FindByName(names []string) map[string]int {
	found := map[string]int{}
	for _, name := range names {
		if pid, ok := f[name]; ok {
			found[name] = pid
		}
	}
	return found
}

// A host with no systemd still has a truthful status page. This is the
// difference between "nginx is running, and I cannot restart it here" and a
// blank list — and the second is what a panel that only asks systemd shows.
func TestDetectReportsRunningServicesWithoutASystemManager(t *testing.T) {
	// No runner at all: Available() is false, exactly like a container.
	provider := NewProvider(nil)

	detected := provider.Detect(context.Background(), nil, fakeProcesses{
		"nginx":    10,
		"mariadbd": 73,
	})

	byKey := map[string]Detected{}
	for _, entry := range detected {
		byKey[entry.Key] = entry
	}

	nginx, found := byKey["nginx"]
	if !found {
		t.Fatalf("nginx was not detected: %+v", byKey)
	}
	if !nginx.Running || nginx.PID != 10 {
		t.Fatalf("nginx = running %v pid %d, want running with pid 10",
			nginx.Running, nginx.PID)
	}
	if !nginx.Installed {
		t.Error("a service with a running process is installed")
	}
	// The honest half: it is up, and this host cannot be told to change that.
	if nginx.Controllable {
		t.Error("a host with no service manager must not claim it can control services")
	}
	// Unknown, not false: nothing here can say whether it starts at boot.
	if nginx.Enabled != nil {
		t.Errorf("enabled = %v, want nil (unknown)", *nginx.Enabled)
	}

	if _, found := byKey["mariadb"]; !found {
		t.Error("mariadb was not detected from its process name")
	}
}

// A panel listing services the host does not have is offering to manage
// something that is not there.
func TestDetectLeavesOutWhatIsNotInstalled(t *testing.T) {
	provider := NewProvider(nil)

	detected := provider.Detect(context.Background(), nil, fakeProcesses{})

	for _, entry := range detected {
		if !entry.Installed {
			t.Errorf("%s was listed but is not installed", entry.Key)
		}
	}
}

// PHP-FPM is one service per installed version, and which versions exist is
// only known at runtime.
func TestPHPFPMDefinitionsFollowTheInstalledVersions(t *testing.T) {
	definitions := PHPFPMDefinitions([]string{"8.4", "8.2"})
	if len(definitions) != 2 {
		t.Fatalf("got %d definitions, want 2", len(definitions))
	}
	// Sorted, so the list does not reshuffle between requests.
	if definitions[0].Key != "php-fpm8.2" || definitions[1].Key != "php-fpm8.4" {
		t.Fatalf("keys = %q, %q", definitions[0].Key, definitions[1].Key)
	}

	// The unit and process names distributions actually use.
	first := definitions[0]
	if !containsString(first.Units, "php-fpm82.service") {
		t.Errorf("units = %v, want the Alpine and RHEL name", first.Units)
	}
	if !containsString(first.Units, "php8.2-fpm.service") {
		t.Errorf("units = %v, want the Debian name", first.Units)
	}
	if !containsString(first.Processes, "php-fpm82") {
		t.Errorf("processes = %v, want the kernel's name for the master", first.Processes)
	}
}

func TestDetectIncludesContributedDefinitions(t *testing.T) {
	provider := NewProvider(nil)

	detected := provider.Detect(context.Background(),
		PHPFPMDefinitions([]string{"8.4"}),
		fakeProcesses{"php-fpm84": 6724})

	for _, entry := range detected {
		if entry.Key == "php-fpm8.4" {
			if !entry.Running || entry.PID != 6724 {
				t.Fatalf("php-fpm8.4 = running %v pid %d", entry.Running, entry.PID)
			}
			return
		}
	}
	t.Fatal("the contributed PHP-FPM definition was not detected")
}

// The list is read top to bottom by someone deciding what to restart: what
// serves the sites first, what stores their data next, the machine's own
// services last.
func TestDetectOrdersTheListByRole(t *testing.T) {
	provider := NewProvider(nil)

	detected := provider.Detect(context.Background(), nil, fakeProcesses{
		"nginx":    10,
		"sshd":     20,
		"postgres": 30,
		"crond":    40,
	})

	var roles []string
	for _, entry := range detected {
		roles = append(roles, entry.Role)
	}
	if len(roles) < 3 {
		t.Fatalf("expected several services, got %v", roles)
	}
	for i := 1; i < len(roles); i++ {
		if roleOrder(roles[i-1]) > roleOrder(roles[i]) {
			t.Fatalf("roles out of order: %v", roles)
		}
	}
}

// A key is not a unit name. Nothing a client sends may select something the
// catalogue does not name.
func TestUnitForRefusesAnythingOutsideTheCatalogue(t *testing.T) {
	provider := NewProvider(nil)
	ctx := context.Background()

	refused := []string{
		"systemd-logind.service",
		"../../etc/passwd",
		"--version",
		"nginx.service", // a real unit, but not a catalogue key
		"",
		"NGINX",
	}
	for _, key := range refused {
		if _, _, err := provider.UnitFor(ctx, key, nil); err == nil {
			t.Errorf("UnitFor(%q) was accepted", key)
		}
	}
}

func TestLookupFindsCataloguedAndContributedServices(t *testing.T) {
	if _, err := Lookup("nginx", nil); err != nil {
		t.Errorf("nginx: %v", err)
	}
	if _, err := Lookup("php-fpm8.4", PHPFPMDefinitions([]string{"8.4"})); err != nil {
		t.Errorf("contributed php-fpm8.4: %v", err)
	}
	if _, err := Lookup("php-fpm8.4", nil); err == nil {
		t.Error("php-fpm8.4 was found without being contributed")
	}
}

// sshd is the one the panel must not take away: stopping it on a remote host
// locks the operator out of the machine they are administering.
func TestSSHIsProtectedAndTheRestAreNot(t *testing.T) {
	for _, definition := range Catalogue() {
		switch definition.Key {
		case "sshd":
			if !definition.Protected {
				t.Error("sshd must be protected from stop and disable")
			}
		default:
			if definition.Protected {
				t.Errorf("%s is protected; only sshd should be", definition.Key)
			}
		}
	}
}

// Every catalogue entry has to be usable: a definition with no way to detect
// it is one that never appears, and a key that cannot be sent is one nothing
// can act on.
func TestEveryCatalogueEntryIsWellFormed(t *testing.T) {
	seen := map[string]bool{}

	for _, definition := range Catalogue() {
		if !validKey(definition.Key) {
			t.Errorf("key %q cannot be sent by a client", definition.Key)
		}
		if seen[definition.Key] {
			t.Errorf("duplicate key %q", definition.Key)
		}
		seen[definition.Key] = true

		if definition.Label == "" || definition.Summary == "" {
			t.Errorf("%s has nothing to show an operator", definition.Key)
		}
		if len(definition.Units) == 0 {
			t.Errorf("%s has no unit to control", definition.Key)
		}
		if len(definition.Processes) == 0 {
			t.Errorf("%s cannot be detected without systemd", definition.Key)
		}
		if len(definition.Binaries) == 0 {
			t.Errorf("%s cannot be found on a host where it is installed but stopped",
				definition.Key)
		}
		for _, path := range definition.Binaries {
			if path == "" || path[0] != '/' {
				t.Errorf("%s has a relative binary path %q", definition.Key, path)
			}
		}
		if roleOrder(definition.Role) == 4 && definition.Role != RoleSystem {
			t.Errorf("%s has an unknown role %q", definition.Key, definition.Role)
		}
	}
}

func containsString(haystack []string, needle string) bool {
	for _, value := range haystack {
		if value == needle {
			return true
		}
	}
	return false
}

// The rule that keeps an operator from locking themselves out of the host they
// are administering.
func TestProtectedServicesRefuseStopAndDisableButNotRestart(t *testing.T) {
	ssh, err := Lookup("sshd", nil)
	if err != nil {
		t.Fatalf("lookup sshd: %v", err)
	}

	for _, action := range []string{ActionStop, ActionDisable} {
		if err := ssh.Allows(action); !errors.Is(err, ErrProtected) {
			t.Errorf("%s on sshd = %v, want ErrProtected", action, err)
		}
	}
	// Restart is how a configuration change is applied, and it keeps the
	// listening socket — so it stays available.
	for _, action := range []string{ActionStart, ActionRestart, ActionEnable} {
		if err := ssh.Allows(action); err != nil {
			t.Errorf("%s on sshd = %v, want it allowed", action, err)
		}
	}

	nginx, err := Lookup("nginx", nil)
	if err != nil {
		t.Fatalf("lookup nginx: %v", err)
	}
	for _, action := range []string{ActionStart, ActionStop, ActionRestart, ActionEnable, ActionDisable} {
		if err := nginx.Allows(action); err != nil {
			t.Errorf("%s on nginx = %v, want it allowed", action, err)
		}
	}
}

// The verb set is closed. Masking makes a unit unstartable in a way that looks
// like a broken package to whoever comes next.
func TestUnknownVerbsAreRefused(t *testing.T) {
	nginx, err := Lookup("nginx", nil)
	if err != nil {
		t.Fatalf("lookup nginx: %v", err)
	}

	for _, action := range []string{"mask", "unmask", "reload", "kill", "", "START"} {
		if err := nginx.Allows(action); err == nil {
			t.Errorf("verb %q was accepted", action)
		}
	}
}
