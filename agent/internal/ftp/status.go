package ftp

import (
	"context"
	"os"
	"sort"
)

// What the panel shows about the FTP server.
//
// Everything here is read from the host, not remembered from the last change:
// the accounts come from the password file proftpd authenticates against, the
// usage from the quota tally proftpd maintains, and the sessions from the
// scoreboard proftpd writes. A panel that reported its own last successful
// write would keep reporting it after somebody changed the file by hand.

// Status is the whole of what the FTP page needs.
type Status struct {
	// Available reports whether proftpd is installed.
	Available bool `json:"available"`
	// Running reports whether it is serving.
	Running bool `json:"running"`
	// CanInstall reports whether the Agent could install it.
	CanInstall bool   `json:"can_install"`
	Version    string `json:"version"`
	// Reason explains an unavailable server in words an operator can act on.
	Reason string `json:"reason,omitempty"`

	// SupportsTLS and SupportsQuota report what this build can do, so the page
	// can say why a feature is not on offer rather than accepting a setting
	// the daemon will ignore.
	SupportsTLS   bool `json:"supports_tls"`
	SupportsQuota bool `json:"supports_quota"`

	// Accounts are the FTP users on the host, with their usage.
	Accounts []AccountStatus `json:"accounts"`
	// Sessions are the clients connected right now.
	Sessions []Session `json:"sessions"`

	// FirewallOpen reports whether the ports FTP needs are actually admitted.
	// FirewallReason says what is closed when they are not.
	//
	// Worth its own field rather than a log line: a passive range the firewall
	// blocks produces a server that accepts the login and then hangs on the
	// first listing, which is the most confusing way for FTP to be broken.
	FirewallOpen   bool   `json:"firewall_open"`
	FirewallReason string `json:"firewall_reason,omitempty"`

	// ConfigPath is the file the panel owns.
	ConfigPath string `json:"config_path"`
	// Conflicts names settings another included file also sets.
	Conflicts []Conflict `json:"conflicts,omitempty"`
}

// AccountStatus is one account as the host has it.
type AccountStatus struct {
	Account
	// QuotaMB is the limit in force, zero for none.
	QuotaMB int `json:"quota_mb"`
	// UsedMB is what proftpd has counted this account uploading.
	//
	// It is what mod_quotatab tallied, not a directory size: files removed
	// through the file manager or a deploy are not subtracted, because
	// proftpd never saw them go. The page says so, and clearing the tally is
	// what corrects it.
	UsedMB float64 `json:"used_mb"`
}

// Status reads the whole picture.
//
// A failure to read one part does not fail the whole: a host whose quota table
// cannot be read still has accounts and sessions worth showing, and an FTP page
// that renders nothing because of one unreadable file is less useful than one
// that renders what it has.
func (p *Provider) Status(ctx context.Context, canInstall bool) Status {
	status := Status{
		CanInstall: canInstall,
		// Assumed open until something says otherwise, so a host with no
		// firewall does not carry a warning about one.
		FirewallOpen: true,
		Accounts:     []AccountStatus{},
		Sessions:     []Session{},
		ConfigPath:   p.paths.DropIn(),
	}

	if !p.Available() {
		status.Reason = ErrUnavailable.Error()
		return status
	}
	status.Available = true
	status.Version = p.Version(ctx)
	status.Running = p.Running(ctx)
	status.SupportsTLS = p.SupportsTLS(ctx)
	status.SupportsQuota = p.SupportsQuota(ctx)

	accounts, err := p.Accounts()
	if err != nil {
		p.log.Error("could not read the FTP accounts", "error", err.Error())
		status.Reason = err.Error()
		return status
	}

	quotas, err := p.Quotas(ctx)
	if err != nil {
		// Worth logging and worth continuing: the accounts are the important
		// half, and a missing quota reads as no limit rather than as a broken
		// page.
		p.log.Warn("could not read the FTP quota tables", "error", err.Error())
		quotas = map[string]Quota{}
	}

	for _, account := range accounts {
		entry := AccountStatus{Account: account}
		if quota, found := quotas[account.Name]; found {
			entry.QuotaMB = quota.LimitMB
			entry.UsedMB = quota.UsedMB
		}
		status.Accounts = append(status.Accounts, entry)
	}
	sort.Slice(status.Accounts, func(i, j int) bool {
		return status.Accounts[i].Name < status.Accounts[j].Name
	})

	if status.Running {
		sessions, err := p.Sessions(ctx)
		if err != nil {
			p.log.Warn("could not read the FTP sessions", "error", err.Error())
		} else {
			status.Sessions = sessions
		}
	}

	if _, err := os.Stat(p.paths.DropIn()); err == nil {
		status.Conflicts = p.conflicts()
	}
	return status
}
