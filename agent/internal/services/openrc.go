package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// OpenRC, the other init system a hosting panel meets.
//
// The specs name systemd, and systemd is what nearly every production host
// runs. But Alpine — which this panel's own Agent image is built on, and which
// plenty of small hosts run — has no systemd at all: it cannot be installed
// there, because it does not exist as a package. On such a host a panel that
// speaks only systemd can report what is running and change none of it.
//
// So there are two backends behind one set of verbs. Which one a host uses is a
// property of the host; nothing above this file chooses, and callers cannot
// tell them apart.
//
// The mapping is straightforward, because the two systems agree about what a
// service manager is for:
//
//	systemctl start nginx.service      rc-service nginx start
//	systemctl enable nginx.service     rc-update add nginx default
//	systemctl show nginx.service       rc-service nginx status
//
// Two differences matter. OpenRC service names carry no ".service" suffix, so
// the catalogue's unit names are stripped before use. And OpenRC has no
// daemon-reload: its init scripts are read on each invocation, so the reload
// that systemd needs after a unit file changes is a no-op here.

// Allowlist keys for the OpenRC tools.
const (
	CommandRCService = "rc-service"
	CommandRCUpdate  = "rc-update"
)

// The runlevel a service is enabled into.
//
// "default" is the one a booted Alpine host reaches and the one every service a
// panel manages belongs in. The others — sysinit, boot, shutdown — are for the
// machine's own startup, and a panel adding a web server to "boot" would be
// making a decision about init order it has no business making.
const openrcRunlevel = "default"

// openrcAvailable reports whether this host is managed by OpenRC.
func (p *Provider) openrcAvailable() bool {
	return p.runner != nil &&
		p.runner.Available(CommandRCService) &&
		p.runner.Available(CommandRCUpdate)
}

// openrcName turns a catalogue unit name into an OpenRC service name.
//
// The catalogue is written in systemd's vocabulary because that is what most
// hosts use; the suffix is simply dropped here. Where the two disagree about
// the name itself — Alpine's Apache script is "apache2" where the systemd unit
// is "httpd.service" — the catalogue already lists both spellings as
// candidates, and the resolver tries each.
func openrcName(unit string) string {
	return strings.TrimSuffix(unit, ".service")
}

// openrcStatus reads one service's state.
func (p *Provider) openrcStatus(ctx context.Context, unit string) (Status, error) {
	name := openrcName(unit)
	if err := ValidateName(name); err != nil {
		return Status{}, err
	}

	// A service with no init script is one this host does not have. Asking the
	// filesystem executes nothing and gives a clearer answer than parsing a
	// refusal.
	if _, err := os.Stat(filepath.Join(p.initDir(), name)); err != nil {
		if os.IsNotExist(err) {
			return Status{}, fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return Status{}, fmt.Errorf("stat the init script for %s: %w", name, err)
	}

	result, err := p.runner.Run(ctx, CommandRCService, name, "status")
	if err != nil {
		return Status{}, fmt.Errorf("query service: %w", err)
	}

	// rc-service reports the state in its output and in its exit code: 0
	// started, 3 stopped, 32 crashed. The output is read rather than the code,
	// because it distinguishes "starting" and "inactive" too — and a service
	// that is still starting is neither up nor down.
	status := Status{
		Name:      name,
		LoadState: "loaded",
		SubState:  openrcState(result.Stdout + result.Stderr),
	}

	switch status.SubState {
	case "started":
		status.ActiveState = "active"
		status.SubState = "running"
		status.Running = true
	case "starting":
		status.ActiveState = "activating"
	case "crashed":
		// OpenRC's word for "it was started and its process is gone", which is
		// systemd's "failed" — and the distinction an operator acts on.
		status.ActiveState = "failed"
	case "inactive":
		// OpenRC's "inactive" is a service that was started and did not finish
		// starting, which is not the same as one that was never started.
		status.ActiveState = "inactive"
	case "stopped":
		status.ActiveState = "inactive"
		status.SubState = "dead"
	default:
		status.ActiveState = "inactive"
		status.SubState = "dead"
	}

	enabled := p.openrcEnabled(name)
	status.Enabled = &enabled

	// OpenRC does not report a main pid, and inventing one from the process
	// table here would be guessing. Detection reads the process table itself
	// for exactly that, and leaves this at zero.
	return status, nil
}

// openrcState pulls the state word out of rc-service's output.
//
// The line is " * status: started". It is read rather than the exit code
// because the code collapses states the panel shows separately.
func openrcState(output string) string {
	for _, line := range strings.Split(output, "\n") {
		_, state, found := strings.Cut(strings.TrimSpace(line), "status:")
		if !found {
			continue
		}
		return strings.ToLower(strings.TrimSpace(state))
	}
	return ""
}

// openrcEnabled reports whether a service starts at boot.
//
// A symlink in the runlevel directory is what "enabled" means to OpenRC, so
// this reads the directory rather than running rc-update: it is the same
// answer, and it executes nothing.
func (p *Provider) openrcEnabled(name string) bool {
	_, err := os.Lstat(filepath.Join(p.runlevelDir(), openrcRunlevel, name))
	return err == nil
}

// openrcControl runs one verb against one service.
func (p *Provider) openrcControl(ctx context.Context, verb, unit string) error {
	name := openrcName(unit)
	if err := ValidateName(name); err != nil {
		return err
	}

	// The verb goes after the service name: "rc-service nginx start".
	result, err := p.runner.Run(ctx, CommandRCService, name, verb)
	if err != nil {
		return fmt.Errorf("%s %s: %w", verb, name, err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("%s %s: %s", verb, name, openrcError(result.Stdout+result.Stderr))
	}

	// OpenRC reports some refusals on stdout and still exits zero — a success
	// that changed nothing, which is the worst answer to give a caller.
	if combined := result.Stdout + result.Stderr; strings.Contains(combined, "ERROR:") {
		return fmt.Errorf("%s %s: %s", verb, name, openrcError(combined))
	}
	return nil
}

// openrcSetBoot adds or removes a service from the boot runlevel.
func (p *Provider) openrcSetBoot(ctx context.Context, unit string, enabled bool) error {
	name := openrcName(unit)
	if err := ValidateName(name); err != nil {
		return err
	}

	verb := "del"
	if enabled {
		verb = "add"
	}

	result, err := p.runner.Run(ctx, CommandRCUpdate, verb, name, openrcRunlevel)
	if err != nil {
		return fmt.Errorf("rc-update %s %s: %w", verb, name, err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("rc-update %s %s: %s", verb, name,
			openrcError(result.Stdout+result.Stderr))
	}
	return nil
}

// openrcError trims OpenRC's output to the part worth showing.
//
// Its messages are decorated with a leading " * " and colour escapes; what is
// left after those is a sentence written for a person.
func openrcError(output string) string {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimPrefix(trimmed, "*")
		trimmed = strings.TrimSpace(trimmed)
		if strings.HasPrefix(trimmed, "ERROR:") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "ERROR:"))
		}
	}

	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "*"))
		if trimmed != "" {
			return trimmed
		}
	}
	return "OpenRC refused the operation"
}

// initDir is where OpenRC keeps its init scripts.
func (p *Provider) initDir() string {
	if p.openrcInitDir != "" {
		return p.openrcInitDir
	}
	return "/etc/init.d"
}

// runlevelDir is where OpenRC records what starts at boot.
func (p *Provider) runlevelDir() string {
	if p.openrcRunlevelDir != "" {
		return p.openrcRunlevelDir
	}
	return "/etc/runlevels"
}
