package services

import (
	"context"
	"sort"
)

// Detection answers two questions that are usually conflated: what this host
// has, and what is currently up.
//
// They have different sources on purpose. Whether a service is *installed* is a
// filesystem probe that executes nothing. Whether it is *running* comes from
// the process table, which is always readable — so a host with no systemd still
// gets a truthful status page instead of a blank one. systemd is consulted for
// what only systemd knows: the unit's own state, and whether it starts at boot.
//
// The consequence worth stating: on a host without systemd this package reports
// state and refuses control, rather than reporting nothing. "nginx is running,
// and I cannot restart it here" is a useful answer; an empty page is not.

// ProcessFinder reports which of the named processes are running.
//
// An interface rather than a direct dependency on the collectors package,
// because this package is about services and should not care how a process
// table is read — and because a test needs to say "pretend nginx is up"
// without starting nginx.
type ProcessFinder interface {
	// FindByName returns the lowest pid running under each of the given
	// process names. A name with nothing running is absent from the map.
	FindByName(names []string) map[string]int
}

// Detected is one service as this host actually has it.
type Detected struct {
	Definition
	// Installed reports whether the service is present on the host at all.
	Installed bool `json:"installed"`
	// Running is what the process table says, which is true whether or not
	// systemd is available to ask.
	Running bool `json:"running"`
	// PID is the process the panel found, or 0.
	PID int `json:"pid"`
	// Unit is the systemd unit that answered, when one did.
	Unit string `json:"unit,omitempty"`
	// Enabled reports whether it starts at boot. Nil means unknown — either
	// systemd is absent, or the unit is transient — which is a different
	// answer from "no".
	Enabled *bool `json:"enabled"`
	// ActiveState and SubState are systemd's own words, empty without it.
	ActiveState string `json:"active_state,omitempty"`
	SubState    string `json:"sub_state,omitempty"`
	// Controllable reports whether the panel can start and stop it. False on a
	// host with no service manager, and false for a daemon the panel starts
	// itself — both of which the panel shows rather than discovering one
	// failed click at a time.
	Controllable bool `json:"controllable"`
}

// Detect reports every catalogued service this host has.
//
// Services that are not installed are left out entirely: a panel listing
// Postfix on a host with no mail server is offering to manage something that
// is not there.
func (p *Provider) Detect(ctx context.Context, extra []Definition, processes ProcessFinder) []Detected {
	definitions := append(Catalogue(), extra...)
	controllable := p.Available()

	// One pass over the process table for every service, rather than one scan
	// each: /proc is cheap to read but not free, and a host has a dozen of
	// these.
	names := make([]string, 0, len(definitions)*2)
	for _, definition := range definitions {
		names = append(names, definition.Processes...)
	}

	running := map[string]int{}
	if processes != nil {
		running = processes.FindByName(names)
	}

	detected := make([]Detected, 0, len(definitions))
	for _, definition := range definitions {
		// A daemon the panel starts itself is never controllable from here,
		// however capable the host's init system is: two owners for one process
		// is worse than one owner and an explanation.
		entry := Detected{
			Definition:   definition,
			Controllable: controllable && !definition.SelfManaged,
		}

		for _, name := range definition.Processes {
			if pid, up := running[name]; up {
				entry.Running = true
				entry.PID = pid
				break
			}
		}

		entry.Installed = definition.installed() || entry.Running

		if controllable {
			// The unit is asked for last, and only if something is there to
			// ask about: systemctl on a host with a hundred absent units is a
			// hundred processes spawned to be told "not-found".
			if status, unit, found := p.resolveUnit(ctx, definition); found {
				entry.Unit = unit
				entry.Enabled = status.Enabled
				entry.ActiveState = status.ActiveState
				entry.SubState = status.SubState
				entry.Installed = true
				// systemd is the better answer where it disagrees with the
				// process table: a unit in "activating" is neither up nor
				// down, and the process table cannot say so.
				if status.Running {
					entry.Running = true
					if entry.PID == 0 {
						entry.PID = status.MainPID
					}
				}
			} else {
				// The init system has no unit or script for it. Whether it is
				// installed is a separate question — a binary can be present,
				// and even running, with nothing telling init about it — but
				// either way there is nothing here to start or stop, so the
				// panel says so rather than offering a button that fails.
				entry.Controllable = false
			}
		}

		if !entry.Installed {
			continue
		}
		detected = append(detected, entry)
	}

	sort.Slice(detected, func(i, j int) bool {
		if detected[i].Role != detected[j].Role {
			return roleOrder(detected[i].Role) < roleOrder(detected[j].Role)
		}
		return detected[i].Key < detected[j].Key
	})
	return detected
}

// resolveUnit finds which of a definition's candidate units this host has.
//
// Distributions disagree about names — httpd against apache2, crond against
// cron — so the panel asks about each candidate and uses the one that answers.
// The alternative is a table per distribution, which is a table that is wrong
// on the distribution nobody tested.
func (p *Provider) resolveUnit(ctx context.Context, definition Definition) (Status, string, bool) {
	for _, unit := range definition.Units {
		status, err := p.Status(ctx, unit)
		if err != nil {
			// Not found is the expected answer for the candidates that do not
			// apply to this host; anything else is a systemd that cannot be
			// asked, and the next candidate will not fare better.
			continue
		}
		return status, unit, true
	}
	return Status{}, "", false
}

// UnitFor returns the systemd unit a service key maps to on this host.
//
// This is the whole safety of the arrangement: a request names a key, and the
// unit it becomes is chosen here from the Agent's own table.
func (p *Provider) UnitFor(ctx context.Context, key string, extra []Definition) (Definition, string, error) {
	if !validKey(key) {
		return Definition{}, "", errUnknownService(key)
	}

	definition, err := Lookup(key, extra)
	if err != nil {
		return Definition{}, "", err
	}
	if !p.Available() {
		return definition, "", ErrUnavailable
	}

	_, unit, found := p.resolveUnit(ctx, definition)
	if !found {
		return definition, "", errUnknownService(key)
	}
	return definition, unit, nil
}

// roleOrder puts the list in the order an operator reads it: what serves the
// sites first, what stores their data next, the machine's own services last.
func roleOrder(role string) int {
	switch role {
	case RoleWeb:
		return 0
	case RoleRuntime:
		return 1
	case RoleDatabase:
		return 2
	case RoleCache:
		return 3
	default:
		return 4
	}
}
