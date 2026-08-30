package firewall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Backups.
//
// ufw's state is files: the rules it enforces and a flag saying whether it is
// switched on. Backing it up is copying them, and restoring is copying them
// back and reloading — which is both simpler and more complete than replaying
// a list of rules through the command line, because it restores the parts the
// panel does not model as well as the parts it does.
//
// CLAUDE.md section 18 says never to overwrite a valid backup before verifying
// the new one. That applies here as much as to a website: each change gets its
// own directory, and nothing is ever written over.

// stateFiles are the files that hold ufw's configuration.
//
// user.rules and user6.rules are the rules themselves. ufw.conf holds
// ENABLED=yes|no, which is why restoring it also restores whether the firewall
// was on — a rollback of "enable" has to switch it off again.
var stateFiles = []string{"user.rules", "user6.rules", "ufw.conf"}

// ufwDir is where those files live.
const ufwDir = "/etc/ufw"

// backup copies the current rules and returns the directory holding them.
func (p *Provider) backup(ctx context.Context, status Status) (string, error) {
	if err := p.ensureStateDir(); err != nil {
		return "", err
	}

	dir := filepath.Join(p.stateDir, "backups",
		time.Now().UTC().Format("20060102T150405.000000000"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create the backup directory: %w", err)
	}

	copied := 0
	for _, name := range stateFiles {
		source := filepath.Join(p.ufwDir(), name)
		data, err := os.ReadFile(source) //nolint:gosec // a fixed path
		if err != nil {
			if os.IsNotExist(err) {
				// A stock ufw has all three; a host that has never enabled it
				// may not have user6.rules. Absence is recorded by the file
				// not being in the backup, and restore skips what it does not
				// have.
				continue
			}
			return "", fmt.Errorf("read %s: %w", source, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return "", fmt.Errorf("write the backup of %s: %w", name, err)
		}
		copied++
	}

	if copied == 0 {
		return "", fmt.Errorf("%w: no ufw configuration was found in %s",
			ErrUnavailable, p.ufwDir())
	}

	// The parsed state goes in beside the files. The files are what a restore
	// uses; this is what a person reads when they want to know what they had.
	if data, err := json.MarshalIndent(status, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "status.json"), data, 0o600)
	}

	p.pruneBackups()
	return dir, nil
}

// restore puts a backup back and reloads the firewall.
func (p *Provider) restore(ctx context.Context, dir string) error {
	if dir == "" {
		return fmt.Errorf("%w: no backup was recorded", ErrNoPendingChange)
	}
	// The path came from the Agent's own records, and is checked anyway: this
	// copies files into /etc as root.
	if !strings.HasPrefix(filepath.Clean(dir), filepath.Join(p.stateDir, "backups")) {
		return fmt.Errorf("refusing to restore from outside %s", p.stateDir)
	}

	restored := 0
	for _, name := range stateFiles {
		data, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // checked above
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read the backup of %s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(p.ufwDir(), name), data, 0o640); err != nil {
			return fmt.Errorf("restore %s: %w", name, err)
		}
		restored++
	}

	if restored == 0 {
		return fmt.Errorf("the backup in %s is empty", dir)
	}

	// Written rules are not enforced rules. ufw reloads its own files when it
	// is told to, and the reload is what makes the restore real.
	return p.reload(ctx)
}

// reload makes ufw read the rules on disk.
//
// Disable then enable rather than "ufw reload": reload is a no-op when the
// firewall is off, and the restored ufw.conf may have just turned it on or off.
// Reading the restored flag and acting on it is what makes a rollback of
// "enable" actually switch it back off.
func (p *Provider) reload(ctx context.Context) error {
	enabled, err := p.enabledInConfig()
	if err != nil {
		return err
	}

	if !enabled {
		return p.disable(ctx)
	}

	// Enabling an already-enabled ufw re-reads the rules, which is the reload.
	if err := p.disable(ctx); err != nil {
		return err
	}
	return p.enable(ctx)
}

// enabledInConfig reads the flag in the restored ufw.conf.
func (p *Provider) enabledInConfig() (bool, error) {
	data, err := os.ReadFile(filepath.Join(p.ufwDir(), "ufw.conf")) //nolint:gosec // fixed path
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read ufw.conf: %w", err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found || strings.TrimSpace(key) != "ENABLED" {
			continue
		}
		return strings.EqualFold(strings.TrimSpace(value), "yes"), nil
	}
	return false, nil
}

// maxBackups bounds how many are kept.
//
// Enough to look back over a session's changes, few enough that a directory
// describing the host's open ports does not accumulate indefinitely.
const maxBackups = 20

// pruneBackups removes the oldest beyond the limit.
//
// Failures are logged rather than returned: a full backup directory must not
// stop a firewall change, because the change may be the one closing a hole.
func (p *Provider) pruneBackups() {
	dir := filepath.Join(p.stateDir, "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	if len(names) <= maxBackups {
		return
	}

	// The names are timestamps, so lexical order is chronological.
	sort.Strings(names)
	for _, name := range names[:len(names)-maxBackups] {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			p.state.log.Warn("could not remove an old firewall backup",
				"backup", name, "error", err.Error())
		}
	}
}

// Backups lists the snapshots taken, newest first.
func (p *Provider) Backups() []string {
	entries, err := os.ReadDir(filepath.Join(p.stateDir, "backups"))
	if err != nil {
		return nil
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names
}

// ufwDir is where ufw keeps its configuration. A field so tests can point it
// somewhere writable.
func (p *Provider) ufwDir() string {
	if p.configDir != "" {
		return p.configDir
	}
	return ufwDir
}
