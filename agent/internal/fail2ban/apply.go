package fail2ban

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jothost/panel/shared/validate"
)

// Changing the configuration.
//
// The sequence is the one Phases 16 and 17 established, for the same reason:
//
//	1. back up what is there
//	2. render the new drop-in
//	3. ask fail2ban to validate it — `fail2ban-client -t` reads the whole
//	   configuration, so a value that conflicts with the distribution's is
//	   caught here rather than at the next restart
//	4. install it atomically
//	5. reload the daemon
//	6. read the policy back *from the daemon* and check it took
//	7. restore the backup on any failure, and reload again
//
// Step 6 is not defensive programming. On Alpine it fails without it: the
// distribution ships a jail.d file setting maxretry, and until the panel's file
// was named so that fail2ban reads it last, every change was written correctly,
// validated successfully, and ignored.

// timeNow is a variable so tests can be deterministic.
var timeNow = time.Now

// Change is what a caller wants a jail to become.
type Change struct {
	// Jail is a name from the catalogue.
	Jail string
	// Enabled turns the jail on or off. Nil leaves it as it is.
	Enabled *bool
	// The policy. Nil fields are left as they are, so changing the ban time
	// does not silently reset the threshold.
	MaxRetry *int
	FindTime *int
	BanTime  *int
}

// Settings are the host-wide values the panel writes.
type Settings struct {
	// Ignored are the addresses no jail may ban. Loopback is always in it.
	Ignored []string
}

// ApplyResult reports what a change did.
type ApplyResult struct {
	Jail string `json:"jail"`
	// Policy is what the daemon is running afterwards, read back rather than
	// assumed from the request.
	Policy  Policy `json:"policy"`
	Enabled bool   `json:"enabled"`
	Backup  string `json:"backup,omitempty"`
	// Reloaded reports whether the running daemon picked the change up.
	Reloaded bool `json:"reloaded"`
}

// state is the panel's drop-in as a structure.
type state struct {
	// jails maps a jail name to the options the panel set for it.
	jails map[string]map[string]string
	// ignored is the host-wide ignore list.
	ignored []string
}

// Apply changes one jail.
func (p *Provider) Apply(ctx context.Context, change Change) (ApplyResult, error) {
	if !p.Available() {
		return ApplyResult{}, ErrUnavailable
	}

	definition, err := Lookup(change.Jail)
	if err != nil {
		return ApplyResult{}, err
	}

	current := p.readState()
	options := current.jails[definition.Name]
	if options == nil {
		options = map[string]string{}
	}

	// A jail switched on for the first time gets the catalogue's defaults, so
	// that turning something on never writes a half-configured jail.
	policy := Policy{
		MaxRetry: valueOr(options["maxretry"], definition.Defaults.MaxRetry),
		FindTime: valueOr(options["findtime"], definition.Defaults.FindTime),
		BanTime:  valueOr(options["bantime"], definition.Defaults.BanTime),
	}
	if change.MaxRetry != nil {
		policy.MaxRetry = *change.MaxRetry
	}
	if change.FindTime != nil {
		policy.FindTime = *change.FindTime
	}
	if change.BanTime != nil {
		policy.BanTime = *change.BanTime
	}
	if err := policy.Validate(); err != nil {
		return ApplyResult{}, err
	}

	enabled := options["enabled"] == "true"
	if change.Enabled != nil {
		enabled = *change.Enabled
	}

	// A jail whose log does not exist is refused rather than written. fail2ban
	// reports it as a failed jail at start-up and carries on, so the panel would
	// show a jail that is enabled and watching nothing.
	logPath := p.logPathFor(definition)
	if enabled && logPath == "" {
		return ApplyResult{}, fmt.Errorf(
			"%w: none of the logs this jail watches exist on this host (%s)",
			ErrInvalidConfig, strings.Join(definition.LogPaths, ", "))
	}

	options["enabled"] = boolText(enabled)
	options["maxretry"] = strconv.Itoa(policy.MaxRetry)
	options["findtime"] = strconv.Itoa(policy.FindTime)
	options["bantime"] = strconv.Itoa(policy.BanTime)
	if definition.SuppliesLogPath && logPath != "" {
		options["logpath"] = logPath
	}
	current.jails[definition.Name] = options

	backup, err := p.write(ctx, current)
	if err != nil {
		return ApplyResult{}, err
	}

	result := ApplyResult{Jail: definition.Name, Enabled: enabled, Policy: policy}
	result.Backup = backup

	if p.Running(ctx) {
		if err := p.Reload(ctx); err != nil {
			p.log.Warn("fail2ban did not reload after a configuration change",
				"jail", definition.Name, "error", err.Error())
		} else {
			result.Reloaded = true
		}
	}

	// The check the rest of this exists for. Only for a jail that should now be
	// running: a disabled jail has no policy to read back.
	if enabled && result.Reloaded {
		maxRetry, findTime, banTime, err := p.Policy(ctx, definition.Name)
		if err != nil {
			return result, nil
		}
		got := Policy{MaxRetry: maxRetry, FindTime: findTime, BanTime: banTime}
		if got != policy {
			// restoreFrom, with the value write() returned: an empty string
			// means there was no file before, and the way back is to remove the
			// one this call wrote. Passing the *path* instead would try to read
			// a backup that was never taken, fail, and leave the rejected
			// configuration in place — a rollback that does not roll back.
			p.restoreFrom(backup)
			if err := p.Reload(ctx); err != nil {
				p.log.Error("the configuration was restored but fail2ban did not reload",
					"error", err.Error())
			}
			return ApplyResult{}, fmt.Errorf(
				"%w: it is running %d failures in %ds banned for %ds, not %d in %ds for %ds — "+
					"something else on this host configures this jail as well, and the panel "+
					"has put back what was there before",
				ErrNotApplied, got.MaxRetry, got.FindTime, got.BanTime,
				policy.MaxRetry, policy.FindTime, policy.BanTime)
		}
		result.Policy = got
	}

	return result, nil
}

// SetIgnored changes the addresses no jail may ban.
func (p *Provider) SetIgnored(ctx context.Context, addresses []string) ([]string, error) {
	if !p.Available() {
		return nil, ErrUnavailable
	}

	// Loopback is added if it is missing, because a host that has banned its
	// own loopback has broken every local service that talks to another over
	// it — while the panel goes on reporting success.
	normalised, err := validate.IgnoredAddresses(addresses)
	if err != nil {
		return nil, err
	}

	current := p.readState()
	current.ignored = normalised

	if _, err := p.write(ctx, current); err != nil {
		return nil, err
	}
	if p.Running(ctx) {
		if err := p.Reload(ctx); err != nil {
			p.log.Warn("fail2ban did not reload after the ignore list changed",
				"error", err.Error())
		}
	}
	return normalised, nil
}

// write installs a state, validating it before and restoring on failure.
//
// It returns where the previous file was kept, or an empty string when there
// was none — which the caller needs in order to roll back correctly.
func (p *Provider) write(ctx context.Context, desired state) (string, error) {
	backup, err := p.backup()
	if err != nil {
		return "", err
	}

	if err := p.install(render(desired)); err != nil {
		return backup, err
	}

	if err := p.Validate(ctx); err != nil {
		// The configuration on disk is the one fail2ban just refused, so it is
		// put back before anything reloads it.
		p.restoreFrom(backup)
		return backup, err
	}
	return backup, nil
}

// render writes the drop-in's contents.
func render(desired state) string {
	var b strings.Builder
	b.WriteString("# Written by JotHost Panel. This file is rewritten on every change.\n")
	b.WriteString("#\n")
	b.WriteString("# The name matters. fail2ban reads jail.conf, then jail.d/*.conf, then\n")
	b.WriteString("# the .local files — and the last value of an option wins. A file named\n")
	b.WriteString("# 10-jothost.conf would be read before the distribution's own drop-in and\n")
	b.WriteString("# silently lose to it.\n")
	b.WriteString("#\n")
	b.WriteString("# What is here is policy: which jails run, how many failures are allowed,\n")
	b.WriteString("# and for how long. Filters and log formats belong to the distribution.\n")
	b.WriteString("#\n")
	b.WriteString("# Written ")
	b.WriteString(timeNow().UTC().Format(time.RFC3339))
	b.WriteString("\n")

	if len(desired.ignored) > 0 {
		b.WriteString("\n[DEFAULT]\n")
		b.WriteString("ignoreip = ")
		b.WriteString(strings.Join(desired.ignored, " "))
		b.WriteString("\n")
	}

	names := make([]string, 0, len(desired.jails))
	for name := range desired.jails {
		names = append(names, name)
	}
	// Sorted so two versions of this file differ where the settings differ,
	// rather than where a map happened to iterate.
	sort.Strings(names)

	for _, name := range names {
		options := desired.jails[name]
		if len(options) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n[%s]\n", name)
		for _, key := range orderedOptions {
			if value, set := options[key]; set {
				fmt.Fprintf(&b, "%s = %s\n", key, value)
			}
		}
	}
	return b.String()
}

// orderedOptions keeps the file stable between writes.
var orderedOptions = []string{"enabled", "logpath", "maxretry", "findtime", "bantime"}

// managedOptions are the ones the panel writes. Anything else found in its own
// file is dropped by a rewrite, so a rewrite only ever contains these.
var managedOptions = map[string]bool{
	"enabled": true, "logpath": true, "maxretry": true, "findtime": true, "bantime": true,
}

// readState reads back what the panel wrote last time.
//
// Only the panel's own file: everything else on the host belongs to the
// distribution or the operator, and a rewrite must not absorb it.
func (p *Provider) readState() state {
	current := state{jails: map[string]map[string]string{}}

	content, err := os.ReadFile(p.DropInPath()) //nolint:gosec // a path this package owns
	if err != nil {
		return current
	}

	section := ""
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}

		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			continue
		}

		key, value, found := strings.Cut(trimmed, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		if section == "DEFAULT" {
			if key == "ignoreip" {
				current.ignored = fields(value)
			}
			continue
		}
		if section == "" || validate.JailName(section) != nil || !managedOptions[key] {
			continue
		}
		if current.jails[section] == nil {
			current.jails[section] = map[string]string{}
		}
		current.jails[section][key] = value
	}
	return current
}

// install writes the drop-in atomically.
func (p *Provider) install(rendered string) error {
	dir := filepath.Dir(p.DropInPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create the drop-in directory: %w", err)
	}

	temp, err := os.CreateTemp(dir, ".jothost-fail2ban-*")
	if err != nil {
		return fmt.Errorf("create a temporary configuration: %w", err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()

	if _, err := temp.WriteString(rendered); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write the configuration: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close the configuration: %w", err)
	}
	// 0644: fail2ban reads it as root, and it holds no secret — but it does say
	// which addresses are never banned, so it is not writable by anybody else.
	if err := os.Chmod(tempPath, 0o644); err != nil {
		return fmt.Errorf("set the configuration's mode: %w", err)
	}
	if err := os.Rename(tempPath, p.DropInPath()); err != nil {
		return fmt.Errorf("install the configuration: %w", err)
	}
	return nil
}

func (p *Provider) backupPath() string { return p.DropInPath() + ".backup" }

// backup keeps a copy before the file is replaced.
func (p *Provider) backup() (string, error) {
	content, err := os.ReadFile(p.DropInPath()) //nolint:gosec // a path this package owns
	if err != nil {
		if os.IsNotExist(err) {
			// Nothing to back up: the panel has not written here before.
			return "", nil
		}
		return "", fmt.Errorf("read the current configuration: %w", err)
	}
	if err := os.WriteFile(p.backupPath(), content, 0o644); err != nil {
		return "", fmt.Errorf("back up the configuration: %w", err)
	}
	return p.backupPath(), nil
}

// restoreFrom puts a backup back, or removes what was written when there was
// nothing there before.
func (p *Provider) restoreFrom(backup string) {
	if backup == "" {
		// There was nothing there before, so the way back is to remove what
		// this call wrote.
		if err := os.Remove(p.DropInPath()); err != nil && !os.IsNotExist(err) {
			p.log.Error("the fail2ban drop-in could not be removed during a rollback",
				"path", p.DropInPath(), "error", err.Error())
		}
		return
	}

	content, err := os.ReadFile(backup) //nolint:gosec // a path this package wrote
	if err != nil {
		p.log.Error("the fail2ban configuration backup could not be read",
			"path", backup, "error", err.Error())
		return
	}
	if err := p.install(string(content)); err != nil {
		p.log.Error("the fail2ban configuration could not be restored",
			"path", p.DropInPath(), "error", err.Error())
	}
}

func valueOr(text string, fallback int) int {
	if value := atoi(text); value > 0 {
		return value
	}
	return fallback
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
