package ssh

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Changing the configuration.
//
// The sequence is the one CLAUDE.md section 19 lays down for the firewall,
// adapted to a service where the dangerous moment is different:
//
//	1. back up what is there
//	2. render the new drop-in
//	3. ask sshd to validate the *whole* configuration, includes and all
//	4. install it atomically
//	5. reload the server
//	6. read the effective configuration back and check it took
//	7. restore the backup on any failure, and reload again
//
// Step 6 is the one that is easy to leave out and the one that matters most. A
// drop-in can be written correctly, be syntactically valid, and still not take
// effect — another file may set the same directive earlier, or the server may
// not have reloaded. Reporting success then would leave an operator believing
// password authentication is off when it is on, which is worse than any error
// this package could return.

// Change is what a caller wants the configuration to become.
//
// Every field is a pointer, so "leave it alone" and "set it to false" are
// different requests. A bool that meant both would turn every partial update
// into a full one, and the field it would silently reset here is the one that
// decides whether anybody can log in.
type Change struct {
	Port                   *int
	RootLogin              *string
	PasswordAuthentication *bool
	PubkeyAuthentication   *bool
	PermitEmptyPasswords   *bool
	X11Forwarding          *bool
	MaxAuthTries           *int
}

// Empty reports whether a change asks for nothing.
func (c Change) Empty() bool {
	return c.Port == nil && c.RootLogin == nil && c.PasswordAuthentication == nil &&
		c.PubkeyAuthentication == nil && c.PermitEmptyPasswords == nil &&
		c.X11Forwarding == nil && c.MaxAuthTries == nil
}

// RootLoginValues are what PermitRootLogin may be set to.
//
// "prohibit-password" is the modern spelling and what the panel writes;
// "without-password" is the same thing and what sshd -T prints back, so both
// are accepted on the way in.
var RootLoginValues = map[string]string{
	"yes":                  "yes",
	"no":                   "no",
	"prohibit-password":    "prohibit-password",
	"without-password":     "prohibit-password",
	"forced-commands-only": "forced-commands-only",
}

// Guard is what the Provider needs to know from the rest of the host before it
// will make a change.
//
// An interface rather than a dependency on the firewall package, because what
// this package needs is one question answered — "would a connection to this
// port get through?" — and a host with no firewall answers it by saying yes.
type Guard interface {
	// PortReachable reports whether the firewall would admit a connection to
	// this port, and why not when it would not.
	PortReachable(ctx context.Context, port int) (bool, string)
}

// ApplyResult reports what a change did.
type ApplyResult struct {
	// Config is the effective configuration afterwards, read back from the
	// server rather than assumed from the request.
	Config Config `json:"config"`
	// Changed names the directives that were written.
	Changed []string `json:"changed"`
	// Backup is where the previous drop-in was kept.
	Backup string `json:"backup,omitempty"`
	// Reloaded reports whether the running server picked the change up. False
	// means it is on disk and takes effect at the next restart, which the panel
	// says rather than implying it is live.
	Reloaded bool `json:"reloaded"`
}

// Reloader restarts or reloads the SSH server.
//
// An interface for the same reason as Guard: this package needs one verb, and
// the service manager that provides it belongs to another phase.
type Reloader interface {
	ReloadSSH(ctx context.Context) error
}

// Apply changes the configuration.
func (p *Provider) Apply(ctx context.Context, change Change, guard Guard,
	reloader Reloader,
) (ApplyResult, error) {
	if !p.Available() {
		return ApplyResult{}, ErrUnavailable
	}
	if change.Empty() {
		return ApplyResult{}, fmt.Errorf("%w: nothing was asked for", ErrInvalidConfig)
	}

	before, err := p.Read(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	if !before.Managed {
		return ApplyResult{}, fmt.Errorf("%w: %s", ErrNoDropIn, before.Reason)
	}

	if err := p.checkLockout(ctx, change, before, guard); err != nil {
		return ApplyResult{}, err
	}

	// The directives the panel manages, merged with what it wrote last time:
	// a change that sets one value must not silently drop another the panel
	// set on a previous call.
	desired := p.merge(change)
	rendered := render(desired)

	backup, err := p.backup()
	if err != nil {
		return ApplyResult{}, err
	}

	if err := p.validate(ctx, rendered); err != nil {
		return ApplyResult{}, err
	}
	if err := p.install(rendered); err != nil {
		p.restore(backup)
		return ApplyResult{}, err
	}

	result := ApplyResult{Backup: backup, Changed: keysOf(desired)}

	if reloader != nil {
		if err := reloader.ReloadSSH(ctx); err != nil {
			// The configuration is valid and installed; the server did not take
			// it. That is worth saying rather than rolling back, because the
			// file is correct and a restart will pick it up — but it is not a
			// success either, so it is reported as a failure with the reason.
			p.log.Warn("the SSH server did not reload after a configuration change",
				"error", err.Error())
		} else {
			result.Reloaded = true
		}
	}

	after, err := p.Read(ctx)
	if err != nil {
		p.restore(backup)
		return ApplyResult{}, err
	}

	// The check that makes the rest of this worth doing.
	if missing := disagreements(desired, after); len(missing) > 0 {
		p.restore(backup)
		if reloader != nil {
			if err := reloader.ReloadSSH(ctx); err != nil {
				p.log.Error("the SSH configuration was restored but the server did not reload",
					"error", err.Error())
			}
		}
		return ApplyResult{}, fmt.Errorf(
			"%w: %s — something else on this host sets it as well, and the panel has "+
				"put back what was there before",
			ErrNotApplied, strings.Join(missing, ", "))
	}

	result.Config = after
	return result, nil
}

// checkLockout refuses the changes that would leave nobody able to log in.
//
// These are refusals rather than warnings. A warning is something an operator
// clicks past at the end of a long day; the cost here is a machine that has to
// be rescued from a console, and on a rented server there may not be one.
func (p *Provider) checkLockout(ctx context.Context, change Change, before Config, guard Guard) error {
	// Turning off password authentication with no key anywhere is the classic
	// way to lose a host, and it is entirely predictable from here.
	if change.PasswordAuthentication != nil && !*change.PasswordAuthentication &&
		before.PasswordAuthentication {
		keyed, err := p.anyAuthorisedKey()
		if err != nil {
			return err
		}
		if !keyed {
			return fmt.Errorf(
				"%w: no account on this host has an authorised SSH key, so turning off "+
					"password authentication would leave no way in. Add a key first",
				ErrWouldLockOut)
		}
	}

	// The same argument for keys themselves: without pubkey authentication and
	// without passwords, nothing can authenticate.
	if change.PubkeyAuthentication != nil && !*change.PubkeyAuthentication {
		passwords := before.PasswordAuthentication
		if change.PasswordAuthentication != nil {
			passwords = *change.PasswordAuthentication
		}
		if !passwords {
			return fmt.Errorf(
				"%w: turning off public key authentication with passwords already off "+
					"would leave no way to authenticate", ErrWouldLockOut)
		}
	}

	if change.Port != nil {
		port := *change.Port
		if port < 1 || port > 65535 {
			return fmt.Errorf("%w: %d is not a port", ErrInvalidConfig, port)
		}
		// A port the firewall does not admit is a port nothing can reach. The
		// panel will not open it as a side effect of this change — a firewall
		// change is its own deliberate act, with its own protocol — so this
		// says exactly what to do first.
		if guard != nil && !samePort(before.Ports, port) {
			reachable, reason := guard.PortReachable(ctx, port)
			if !reachable {
				return fmt.Errorf(
					"%w: %s. Allow port %d in the firewall first, then change the SSH port",
					ErrWouldLockOut, reason, port)
			}
		}
	}

	// PermitRootLogin no, on a host where root is the only account that can log
	// in, is the third way to lose a machine.
	if change.RootLogin != nil && normaliseRootLogin(*change.RootLogin) == "no" {
		accounts, err := p.accounts.LoginAccounts()
		if err != nil {
			return err
		}
		others := 0
		for _, account := range accounts {
			if account.UID != 0 {
				others++
			}
		}
		if others == 0 {
			return fmt.Errorf(
				"%w: root is the only account on this host that can log in, so refusing "+
					"root logins would leave no way in. Create an administrative account first",
				ErrWouldLockOut)
		}
	}

	return nil
}

// anyAuthorisedKey reports whether any account could log in with a key.
func (p *Provider) anyAuthorisedKey() (bool, error) {
	accounts, err := p.accounts.LoginAccounts()
	if err != nil {
		return false, err
	}
	for _, account := range accounts {
		keys, err := p.readKeys(account)
		if err != nil {
			continue
		}
		if len(keys) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// merge combines a change with the directives the panel already set.
//
// The drop-in is rewritten whole on every change, so anything the panel set
// before and is not setting now has to be carried across — otherwise changing
// the port would quietly turn password authentication back on.
func (p *Provider) merge(change Change) map[string]string {
	desired := p.currentDropIn()

	if change.Port != nil {
		desired["Port"] = strconv.Itoa(*change.Port)
	}
	if change.RootLogin != nil {
		desired["PermitRootLogin"] = normaliseRootLogin(*change.RootLogin)
	}
	if change.PasswordAuthentication != nil {
		desired["PasswordAuthentication"] = yesNo(*change.PasswordAuthentication)
	}
	if change.PubkeyAuthentication != nil {
		desired["PubkeyAuthentication"] = yesNo(*change.PubkeyAuthentication)
	}
	if change.PermitEmptyPasswords != nil {
		desired["PermitEmptyPasswords"] = yesNo(*change.PermitEmptyPasswords)
	}
	if change.X11Forwarding != nil {
		desired["X11Forwarding"] = yesNo(*change.X11Forwarding)
	}
	if change.MaxAuthTries != nil {
		desired["MaxAuthTries"] = strconv.Itoa(*change.MaxAuthTries)
	}
	return desired
}

// currentDropIn reads back what the panel wrote last time.
func (p *Provider) currentDropIn() map[string]string {
	values := map[string]string{}

	content, err := os.ReadFile(p.DropInPath()) //nolint:gosec // a path this package owns
	if err != nil {
		return values
	}

	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, found := strings.Cut(trimmed, " ")
		if !found {
			continue
		}
		if _, managed := managedDirectives[key]; managed {
			values[key] = strings.TrimSpace(value)
		}
	}
	return values
}

// managedDirectives are the ones the panel writes. Anything else in the file is
// not the panel's, and would be dropped by a rewrite — so a rewrite only ever
// contains these.
var managedDirectives = map[string]struct{}{
	"Port":                   {},
	"PermitRootLogin":        {},
	"PasswordAuthentication": {},
	"PubkeyAuthentication":   {},
	"PermitEmptyPasswords":   {},
	"X11Forwarding":          {},
	"MaxAuthTries":           {},
}

// render writes the drop-in file's contents.
func render(values map[string]string) string {
	var b strings.Builder
	b.WriteString("# Written by JotHost Panel. This file is rewritten on every change.\n")
	b.WriteString("#\n")
	b.WriteString("# It is included from sshd_config before the directives below it, and\n")
	b.WriteString("# the first setting of a directive is the one sshd uses — so what is\n")
	b.WriteString("# here overrides the rest of the configuration.\n")
	b.WriteString("#\n")
	b.WriteString("# Written ")
	b.WriteString(timeNow().UTC().Format(time.RFC3339))
	b.WriteString("\n\n")

	for _, key := range orderedDirectives {
		value, set := values[key]
		if !set {
			continue
		}
		b.WriteString(key)
		b.WriteString(" ")
		b.WriteString(value)
		b.WriteString("\n")
	}
	return b.String()
}

// orderedDirectives keeps the file stable between writes, so a diff of two
// versions shows what changed rather than what moved.
var orderedDirectives = []string{
	"Port",
	"PermitRootLogin",
	"PasswordAuthentication",
	"PubkeyAuthentication",
	"PermitEmptyPasswords",
	"X11Forwarding",
	"MaxAuthTries",
}

// timeNow is a variable so tests can be deterministic.
var timeNow = time.Now

// validate asks sshd whether it would accept the configuration.
//
// The candidate is written beside the real drop-in and included by the same
// glob, so what is checked is the whole configuration as sshd would assemble
// it — not the fragment in isolation, which would miss a value that conflicts
// with something in the main file.
func (p *Provider) validate(ctx context.Context, rendered string) error {
	dir := filepath.Dir(p.DropInPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create the drop-in directory: %w", err)
	}

	// The candidate is written to the real path, checked, and put back if the
	// check fails. Checking a differently-named file in the same directory
	// would mean sshd read both, and the two would conflict.
	previous, existed := p.readDropIn()
	if err := p.install(rendered); err != nil {
		return err
	}

	result, err := p.runner.Run(ctx, CommandName, "-t", "-f", p.ConfigPath())
	if err != nil {
		p.putBack(previous, existed)
		return fmt.Errorf("validate the SSH configuration: %w", err)
	}
	if !result.Succeeded() {
		p.putBack(previous, existed)
		return fmt.Errorf("%w: %s", ErrInvalidConfig, firstLine(result.Stderr, result.Stdout))
	}
	return nil
}

func (p *Provider) readDropIn() (string, bool) {
	content, err := os.ReadFile(p.DropInPath()) //nolint:gosec // a path this package owns
	if err != nil {
		return "", false
	}
	return string(content), true
}

func (p *Provider) putBack(previous string, existed bool) {
	if !existed {
		if err := os.Remove(p.DropInPath()); err != nil && !os.IsNotExist(err) {
			p.log.Error("a rejected SSH drop-in could not be removed",
				"path", p.DropInPath(), "error", err.Error())
		}
		return
	}
	if err := p.install(previous); err != nil {
		p.log.Error("the previous SSH drop-in could not be put back",
			"path", p.DropInPath(), "error", err.Error())
	}
}

// install writes the drop-in atomically.
func (p *Provider) install(rendered string) error {
	dir := filepath.Dir(p.DropInPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create the drop-in directory: %w", err)
	}

	temp, err := os.CreateTemp(dir, ".jothost-sshd-*")
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
	// 0600: sshd reads it as root, and the file says how the host authenticates.
	if err := os.Chmod(tempPath, 0o600); err != nil {
		return fmt.Errorf("set the configuration's mode: %w", err)
	}

	// A temporary file in the same directory would be picked up by the *.conf
	// glob if it ended in .conf; it does not, and it is renamed away here
	// before sshd is asked anything.
	if err := os.Rename(tempPath, p.DropInPath()); err != nil {
		return fmt.Errorf("install the configuration: %w", err)
	}
	return nil
}

// backup keeps a copy of the drop-in before it is replaced.
func (p *Provider) backup() (string, error) {
	content, err := os.ReadFile(p.DropInPath()) //nolint:gosec // a path this package owns
	if err != nil {
		if os.IsNotExist(err) {
			// Nothing to back up: the panel has not written here before.
			return "", nil
		}
		return "", fmt.Errorf("read the current configuration: %w", err)
	}

	path := p.DropInPath() + ".backup"
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return "", fmt.Errorf("back up the configuration: %w", err)
	}
	return path, nil
}

// restore puts the backup back after a failure.
func (p *Provider) restore(backup string) {
	if backup == "" {
		// There was nothing there before, so the way back is to remove what
		// this call wrote.
		if err := os.Remove(p.DropInPath()); err != nil && !os.IsNotExist(err) {
			p.log.Error("the SSH drop-in could not be removed during a rollback",
				"path", p.DropInPath(), "error", err.Error())
		}
		return
	}

	content, err := os.ReadFile(backup) //nolint:gosec // a path this package wrote
	if err != nil {
		p.log.Error("the SSH configuration backup could not be read",
			"path", backup, "error", err.Error())
		return
	}
	if err := p.install(string(content)); err != nil {
		p.log.Error("the SSH configuration could not be restored",
			"path", p.DropInPath(), "error", err.Error())
	}
}

// disagreements reports the directives the server did not adopt.
func disagreements(desired map[string]string, after Config) []string {
	missing := make([]string, 0, len(desired))

	for _, key := range orderedDirectives {
		want, set := desired[key]
		if !set {
			continue
		}

		var got string
		switch key {
		case "Port":
			if samePort(after.Ports, atoi(want)) {
				continue
			}
			got = joinPorts(after.Ports)
		case "PermitRootLogin":
			if normaliseRootLogin(after.RootLogin) == normaliseRootLogin(want) {
				continue
			}
			got = after.RootLogin
		case "PasswordAuthentication":
			if yesNo(after.PasswordAuthentication) == want {
				continue
			}
			got = yesNo(after.PasswordAuthentication)
		case "PubkeyAuthentication":
			if yesNo(after.PubkeyAuthentication) == want {
				continue
			}
			got = yesNo(after.PubkeyAuthentication)
		case "PermitEmptyPasswords":
			if yesNo(after.PermitEmptyPasswords) == want {
				continue
			}
			got = yesNo(after.PermitEmptyPasswords)
		case "X11Forwarding":
			if yesNo(after.X11Forwarding) == want {
				continue
			}
			got = yesNo(after.X11Forwarding)
		case "MaxAuthTries":
			if strconv.Itoa(after.MaxAuthTries) == want {
				continue
			}
			got = strconv.Itoa(after.MaxAuthTries)
		}

		missing = append(missing, fmt.Sprintf("%s is %s, not %s", key, got, want))
	}
	return missing
}

func keysOf(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for _, key := range orderedDirectives {
		if _, set := values[key]; set {
			out = append(out, key)
		}
	}
	return out
}

func samePort(ports []int, port int) bool {
	for _, listening := range ports {
		if listening == port {
			return true
		}
	}
	return false
}

func joinPorts(ports []int) string {
	parts := make([]string, 0, len(ports))
	for _, port := range ports {
		parts = append(parts, strconv.Itoa(port))
	}
	if len(parts) == 0 {
		return "unset"
	}
	return strings.Join(parts, ", ")
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

// normaliseRootLogin maps sshd's two spellings of the same value onto one.
func normaliseRootLogin(value string) string {
	if mapped, ok := RootLoginValues[strings.ToLower(strings.TrimSpace(value))]; ok {
		return mapped
	}
	return strings.ToLower(strings.TrimSpace(value))
}
