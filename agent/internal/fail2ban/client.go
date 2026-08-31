package fail2ban

import (
	"context"
	"fmt"
	"strings"

	"github.com/jothost/panel/shared/validate"
)

// Talking to the daemon.
//
// fail2ban-client prints a pipe-drawn tree that was designed to be read by a
// person, and it is the only interface the daemon has. So it is parsed — but
// only the parts that are stable across versions, and every number the panel
// shows is asked for individually with `get` rather than scraped, because `get`
// returns one value on one line and cannot be misread.
//
// The tree is used for the two things `get` cannot answer: which jails exist,
// and which addresses are banned right now.

// Jails lists the jails the daemon is running.
//
// The output is:
//
//	Status
//	|- Number of jail:	2
//	`- Jail list:	sshd, nginx-http-auth
func (p *Provider) Jails(ctx context.Context) ([]string, error) {
	result, err := p.runner.Run(ctx, CommandName, "status")
	if err != nil {
		return nil, wrap("list the jails", err)
	}
	if !result.Succeeded() {
		return nil, fmt.Errorf("%w: %s", ErrNotRunning, firstLine(result.Stderr, result.Stdout))
	}

	for _, line := range strings.Split(result.Stdout, "\n") {
		_, list, found := strings.Cut(line, "Jail list:")
		if !found {
			continue
		}

		names := make([]string, 0, 4)
		for _, name := range strings.Split(list, ",") {
			trimmed := strings.TrimSpace(name)
			// A name that is not one is a line this parser misread, and acting
			// on it would mean passing it to the daemon as an argument.
			if trimmed == "" || validate.JailName(trimmed) != nil {
				continue
			}
			names = append(names, trimmed)
		}
		return names, nil
	}
	return []string{}, nil
}

// JailStatus reads one jail's counters and its banned addresses.
//
// The output is:
//
//	Status for the jail: sshd
//	|- Filter
//	|  |- Currently failed:	0
//	|  |- Total failed:	12
//	|  `- File list:	/var/log/messages
//	`- Actions
//	   |- Currently banned:	1
//	   |- Total banned:	1
//	   `- Banned IP list:	198.51.100.7
func (p *Provider) JailStatus(ctx context.Context, name string) (Jail, error) {
	if err := validate.JailName(name); err != nil {
		return Jail{}, err
	}

	result, err := p.runner.Run(ctx, CommandName, "status", name)
	if err != nil {
		return Jail{}, wrap("read the jail", err)
	}
	if !result.Succeeded() {
		return Jail{}, fmt.Errorf("%w: %s", ErrUnknownJail,
			firstLine(result.Stderr, result.Stdout))
	}

	jail := Jail{Name: name, Enabled: true, LogPaths: []string{}, Banned: []string{}}

	for _, line := range strings.Split(result.Stdout, "\n") {
		label, value, found := cutField(line)
		if !found {
			continue
		}

		switch label {
		case "Currently failed":
			jail.Failed = atoi(value)
		case "Total failed":
			jail.TotalFailed = atoi(value)
		case "Currently banned":
			jail.Currently = atoi(value)
		case "Total banned":
			jail.Total = atoi(value)
		case "File list":
			jail.LogPaths = fields(value)
		case "Banned IP list":
			// Validated on the way out as well as in: these become rows in the
			// panel and arguments to an unban, and a line this parser misread
			// must not become either.
			for _, address := range fields(value) {
				if validate.BanIP(address) == nil {
					jail.Banned = append(jail.Banned, address)
				}
			}
		}
	}

	return jail, nil
}

// Policy reads the numbers a jail is actually running with.
//
// Asked one at a time with `get`, which returns a bare value, rather than read
// from the configuration files — the whole point of this call is to find out
// whether what the panel wrote is what the daemon is using.
func (p *Provider) Policy(ctx context.Context, name string) (maxRetry, findTime, banTime int, err error) {
	if err := validate.JailName(name); err != nil {
		return 0, 0, 0, err
	}

	values := make([]int, 3)
	for i, option := range []string{"maxretry", "findtime", "bantime"} {
		result, runErr := p.runner.Run(ctx, CommandName, "get", name, option)
		if runErr != nil {
			return 0, 0, 0, wrap("read the jail policy", runErr)
		}
		if !result.Succeeded() {
			return 0, 0, 0, fmt.Errorf("%w: %s", ErrUnknownJail,
				firstLine(result.Stderr, result.Stdout))
		}
		values[i] = atoi(firstLine(result.Stdout))
	}
	return values[0], values[1], values[2], nil
}

// Ignored reads the addresses a jail never bans.
func (p *Provider) Ignored(ctx context.Context, name string) ([]string, error) {
	if err := validate.JailName(name); err != nil {
		return nil, err
	}

	result, err := p.runner.Run(ctx, CommandName, "get", name, "ignoreip")
	if err != nil {
		return nil, wrap("read the ignored addresses", err)
	}
	if !result.Succeeded() {
		return nil, fmt.Errorf("%w: %s", ErrUnknownJail, firstLine(result.Stderr, result.Stdout))
	}

	// The output is a tree again:
	//   These IP addresses/networks are ignored:
	//   |- 127.0.0.0/8
	//   `- ::1
	ignored := make([]string, 0, 4)
	for _, line := range strings.Split(result.Stdout, "\n") {
		trimmed := strings.TrimSpace(strings.TrimLeft(line, "|`- \t"))
		if trimmed == "" || strings.HasSuffix(trimmed, ":") {
			continue
		}
		if validate.BanIP(trimmed) == nil {
			ignored = append(ignored, trimmed)
		}
	}
	return ignored, nil
}

// Unban removes one address from one jail.
func (p *Provider) Unban(ctx context.Context, name, address string) error {
	if err := validate.JailName(name); err != nil {
		return err
	}
	// Validated before it becomes an argument. fail2ban would also accept a
	// host name here and resolve it, which would make what gets unbanned depend
	// on what DNS said at that moment.
	if err := validate.BanIP(address); err != nil {
		return err
	}

	result, err := p.runner.Run(ctx, CommandName, "set", name, "unbanip", address)
	if err != nil {
		return wrap("unban the address", err)
	}
	if !result.Succeeded() {
		message := firstLine(result.Stderr, result.Stdout)
		if strings.Contains(strings.ToLower(message), "not banned") {
			return fmt.Errorf("%w: %s in %s", ErrNotBanned, address, name)
		}
		return fmt.Errorf("unban %s: %s", address, message)
	}

	// fail2ban answers "0" when it removed nothing, which is a success exit
	// code for a call that did not do what was asked.
	if strings.TrimSpace(firstLine(result.Stdout)) == "0" {
		return fmt.Errorf("%w: %s in %s", ErrNotBanned, address, name)
	}
	return nil
}

// Ban adds one address to one jail.
//
// The panel offers this because an operator who has just read a log knows
// something fail2ban does not, and the alternative is a firewall rule they will
// forget to remove — a ban expires on its own.
func (p *Provider) Ban(ctx context.Context, name, address string) error {
	if err := validate.JailName(name); err != nil {
		return err
	}
	if err := validate.BanIP(address); err != nil {
		return err
	}

	result, err := p.runner.Run(ctx, CommandName, "set", name, "banip", address)
	if err != nil {
		return wrap("ban the address", err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("ban %s: %s", address, firstLine(result.Stderr, result.Stdout))
	}
	return nil
}

// Validate asks fail2ban whether it would accept the configuration on disk.
func (p *Provider) Validate(ctx context.Context) error {
	result, err := p.runner.Run(ctx, CommandName, "-t")
	if err != nil {
		return wrap("validate the configuration", err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, lastMeaningfulLine(result.Stderr, result.Stdout))
	}
	return nil
}

// Reload asks the daemon to re-read its configuration.
func (p *Provider) Reload(ctx context.Context) error {
	result, err := p.runner.Run(ctx, CommandName, "reload")
	if err != nil {
		return wrap("reload fail2ban", err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("reload fail2ban: %s", firstLine(result.Stderr, result.Stdout))
	}
	return nil
}

// cutField splits one line of fail2ban's tree into its label and value.
func cutField(line string) (label, value string, ok bool) {
	trimmed := strings.TrimLeft(line, "|`- \t")
	label, value, found := strings.Cut(trimmed, ":")
	if !found {
		return "", "", false
	}
	return strings.TrimSpace(label), strings.TrimSpace(value), true
}

// fields splits a whitespace- or comma-separated list.
func fields(value string) []string {
	out := make([]string, 0, 4)
	for _, part := range strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	}) {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// lastMeaningfulLine returns the last non-empty line, which is where
// fail2ban -t puts the reason a configuration was refused.
func lastMeaningfulLine(values ...string) string {
	last := ""
	for _, value := range values {
		for _, line := range strings.Split(value, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				last = trimmed
			}
		}
	}
	if last == "" {
		return "fail2ban did not say why"
	}
	return last
}
