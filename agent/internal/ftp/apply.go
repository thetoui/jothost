package ftp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Changing the configuration.
//
// The sequence is the one Phases 16, 17 and 18 established:
//
//	1. back up what is there
//	2. render the whole drop-in from the panel's state
//	3. install it
//	4. ask proftpd to validate — `proftpd -t` reads the *entire* configuration,
//	   so a setting that conflicts with the distribution's is caught here and
//	   not at the next restart, by which time the daemon is down
//	5. restart the daemon
//	6. check it came back, and check nothing else is overriding what was written
//	7. restore the backup on any failure, and restart again
//
// Step 4 is deliberately after the install rather than against a temporary
// file: proftpd validates a configuration in place, includes and all, and
// checking a candidate somewhere else would check something the daemon will
// never read.
//
// Step 6's conflict scan exists because of what Phase 18 found the hard way. A
// drop-in that loses to another file produces no error anywhere: the panel
// writes a correct file, the validator accepts it, the daemon reloads, and the
// setting in force is somebody else's.

// timeNow is a variable so tests can be deterministic.
var timeNow = time.Now

// ApplyResult reports what a change did.
type ApplyResult struct {
	// Path is the file that was written.
	Path string `json:"path"`
	// Backup is where the previous file was kept, empty when there was none.
	Backup string `json:"backup,omitempty"`
	// Reloaded reports whether the running daemon picked the change up. False
	// with no error means the configuration is on disk and correct and the
	// daemon is not running — which is a true thing to tell an operator, not a
	// failure to hide.
	Reloaded bool `json:"reloaded"`
	// Conflicts names settings another included file also sets, and which of
	// the two the daemon is using.
	Conflicts []Conflict `json:"conflicts,omitempty"`
}

// Conflict is one directive two files disagree about.
type Conflict struct {
	Directive string `json:"directive"`
	// File is the other file setting it.
	File string `json:"file"`
	// PanelWins reports whether the panel's file is read first, and so wins.
	PanelWins bool `json:"panel_wins"`
}

// ownedDirectives are the settings this package writes.
//
// They are listed rather than derived from the rendered file so that the scan
// looks for what the panel *means* to control, not for whatever happens to be
// in the output of one particular render — a setting that is only emitted when
// a feature is on would otherwise stop being checked the moment it is off.
var ownedDirectives = []string{
	"AuthUserFile",
	"AuthOrder",
	"DefaultRoot",
	"PassivePorts",
	"ScoreboardFile",
	"MasqueradeAddress",
	"MaxClients",
	"RequireValidShell",
}

// Apply writes the panel's configuration and puts it into service.
func (p *Provider) Apply(ctx context.Context, s Settings, users []User) (ApplyResult, error) {
	if !p.Available() {
		return ApplyResult{}, ErrUnavailable
	}
	if err := p.ensureDirs(); err != nil {
		return ApplyResult{}, err
	}

	result := ApplyResult{Path: p.paths.DropIn()}

	backup, err := p.backup()
	if err != nil {
		return result, err
	}
	result.Backup = backup

	if err := writeFile(p.paths.DropIn(), Render(p.paths, s, users), 0o644); err != nil {
		return result, err
	}

	if err := p.Validate(ctx); err != nil {
		// The rejected file must not be left where proftpd will read it at the
		// next restart — which is what a reboot would do, taking the daemon
		// down with a configuration nobody chose.
		p.restore(ctx, backup)
		return result, err
	}

	if p.reload != nil {
		if err := p.reload.Reload(ctx); err != nil {
			p.restore(ctx, backup)
			return result, fmt.Errorf("restart the FTP server: %w", err)
		}
		result.Reloaded = p.reload.Running(ctx)
		if !result.Reloaded {
			// The daemon accepted the configuration and then did not come
			// back. Whatever the reason, the host now has no FTP server, and
			// the configuration that was working before is the one to have.
			p.restore(ctx, backup)
			return result, fmt.Errorf(
				"%w: it validated the configuration and then did not start",
				ErrNotRunning)
		}
	}

	result.Conflicts = p.conflicts()
	for _, conflict := range result.Conflicts {
		if !conflict.PanelWins {
			p.log.Warn("another FTP configuration file overrides the panel",
				"directive", conflict.Directive, "file", conflict.File)
		}
	}
	return result, nil
}

// backup copies the current drop-in aside and returns where it went.
//
// It returns an empty path when there was nothing to back up, and that
// distinction is the whole point of returning it at all: Phase 18 had a
// rollback that rebuilt this path by convention, and on a first write it tried
// to restore a backup that had never been taken and left the rejected file in
// place. Here an empty backup means "there was no file", and restoring it
// removes what was written.
func (p *Provider) backup() (string, error) {
	current, err := os.ReadFile(p.paths.DropIn())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read the current FTP configuration: %w", err)
	}

	path := fmt.Sprintf("%s.bak.%s", p.paths.DropIn(), timeNow().UTC().Format("20060102T150405Z"))
	if err := writeFile(path, string(current), 0o644); err != nil {
		return "", fmt.Errorf("back up the FTP configuration: %w", err)
	}
	return path, nil
}

// restore puts back what was there before, and restarts the daemon.
//
// Errors here are logged rather than returned: the caller is already failing,
// and the error it is failing with is the one that explains what happened. A
// rollback that fails is logged loudly because it means the host is now in a
// state nobody asked for.
func (p *Provider) restore(ctx context.Context, backup string) {
	if backup == "" {
		// There was no previous file, so the correct restoration is removing
		// the one just written.
		if err := os.Remove(p.paths.DropIn()); err != nil && !os.IsNotExist(err) {
			p.log.Error("could not remove the rejected FTP configuration",
				"path", p.paths.DropIn(), "error", err.Error())
		}
	} else {
		previous, err := os.ReadFile(backup)
		if err != nil {
			p.log.Error("could not read the FTP configuration backup",
				"path", backup, "error", err.Error())
			return
		}
		if err := writeFile(p.paths.DropIn(), string(previous), 0o644); err != nil {
			p.log.Error("could not restore the FTP configuration",
				"path", p.paths.DropIn(), "error", err.Error())
			return
		}
	}

	if p.reload == nil {
		return
	}
	if err := p.reload.Reload(ctx); err != nil {
		p.log.Error("could not restart the FTP server after rolling back",
			"error", err.Error())
	}
}

// conflicts reports directives another included file also sets.
//
// It reads the files rather than asking proftpd, because proftpd has no way to
// be asked: there is no -T that dumps an effective configuration the way sshd
// has. So this is a scan of the same directory the daemon reads, in the same
// sorted order, which is as close to the daemon's own view as the tool allows.
func (p *Provider) conflicts() []Conflict {
	entries, err := os.ReadDir(p.paths.DropInDir())
	if err != nil {
		p.log.Warn("could not scan the FTP configuration directory", "error", err.Error())
		return nil
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".conf") {
			continue
		}
		names = append(names, entry.Name())
	}
	// The same order proftpd includes them in.
	sort.Strings(names)

	found := make([]Conflict, 0)
	for _, name := range names {
		if name == dropInName {
			continue
		}
		data, err := os.ReadFile(filepath.Join(p.paths.DropInDir(), name))
		if err != nil {
			continue
		}
		for _, directive := range setDirectives(string(data)) {
			if !owns(directive) {
				continue
			}
			found = append(found, Conflict{
				Directive: directive,
				File:      name,
				// First wins, so the panel keeps the setting when its file
				// sorts earlier.
				PanelWins: dropInName < name,
			})
		}
	}
	return found
}

// setDirectives lists the directives a configuration file sets at top level.
func setDirectives(content string) []string {
	seen := map[string]bool{}
	names := make([]string, 0, 4)
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		name := strings.Fields(trimmed)[0]
		if strings.HasPrefix(name, "<") || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

func owns(directive string) bool {
	for _, owned := range ownedDirectives {
		if strings.EqualFold(owned, directive) {
			return true
		}
	}
	return false
}

// writeFile installs a file atomically.
//
// Through a temporary file in the same directory and a rename, so that proftpd
// — or the validator, which reads the same path — never sees a half-written
// configuration. A rename within a directory is atomic; a write in place is a
// window in which the file is neither the old one nor the new one.
func writeFile(path, content string, mode os.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".jothost-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tempPath := temp.Name()
	// Removed on every path that does not rename it away.
	defer func() { _ = os.Remove(tempPath) }()

	if _, err := temp.WriteString(content); err != nil {
		temp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return fmt.Errorf("set the mode of %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("install %s: %w", path, err)
	}
	return nil
}
