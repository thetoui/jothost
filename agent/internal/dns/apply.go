package dns

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Changing what this host serves.
//
// The sequence is the one Phases 16 to 18 established, with one difference that
// matters: the zone files are validated *before* they are installed. BIND's
// named-checkzone will check any file against any origin, so a zone that would
// stop the server is caught while it is still a temporary file nothing reads.
// proftpd and sshd have no equivalent, which is why those phases had to install
// first and roll back after.
//
//	1. render every zone from the panel's state, and validate each one
//	2. install only the zones whose contents actually changed
//	3. back up and install the include file of zone statements
//	4. give this host a named.conf if it has none, or add one include line if it
//	   has one that is somebody else's
//	5. ask named-checkconf to read the whole configuration
//	6. start or reload the server
//	7. remove the files of zones that are no longer served
//	8. restore everything on any failure
//
// Step 2's "actually changed" is not an optimisation. Rewriting an unchanged
// zone means a reload, a re-signing, and a serial every secondary on the
// internet then pulls.

// timeNow is a variable so tests can be deterministic.
var timeNow = time.Now

// Desired is the complete state the panel wants this host to be in.
//
// Complete, not a delta: a zone the panel has forgotten about is a zone this
// host stops serving, which is what makes a delete take effect on a host the
// panel has not spoken to for a while.
type Desired struct {
	Settings Settings `json:"settings"`
	Zones    []Zone   `json:"zones"`
}

// ReconcileResult reports what a reconcile did.
type ReconcileResult struct {
	// IncludePath is the file of zone statements the panel owns.
	IncludePath string `json:"include_path"`
	// ConfigPath is BIND's own entry point.
	ConfigPath string `json:"config_path"`
	// CreatedConfig reports that this host had no named.conf and was given
	// one.
	CreatedConfig bool `json:"created_config"`
	// AddedInclude reports that an existing named.conf gained the panel's
	// include line.
	AddedInclude bool `json:"added_include"`
	// Changed names the zones whose file was rewritten.
	Changed []string `json:"changed"`
	// Removed names the zones this host stopped serving.
	Removed []string `json:"removed"`
	// Signed names the zones DNSSEC is on for.
	Signed []string `json:"signed"`
	// Reloaded reports whether the running server picked the change up.
	Reloaded bool `json:"reloaded"`
	// Warnings are things an operator should know that did not stop the
	// change: a zone whose files could not be cleaned up, a server that is
	// configured correctly and not running.
	Warnings []string `json:"warnings,omitempty"`
}

// Reconcile makes this host serve exactly what the panel says it should.
func (p *Provider) Reconcile(ctx context.Context, desired Desired) (ReconcileResult, error) {
	if !p.Available() {
		return ReconcileResult{}, ErrUnavailable
	}
	if !p.SupportsDNSSEC(ctx) {
		for _, zone := range desired.Zones {
			if zone.DNSSEC {
				return ReconcileResult{}, fmt.Errorf(
					"%w: %s asks for signing, and this BIND (%s) has no dnssec-policy",
					ErrNoDNSSEC, zone.Name, p.Version(ctx))
			}
		}
	}
	if err := p.ensureDirs(); err != nil {
		return ReconcileResult{}, err
	}

	result := ReconcileResult{
		IncludePath: p.paths.Include(),
		ConfigPath:  p.paths.MainConf(),
		Changed:     []string{},
		Removed:     []string{},
		Signed:      []string{},
	}

	// The zone files first, and validated before anything is installed.
	rendered := make(map[string]string, len(desired.Zones))
	for _, zone := range desired.Zones {
		if zone.DNSSEC {
			result.Signed = append(result.Signed, zone.Name)
		}
		if zone.Kind == "slave" {
			// A secondary's file is written by named when it transfers the
			// zone. Writing one here would be the panel inventing the contents
			// of a zone it does not own.
			continue
		}
		content, err := RenderZone(zone)
		if err != nil {
			return result, err
		}
		if err := p.checkCandidate(ctx, zone.Name, content); err != nil {
			return result, err
		}
		rendered[zone.Name] = content
	}

	// What is on the host now, so removals can be found and the include can be
	// rolled back to it.
	previous := p.ServedZones()

	include, err := RenderInclude(p.paths, desired.Settings, desired.Zones)
	if err != nil {
		return result, err
	}

	// Install the zone files that differ. A zone that is byte-identical is
	// left alone, journal, signatures and all.
	written := make([]string, 0, len(rendered))
	for name, content := range rendered {
		path := p.paths.ZoneFile(name)
		if same, err := fileHas(path, content); err != nil {
			p.rollbackZones(written)
			return result, err
		} else if same {
			continue
		}
		if err := p.installZone(path, content); err != nil {
			p.rollbackZones(written)
			return result, err
		}
		written = append(written, name)
		result.Changed = append(result.Changed, name)
	}
	sort.Strings(result.Changed)

	includeBackup, err := backupFile(p.paths.Include())
	if err != nil {
		p.rollbackZones(written)
		return result, err
	}
	if err := writeFile(p.paths.Include(), include, 0o640); err != nil {
		p.rollbackZones(written)
		return result, err
	}
	if err := chownNamed(p.paths.Include()); err != nil {
		p.log.Warn("could not give the zone list to the name server's account",
			"error", err.Error())
	}

	mainBackup, created, added, err := p.ensureMainConf(desired.Settings)
	if err != nil {
		restoreFile(p.paths.Include(), includeBackup, p.log)
		p.rollbackZones(written)
		return result, err
	}
	result.CreatedConfig = created
	result.AddedInclude = added

	if err := p.Validate(ctx); err != nil {
		// Nothing named will read at its next start may be left behind: a
		// rejected configuration on disk is a server that fails to come back
		// after a reboot, and takes every zone with it.
		if created {
			if removeErr := os.Remove(p.paths.MainConf()); removeErr != nil {
				p.log.Error("could not remove the rejected named.conf",
					"path", p.paths.MainConf(), "error", removeErr.Error())
			}
		} else {
			restoreFile(p.paths.MainConf(), mainBackup, p.log)
		}
		restoreFile(p.paths.Include(), includeBackup, p.log)
		p.rollbackZones(written)
		return result, err
	}

	reloaded, warning := p.reload(ctx, result.Changed)
	result.Reloaded = reloaded
	if warning != "" {
		result.Warnings = append(result.Warnings, warning)
	}
	if reloaded {
		result.Warnings = append(result.Warnings, p.verifyLoaded(ctx, result.Changed)...)
	}

	// Only once the server has accepted the new configuration: a zone file
	// removed before that would leave a rollback pointing at nothing.
	for _, name := range previous {
		if _, still := rendered[name]; still {
			continue
		}
		if hasZone(desired.Zones, name) {
			// Still served, as a secondary. Its file is named's.
			continue
		}
		if err := removeZoneFiles(p.paths.ZoneFile(name)); err != nil {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("the files of the removed zone %s could not be deleted: %s",
					name, err.Error()))
			continue
		}
		result.Removed = append(result.Removed, name)
	}
	sort.Strings(result.Removed)

	return result, nil
}

// verifyLoaded asks the running server whether it really took the change.
//
// rndc's exit status says the command was accepted, not that the reload
// succeeded: a named that cannot read its own configuration answers "reloading
// configuration failed: permission denied" in its log and goes on serving what
// it already had, while rndc reports success. That is a change the panel would
// otherwise report as applied and that never took effect — which is the exact
// failure this phase produced by rewriting named.conf without giving it back to
// the name server's account.
//
// It reports rather than fails: the configuration on disk is correct and
// validated, and refusing the whole change would leave the panel and the host
// disagreeing in the other direction.
func (p *Provider) verifyLoaded(ctx context.Context, changed []string) []string {
	if !p.runner.Available(CommandRndc) {
		return nil
	}
	warnings := make([]string, 0)
	for _, zone := range changed {
		state, err := p.ZoneStatus(ctx, zone)
		if err != nil {
			continue
		}
		if !state.Loaded {
			warnings = append(warnings, fmt.Sprintf(
				"the name server accepted the reload and is not serving %s: %s",
				zone, state.Reason))
		}
	}
	return warnings
}

// checkCandidate validates a rendered zone before it is installed.
//
// The candidate is written into the state directory rather than a temporary
// one so that it is on the same filesystem as the file it may become, and so
// that a validator running as another account can read it.
func (p *Provider) checkCandidate(ctx context.Context, zone, content string) error {
	path := filepath.Join(p.paths.StateDir, ".candidate-"+zone+zoneSuffix)
	if err := writeFile(path, content, 0o644); err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			p.log.Warn("could not remove a checked zone candidate",
				"path", path, "error", err.Error())
		}
	}()
	return p.ValidateZone(ctx, zone, path)
}

// installZone writes a zone file and gives it to the name server.
//
// The journal is removed with it. named replays a journal over the file it
// finds, so a new file beside an old journal is served as the old contents
// plus whatever the journal held — which looks exactly like the panel's write
// having been ignored.
func (p *Provider) installZone(path, content string) error {
	for _, artifact := range zoneArtifacts(path)[1:] {
		if err := os.Remove(artifact); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove the stale %s: %w", filepath.Base(artifact), err)
		}
	}
	if err := writeFile(path, content, 0o644); err != nil {
		return err
	}
	return chownNamed(path)
}

// rollbackZones removes zone files written during a reconcile that then failed.
//
// They are removed rather than restored: the panel holds the whole zone, so the
// next reconcile writes it again, and a half-applied set of zones is worse than
// a missing one — the include file being rolled back means the server is not
// reading them anyway.
func (p *Provider) rollbackZones(names []string) {
	for _, name := range names {
		path := p.paths.ZoneFile(name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			p.log.Error("could not remove a zone file after a failed change",
				"zone", name, "path", path, "error", err.Error())
		}
	}
}

// ensureMainConf makes sure named.conf exists and includes the panel's zones.
//
// It returns the backup taken, whether the file was created, and whether an
// include line was added to an existing one.
func (p *Provider) ensureMainConf(settings Settings) (string, bool, bool, error) {
	path := p.paths.MainConf()

	current, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", false, false, fmt.Errorf("%w: %s could not be read: %s",
				ErrForeignConfig, path, err.Error())
		}
		// No named.conf at all. Alpine ships two samples and no live file, so
		// this is the ordinary case on a host that has never served DNS.
		if err := writeFile(path, RenderMainConf(p.paths, settings), 0o640); err != nil {
			return "", false, false, err
		}
		if err := chownNamed(path); err != nil {
			p.log.Warn("could not give named.conf to the name server's account",
				"error", err.Error())
		}
		return "", true, false, nil
	}

	if strings.Contains(string(current), confMarker) {
		// This host's named.conf is the panel's own, so the panel's settings
		// are what it should say. Rewriting it is how a change to listen-on
		// takes effect at all: written once and never again, that setting
		// would be a control that silently did nothing after the first zone.
		rendered := RenderMainConf(p.paths, settings)
		if string(current) == rendered {
			return "", false, false, nil
		}
		backup, err := backupFile(path)
		if err != nil {
			return "", false, false, err
		}
		if err := writeFile(path, rendered, 0o640); err != nil {
			return backup, false, false, err
		}
		// Given back to the name server's account, every time.
		//
		// writeFile installs through a temporary file and a rename, so the new
		// file is root's whatever the old one was. named runs as its own
		// account and reads this file at every reload: without this it reports
		// "reloading configuration failed: permission denied" and goes on
		// serving the configuration it already had — a change the panel
		// reported as applied and that never took effect.
		if err := chownNamed(path); err != nil {
			p.log.Warn("could not give named.conf to the name server's account",
				"error", err.Error())
		}
		return backup, false, false, nil
	}

	if HasInclude(string(current), p.paths) {
		return "", false, false, nil
	}

	backup, err := backupFile(path)
	if err != nil {
		return "", false, false, err
	}
	if err := writeFile(path, AddInclude(string(current), p.paths), 0o640); err != nil {
		return backup, false, false, err
	}
	if err := chownNamed(path); err != nil {
		p.log.Warn("could not give named.conf to the name server's account",
			"error", err.Error())
	}
	return backup, false, true, nil
}

// reload puts a change into service.
//
// rndc where there is one: `reconfig` picks up zones that were added or
// removed, and a per-zone `reload` re-reads the ones whose file changed. A
// blanket reload would re-read every zone on the host, which on a server with
// a few hundred of them is a stall nobody asked for.
//
// The return is (reloaded, warning). A server that is configured correctly and
// not running is not a failure — it is a true thing to tell an operator, and it
// is what a host looks like the moment before its first zone.
func (p *Provider) reload(ctx context.Context, changed []string) (bool, string) {
	if p.service == nil {
		return false, "this host has no service manager, so the name server was not started"
	}
	if !p.service.Running(ctx) {
		if err := p.service.Start(ctx); err != nil {
			return false, "the name server would not start: " + err.Error()
		}
		// A server that has just started has read everything.
		return p.service.Running(ctx), ""
	}

	if !p.runner.Available(CommandRndc) {
		if err := p.service.Start(ctx); err != nil {
			return false, "the name server could not be reloaded: " + err.Error()
		}
		return true, ""
	}

	if result, err := p.runner.Run(ctx, CommandRndc, "reconfig"); err != nil || !result.Succeeded() {
		return false, "the name server did not accept the new zone list: " +
			firstLine(errText(err), result.Stderr, result.Stdout)
	}
	for _, zone := range changed {
		result, err := p.runner.Run(ctx, CommandRndc, "reload", zone)
		if err != nil || !result.Succeeded() {
			return false, fmt.Sprintf("the zone %s was not reloaded: %s", zone,
				firstLine(errText(err), result.Stderr, result.Stdout))
		}
	}
	return true, ""
}

// errText renders an error for a message, or "" for nil.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// hasZone reports whether a zone is in a set.
func hasZone(zones []Zone, name string) bool {
	for _, zone := range zones {
		if zone.Name == name {
			return true
		}
	}
	return false
}

// fileHas reports whether a file already holds exactly this content.
func fileHas(path, content string) (bool, error) {
	current, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return string(current) == content, nil
}

// backupFile copies a file aside and returns where it went.
//
// An empty path means there was nothing to back up, and that distinction is
// what restoreFile needs: restoring "no file" is removing the one that was
// written, not failing to find a backup that never existed.
func backupFile(path string) (string, error) {
	current, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	backup := fmt.Sprintf("%s.bak.%s", path, timeNow().UTC().Format("20060102T150405Z"))
	if err := writeFile(backup, string(current), 0o640); err != nil {
		return "", fmt.Errorf("back up %s: %w", path, err)
	}
	return backup, nil
}

// restoreFile puts back what a backup holds, or removes the file when there was
// no backup.
//
// Errors are logged rather than returned: the caller is already failing with
// the error that explains what happened, and a rollback that itself fails is
// logged loudly because the host is then in a state nobody chose.
func restoreFile(path, backup string, log logger) {
	if backup == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Error("could not remove a rejected file", "path", path, "error", err.Error())
		}
		return
	}
	previous, err := os.ReadFile(backup)
	if err != nil {
		log.Error("could not read a backup", "path", backup, "error", err.Error())
		return
	}
	if err := writeFile(path, string(previous), 0o640); err != nil {
		log.Error("could not restore a file", "path", path, "error", err.Error())
	}
}

// logger is the little of *slog.Logger this file uses.
type logger interface {
	Error(msg string, args ...any)
	Warn(msg string, args ...any)
}

// writeFile installs a file atomically.
//
// Through a temporary file in the same directory and a rename, so named — or
// the validator, which reads the same path — never sees a half-written file. A
// rename within a directory is atomic; a write in place is a window in which
// the file is neither the old one nor the new one, and named reading that
// window is a zone that fails to load.
func writeFile(path, content string, mode os.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".jothost-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tempPath := temp.Name()
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
