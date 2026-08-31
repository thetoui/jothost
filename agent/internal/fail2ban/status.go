package fail2ban

import (
	"context"
	"sort"
)

// Reporting what this host is doing.
//
// Two sources, deliberately kept apart. What the daemon is *running* comes from
// the daemon; what the panel *offers* comes from the catalogue. Where they
// overlap the daemon wins, because it is the thing actually banning people — and
// where they do not, both halves are still shown:
//
//   - a jail the panel offers and this host cannot run (its log is absent) is
//     listed as unavailable with the reason, rather than hidden, so that
//     "there is no jail for that" and "the panel does not do that" are
//     distinguishable
//   - a jail somebody configured by hand is listed as unmanaged and left alone,
//     because it is banning people whether the panel knows about it or not, and
//     a page that hid it would be describing a different machine

// Status reports everything the panel shows about intrusion prevention.
func (p *Provider) Status(ctx context.Context, canInstall bool) Status {
	status := Status{
		CanInstall: canInstall,
		Jails:      []Jail{},
		Ignored:    []string{},
		DropInPath: p.DropInPath(),
	}

	if !p.Available() {
		status.Reason = "fail2ban is not installed on this host"
		return status
	}
	status.Available = true
	status.CanInstall = false
	status.Version = p.Version(ctx)
	status.Running = p.Running(ctx)

	// What the panel offers, from the catalogue.
	//
	// Indexed by position rather than by pointer. A pointer taken into a slice
	// that is still being appended to points at the array the slice *had*, and
	// every write through it afterwards goes into memory nothing reads — which
	// looked exactly like the daemon reporting stale numbers.
	byName := map[string]int{}
	for _, definition := range Catalogue() {
		jail := Jail{
			Name:      definition.Name,
			Label:     definition.Label,
			Summary:   definition.Summary,
			Managed:   true,
			LogPaths:  []string{},
			Banned:    []string{},
			MaxRetry:  definition.Defaults.MaxRetry,
			FindTime:  definition.Defaults.FindTime,
			BanTime:   definition.Defaults.BanTime,
			Available: true,
		}

		if path := p.logPathFor(definition); path != "" {
			jail.LogPaths = []string{path}
		} else {
			jail.Available = false
			jail.Reason = "none of the logs this jail watches exist on this host yet"
		}

		status.Jails = append(status.Jails, jail)
		byName[definition.Name] = len(status.Jails) - 1
	}

	// What the panel last wrote, so a jail that is configured but not running —
	// because the daemon is stopped — still shows its settings.
	stored := p.readState()
	status.Ignored = stored.ignored
	for name, options := range stored.jails {
		index, known := byName[name]
		if !known {
			continue
		}
		jail := &status.Jails[index]
		jail.Enabled = options["enabled"] == "true"
		jail.MaxRetry = valueOr(options["maxretry"], jail.MaxRetry)
		jail.FindTime = valueOr(options["findtime"], jail.FindTime)
		jail.BanTime = valueOr(options["bantime"], jail.BanTime)
	}

	if !status.Running {
		if status.Reason == "" {
			status.Reason = "fail2ban is installed but not running, so nothing is being banned"
		}
		sortJails(status.Jails)
		return status
	}

	// What the daemon is actually running. This overrides everything above,
	// because it is the truth.
	running, err := p.Jails(ctx)
	if err != nil {
		p.log.Warn("the running jails could not be listed", "error", err.Error())
		sortJails(status.Jails)
		return status
	}

	for _, name := range running {
		live, err := p.JailStatus(ctx, name)
		if err != nil {
			p.log.Warn("a jail's status could not be read", "jail", name, "error", err.Error())
			continue
		}

		maxRetry, findTime, banTime, err := p.Policy(ctx, name)
		if err == nil {
			live.MaxRetry, live.FindTime, live.BanTime = maxRetry, findTime, banTime
		}

		if index, known := byName[name]; known {
			// Keep the catalogue's words, take the daemon's numbers.
			existing := status.Jails[index]
			live.Label, live.Summary, live.Managed = existing.Label, existing.Summary, true
			live.Available = true
			live.Reason = ""
			status.Jails[index] = live
			continue
		}

		// A jail the panel does not offer. Listed, and not touched.
		live.Managed = false
		live.Available = true
		status.Jails = append(status.Jails, live)
	}

	// The ignore list as the daemon resolved it, which is what is in force.
	// Read from any running jail: it is a DEFAULT, so they all have the same.
	if len(running) > 0 {
		if ignored, err := p.Ignored(ctx, running[0]); err == nil && len(ignored) > 0 {
			status.Ignored = ignored
		}
	}

	for _, jail := range status.Jails {
		status.Banned += jail.Currently
	}
	sortJails(status.Jails)
	return status
}

// Banned lists every currently banned address, with the jail that banned it.
type Banned struct {
	Address string `json:"address"`
	Jail    string `json:"jail"`
}

// BannedAddresses reports what this host is currently blocking.
func (p *Provider) BannedAddresses(ctx context.Context) ([]Banned, error) {
	if !p.Available() {
		return nil, ErrUnavailable
	}
	if !p.Running(ctx) {
		return nil, ErrNotRunning
	}

	jails, err := p.Jails(ctx)
	if err != nil {
		return nil, err
	}

	banned := make([]Banned, 0, 8)
	for _, name := range jails {
		jail, err := p.JailStatus(ctx, name)
		if err != nil {
			p.log.Warn("a jail's bans could not be read", "jail", name, "error", err.Error())
			continue
		}
		for _, address := range jail.Banned {
			banned = append(banned, Banned{Address: address, Jail: name})
		}
	}

	sort.Slice(banned, func(i, j int) bool {
		if banned[i].Jail != banned[j].Jail {
			return banned[i].Jail < banned[j].Jail
		}
		return banned[i].Address < banned[j].Address
	})
	return banned, nil
}

// sortJails puts the managed jails first, then orders by name — so the page
// leads with what the panel can do something about.
func sortJails(jails []Jail) {
	sort.SliceStable(jails, func(i, j int) bool {
		if jails[i].Managed != jails[j].Managed {
			return jails[i].Managed
		}
		return jails[i].Name < jails[j].Name
	})
}
