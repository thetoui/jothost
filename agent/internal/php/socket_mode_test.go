package php_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/jothost/panel/agent/internal/php"
)

// nginx connects to each site's FPM socket, so the socket directory must be
// traversable by it. /run is emptied at every boot and PrepareRuntime is what
// recreates this directory; under the Agent's 0077 umask it came back 0700
// and every PHP site answered 502 after a reboot - which no container-based
// check ever performs.
func TestTheSocketDirectoryIsReachableByTheWebServer(t *testing.T) {
	previous := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previous) })

	root := t.TempDir()
	provider := php.NewProvider(php.ProviderOptions{Root: root})
	if err := provider.PrepareRuntime("", -1, -1); err != nil {
		t.Fatalf("PrepareRuntime: %v", err)
	}

	// Every directory the call created, from the socket directory up to just
	// below the test root.
	for dir := filepath.Join(root, php.SocketDir); dir != root; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if got := info.Mode().Perm(); got != 0o755 {
			t.Fatalf("%s is %04o, want 0755", dir, got)
		}
	}
}
