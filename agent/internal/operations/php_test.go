package operations

import (
	"testing"

	"github.com/jothost/panel/agent/internal/php"
	"github.com/jothost/panel/shared/protocol"
)

// phpFixture builds a registry whose host has no PHP at all, which is what a
// version that never installed successfully looks like.
func phpFixture(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t)
	f.registry.deps.PHP = php.NewDetector(php.DetectorOptions{Root: t.TempDir()})
	return f
}

// Removing a version the host does not have must succeed.
//
// The package manager refuses to remove a package it never installed, so
// reporting that as a failure left a version whose installation had failed
// pinned in the panel: the only action that could clear it was the one that
// would not run.
func TestUninstallingAVersionTheHostDoesNotHaveSucceeds(t *testing.T) {
	f := phpFixture(t)

	resp := f.dispatch(protocol.OperationPHPUninstall, map[string]any{"version": "8.5"})

	if resp.Status != protocol.StatusSuccess {
		t.Fatalf("removing an absent version failed: %+v", resp.Error)
	}

	data := wireData(t, resp)
	if data["version"] != "8.5" {
		t.Fatalf("version = %v, want 8.5", data["version"])
	}
	// Reported honestly: nothing was removed, because there was nothing there.
	if data["removed"] != false {
		t.Fatalf("removed = %v, want false when the version was never installed", data["removed"])
	}
}

// The reconciliation must not swallow a malformed version: that is a bad
// request, not a host already in the desired state.
func TestUninstallStillValidatesTheVersion(t *testing.T) {
	f := phpFixture(t)

	for _, version := range []string{"", "latest", "8.3.19", "../../etc", "8.3;rm -rf /"} {
		resp := f.dispatch(protocol.OperationPHPUninstall, map[string]any{"version": version})
		if resp.Status == protocol.StatusSuccess {
			t.Fatalf("version %q was accepted", version)
		}
		if resp.Error == nil || resp.Error.Code != protocol.CodeInvalidPayload {
			t.Fatalf("version %q: error = %+v, want an invalid payload", version, resp.Error)
		}
	}
}
