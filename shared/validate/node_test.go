package validate

import (
	"strings"
	"testing"
)

func TestNodeVersionAcceptsAMajor(t *testing.T) {
	for _, version := range []string{"20", "22", "23", "18.19"} {
		if err := NodeVersion(version); err != nil {
			t.Errorf("NodeVersion(%q) = %v, want nil", version, err)
		}
	}
}

func TestNodeVersionRejectsAnythingElse(t *testing.T) {
	for _, version := range []string{"", "v22", "22.11.0", "latest", "0", "22; rm -rf /"} {
		if err := NodeVersion(version); err == nil {
			t.Errorf("NodeVersion(%q) = nil, want a refusal", version)
		}
	}
}

// The name becomes a systemd unit name, a log file name, and part of a path.
// A value with a slash, a space, or an @ produces a unit name systemd reads as
// something else entirely.
func TestAppNameRejectsAnythingThatIsNotAUnitStem(t *testing.T) {
	for _, name := range []string{
		"", "App", "1app", "-app", "my app", "my/app", "app@host",
		"app.service", "app;reboot", strings.Repeat("a", 41),
	} {
		if err := AppName(name); err == nil {
			t.Errorf("AppName(%q) = nil, want a refusal", name)
		}
	}
	for _, name := range []string{"app", "my-app", "my_app", "app2", strings.Repeat("a", 40)} {
		if err := AppName(name); err != nil {
			t.Errorf("AppName(%q) = %v, want nil", name, err)
		}
	}
}

func TestAppPortRefusesPrivilegedPorts(t *testing.T) {
	// An application the panel runs is never privileged, so it can never bind
	// one of these however the port was chosen.
	for _, port := range []int{0, 1, 80, 443, 1023} {
		if err := AppPort(port); err == nil {
			t.Errorf("AppPort(%d) = nil, want a refusal", port)
		}
	}
}

// The kernel hands the ephemeral range to outgoing connections. An application
// asked to listen there starts fine most days and fails on the day something
// else got there first, which looks random and is not.
func TestAppPortRefusesTheEphemeralRange(t *testing.T) {
	for _, port := range []int{32768, 40000, 65535} {
		err := AppPort(port)
		if err == nil {
			t.Errorf("AppPort(%d) = nil, want a refusal", port)
			continue
		}
		if !strings.Contains(err.Error(), "ephemeral") {
			t.Errorf("AppPort(%d) = %v, want the reason to name the ephemeral range", port, err)
		}
	}
}

func TestAppPortAcceptsTheUsualRange(t *testing.T) {
	for _, port := range []int{1024, 3000, 8080, 32767} {
		if err := AppPort(port); err != nil {
			t.Errorf("AppPort(%d) = %v, want nil", port, err)
		}
	}
}

// Setting one of these turns "configure my application" into "run something
// else in it".
func TestEnvKeyRefusesTheLoaderVariables(t *testing.T) {
	for _, key := range []string{
		"LD_PRELOAD", "LD_LIBRARY_PATH", "NODE_OPTIONS", "PATH", "BASH_ENV",
	} {
		if err := EnvKey(key); err == nil {
			t.Errorf("EnvKey(%q) = nil, want a refusal", key)
		}
	}
}

func TestEnvKeyRefusesThePanelsOwnVariables(t *testing.T) {
	// The panel sets these from the application's record. Accepting them would
	// let one value contradict the other.
	for _, key := range []string{"PORT", "HOME", "USER"} {
		if err := EnvKey(key); err == nil {
			t.Errorf("EnvKey(%q) = nil, want a refusal", key)
		}
	}
}

func TestEnvKeyShape(t *testing.T) {
	for _, key := range []string{"DATABASE_URL", "API_KEY", "_PRIVATE", "A1"} {
		if err := EnvKey(key); err != nil {
			t.Errorf("EnvKey(%q) = %v, want nil", key, err)
		}
	}
	for _, key := range []string{"", "lowercase", "1START", "HAS-DASH", "HAS SPACE"} {
		if err := EnvKey(key); err == nil {
			t.Errorf("EnvKey(%q) = nil, want a refusal", key)
		}
	}
}

// A newline would close the line in a unit file or an env file and begin a
// directive of the caller's choosing.
func TestEnvValueRefusesLineBreaksAndNulls(t *testing.T) {
	for _, value := range []string{"a\nExecStart=/bin/sh", "a\rb", "a\x00b"} {
		if err := EnvValue(value); err == nil {
			t.Errorf("EnvValue(%q) = nil, want a refusal", value)
		}
	}
	// Everything else is data, including characters a shell would care about —
	// because nothing here reaches a shell.
	for _, value := range []string{"", "postgres://u:p@h/db?x=1", "a b c", "$(whoami)", "; reboot"} {
		if err := EnvValue(value); err != nil {
			t.Errorf("EnvValue(%q) = %v, want nil", value, err)
		}
	}
}

func TestStartupFileMustStayInsideTheApplication(t *testing.T) {
	for _, path := range []string{
		"", "/etc/passwd", "../../etc/passwd", "../server.js", "a/../../b",
		"dist/../../x", "has space.js", "a;b.js",
	} {
		if err := StartupFile(path); err == nil {
			t.Errorf("StartupFile(%q) = nil, want a refusal", path)
		}
	}
	for _, path := range []string{"server.js", "dist/main.js", "src/index.mjs", "build/server-1.js"} {
		if err := StartupFile(path); err != nil {
			t.Errorf("StartupFile(%q) = %v, want nil", path, err)
		}
	}
}

// A suggested name must itself pass validation, or the panel offers a default
// it then refuses to accept.
func TestAppNameForProducesAValidName(t *testing.T) {
	for _, domain := range []string{
		"example.com", "123.example.com", "x.io",
		"a-very-long-domain-name-that-runs-past-the-unit-name-limit.example.com",
	} {
		name := AppNameFor(domain, "a1b2c3")
		if err := AppName(name); err != nil {
			t.Errorf("AppNameFor(%q) = %q, which is invalid: %v", domain, name, err)
		}
	}
}
