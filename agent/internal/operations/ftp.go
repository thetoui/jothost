package operations

import (
	"context"
	"errors"
	"fmt"
	"os/user"
	"strconv"
	"strings"

	"github.com/jothost/panel/agent/internal/ftp"
	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// The FTP operations' request boundary.
//
// A request carries **the accounts the panel has recorded** — a name, the
// website account they belong to, a home the API resolved against that
// website's root, and two flags — together with settings whose every field is a
// number or a path the panel itself produced. The uid and gid are not in the
// request: the Agent resolves them from the host's own passwd database, because
// a uid that arrived over the wire would be a way to ask for somebody else's.
//
// What it cannot carry is a configuration directive, a filter, or a path the
// Agent has not resolved for itself. The certificate paths are the one place a
// path arrives from outside, and they are checked to be absolute here and
// checked to exist by proftpd's own validator before the daemon is restarted;
// a relative one, or one that is not there, is a configuration that fails to
// load rather than one that loads something unintended.

// ftpPayload carries what these operations need.
type ftpPayload struct {
	// Settings are the server-wide options.
	Settings ftpSettingsPayload `json:"settings"`
	// Users is the complete set of accounts this host should have. Anything
	// else on the host is removed, which is what makes a deleted website's
	// accounts go away.
	Users []ftpUserPayload `json:"users"`
	// Passwords are the new ones, by account name. Only accounts being created
	// or reset appear here: the panel does not store FTP passwords.
	Passwords map[string]string `json:"passwords"`
	// PID identifies a session to disconnect.
	PID int `json:"pid"`
}

type ftpSettingsPayload struct {
	PassiveFrom       int    `json:"passive_from"`
	PassiveTo         int    `json:"passive_to"`
	TLSCertificate    string `json:"tls_certificate"`
	TLSKey            string `json:"tls_key"`
	RequireTLS        bool   `json:"require_tls"`
	MasqueradeAddress string `json:"masquerade_address"`
	MaxClients        int    `json:"max_clients"`
}

type ftpUserPayload struct {
	Name string `json:"name"`
	// SystemUser is the website's own account. The Agent resolves it to a uid
	// and gid here rather than accepting numbers over the wire: the host's
	// passwd database is the truth about which account owns a site's files, and
	// a uid that arrived in a request would be a way to ask for somebody
	// else's.
	SystemUser string `json:"system_user"`
	Home       string `json:"home"`
	ReadOnly   bool   `json:"read_only"`
	QuotaMB    int    `json:"quota_mb"`
	Suspended  bool   `json:"suspended"`
}

func (p ftpPayload) desired() (ftp.Desired, error) {
	users := make([]ftp.User, 0, len(p.Users))
	for _, entry := range p.Users {
		uid, gid, err := lookupAccount(entry.SystemUser)
		if err != nil {
			return ftp.Desired{}, err
		}
		users = append(users, ftp.User{
			Name:      entry.Name,
			UID:       uid,
			GID:       gid,
			Home:      entry.Home,
			ReadOnly:  entry.ReadOnly,
			QuotaMB:   entry.QuotaMB,
			Suspended: entry.Suspended,
		})
	}
	return ftp.Desired{
		Settings: ftp.Settings{
			PassiveFrom:       p.Settings.PassiveFrom,
			PassiveTo:         p.Settings.PassiveTo,
			TLSCertificate:    p.Settings.TLSCertificate,
			TLSKey:            p.Settings.TLSKey,
			RequireTLS:        p.Settings.RequireTLS,
			MasqueradeAddress: p.Settings.MasqueradeAddress,
			MaxClients:        p.Settings.MaxClients,
		},
		Users:     users,
		Passwords: p.Passwords,
	}, nil
}

// lookupAccount resolves a website's system account to its numeric ids.
//
// Refusals here are refusals to write an FTP account: an account that maps to
// nothing would upload files owned by nobody, and one that maps to root would
// upload them owned by root inside a chroot root can leave.
func lookupAccount(name string) (int, int, error) {
	if name == "" {
		return 0, 0, Fail(protocol.CodeInvalidPayload,
			"an FTP account must name the website account it belongs to", nil)
	}
	if err := validate.SystemUser(name); err != nil {
		return 0, 0, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}

	account, err := user.Lookup(name)
	if err != nil {
		return 0, 0, Fail(protocol.CodeNotFound,
			fmt.Sprintf("this host has no account called %q to map the FTP user onto", name), err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return 0, 0, fmt.Errorf("read the uid of %q: %w", name, err)
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return 0, 0, fmt.Errorf("read the gid of %q: %w", name, err)
	}
	return uid, gid, nil
}

// ftpProvider returns the provider, or the error a caller should see when this
// host has no FTP server.
func (r *Registry) ftpProvider() (*ftp.Provider, error) {
	if r.deps.FTP == nil || !r.deps.FTP.Available() {
		return nil, Fail(protocol.CodeUnsupported,
			"an FTP server is not installed on this host", nil)
	}
	return r.deps.FTP, nil
}

// handleFTPStatus reports the server, its accounts and its sessions.
func (r *Registry) handleFTPStatus(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	// The passive range the panel has recorded, so the firewall check knows
	// which ports to look for. Zero means the caller did not say, and the
	// check then only covers the control port.
	var payload struct {
		PassiveFrom int `json:"passive_from"`
		PassiveTo   int `json:"passive_to"`
	}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	if r.deps.FTP == nil {
		return structToMap(ftp.Status{
			Accounts: []ftp.AccountStatus{}, Sessions: []ftp.Session{},
			Reason: ftp.ErrUnavailable.Error(),
		})
	}
	status := r.deps.FTP.Status(ctx, r.canInstallPackages())
	status.FirewallOpen, status.FirewallReason = r.ftpFirewallReport(ctx,
		payload.PassiveFrom, payload.PassiveTo)
	return structToMap(status)
}

// handleFTPInstall puts an FTP server on the host.
//
// It reuses the package-manager wrapper the Agent already has, for the reason
// the fail2ban and Apache installers do: it resolves apk, apt or dnf once at
// startup and never takes a package name from a request.
func (r *Registry) handleFTPInstall(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	var payload struct{}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if r.deps.FTP != nil && r.deps.FTP.Available() {
		// Already there. Reporting that is more useful than installing it
		// again, and idempotence is what makes a retry safe.
		return structToMap(r.deps.FTP.Status(ctx, false))
	}
	if r.deps.PHPInstaller == nil || !r.deps.PHPInstaller.Available() {
		return nil, Fail(protocol.CodeUnsupported,
			"this host has no package manager the panel can install with", nil)
	}

	// The daemon, the tools this phase drives it with, and the two optional
	// modules it needs.
	//
	// All of them, because each absence fails differently and quietly. Without
	// proftpd-utils the panel writes a configuration and then fails at the first
	// account. Without mod_tls, proftpd skips the whole <IfModule> block holding
	// the FTPS settings, so the panel would write a valid configuration, restart
	// cleanly, report FTPS is on, and serve plain FTP.
	for _, pkg := range []string{
		"proftpd", "proftpd-utils",
		"proftpd-mod_tls", "proftpd-mod_quotatab", "proftpd-mod_quotatab_file",
	} {
		if err := r.deps.PHPInstaller.InstallPackage(ctx, pkg, reporterFunc(reporter)); err != nil {
			return nil, err
		}
	}

	if r.deps.FTP == nil {
		return map[string]any{"available": true}, nil
	}
	return structToMap(r.deps.FTP.Status(ctx, r.canInstallPackages()))
}

// handleFTPReconcile makes the host match what the panel has recorded.
func (r *Registry) handleFTPReconcile(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.ftpProvider()
	if err != nil {
		return nil, err
	}
	var payload ftpPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	desired, err := payload.desired()
	if err != nil {
		return nil, err
	}

	result, err := provider.Reconcile(ctx, desired)
	if err != nil {
		return nil, ftpError(err)
	}
	return structToMap(result)
}

// handleFTPSessions reports who is connected.
func (r *Registry) handleFTPSessions(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.ftpProvider()
	if err != nil {
		return nil, err
	}
	var payload struct{}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	sessions, err := provider.Sessions(ctx)
	if err != nil {
		return nil, ftpError(err)
	}
	return map[string]any{"sessions": sessions, "count": len(sessions)}, nil
}

// handleFTPDisconnect ends one session.
func (r *Registry) handleFTPDisconnect(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.ftpProvider()
	if err != nil {
		return nil, err
	}
	var payload ftpPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	if err := provider.Disconnect(ctx, payload.PID); err != nil {
		return nil, ftpError(err)
	}
	return map[string]any{"pid": payload.PID, "disconnected": true}, nil
}

// ftpFirewall reports whether the ports FTP needs are actually reachable.
//
// It reports and does not change anything, which is the boundary Phase 17 set
// and this phase keeps: a firewall change is its own deliberate act with its
// own protocol — back up, validate, apply, verify connectivity, commit, roll
// back on failure (CLAUDE.md section 19) — and performing one as a side effect
// of saving an FTP setting would open ports without the operator seeing what
// was opened, and without the audit trail saying a firewall change happened.
//
// So the panel checks and says. What it says is worth saying: a passive range
// the firewall does not admit produces a server that accepts the login and then
// hangs on the first directory listing, which is the single most confusing way
// for FTP to be broken.
func (r *Registry) ftpFirewallReport(ctx context.Context, from, to int) (bool, string) {
	provider := r.deps.Firewall
	if provider == nil || !provider.Available() {
		// No firewall is not a closed firewall.
		return true, ""
	}

	status, err := provider.Status(ctx)
	if err != nil {
		return false, "the firewall's rules could not be read: " + err.Error()
	}
	if !status.Enabled {
		return true, ""
	}

	// The control port and the whole passive range. Every one of them has to be
	// admitted: a range that is half open is a server that works until it is
	// busy enough to reach the closed half.
	needed := []int{21, from, to}
	for _, port := range needed {
		covered := false
		for _, rule := range status.Rules {
			if !strings.EqualFold(rule.Action, "allow") {
				continue
			}
			if rule.Direction != "" && !strings.EqualFold(rule.Direction, "in") {
				continue
			}
			if portCovered(rule.Port, port) {
				covered = true
				break
			}
		}
		if !covered {
			if port == 21 {
				return false, "the firewall is on and no rule allows connections to " +
					"port 21, so no client can reach the FTP server"
			}
			return false, fmt.Sprintf(
				"the firewall is on and does not allow ports %d-%d, so logins will "+
					"succeed and every transfer will hang", from, to)
		}
	}
	return true, ""
}

// ftpReloader restarts the FTP server through the service manager.
//
// A restart rather than a signal: proftpd reads the passive port range and the
// TLS material at startup, so a SIGHUP leaves a daemon that reports the new
// configuration while still listening with the old one. The configuration has
// already been through `proftpd -t` by the time this runs, so the restart is
// not where a bad file would be discovered.
type ftpReloader struct{ registry *Registry }

// FTPReloaderFor returns the restarter for a registry, for the Agent to hand
// back to the FTP provider once both exist.
func FTPReloaderFor(r *Registry) ftp.Reloader { return ftpReloader{registry: r} }

func (f ftpReloader) Reload(ctx context.Context) error {
	provider := f.registry.deps.Services
	if provider == nil || !provider.Available() {
		return fmt.Errorf("this host has no service manager to restart the FTP server with")
	}

	_, unit, err := provider.UnitFor(ctx, "proftpd", nil)
	if err != nil {
		return fmt.Errorf("find the FTP service: %w", err)
	}
	return provider.Restart(ctx, unit)
}

func (f ftpReloader) Running(ctx context.Context) bool {
	provider := f.registry.deps.Services
	if provider == nil || !provider.Available() {
		return false
	}
	status, err := provider.Status(ctx, "proftpd")
	if err != nil {
		return false
	}
	return status.Running
}

// ftpError maps this package's errors onto protocol codes.
//
// Each one is a different thing for an operator to do, and collapsing them into
// one code would make the page say "something went wrong" where it could say
// what.
func ftpError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ftp.ErrUnavailable):
		return Fail(protocol.CodeUnsupported, err.Error(), err)
	case errors.Is(err, ftp.ErrNotRunning):
		return Fail(protocol.CodeInvalidRequest, err.Error(), err)
	case errors.Is(err, ftp.ErrUnknownUser), errors.Is(err, ftp.ErrNoSession):
		return Fail(protocol.CodeNotFound, err.Error(), err)
	case errors.Is(err, ftp.ErrUserExists):
		return Fail(protocol.CodeInvalidRequest, err.Error(), err)
	case errors.Is(err, ftp.ErrInvalidConfig), errors.Is(err, ftp.ErrNoCertificate):
		return Fail(protocol.CodeInvalidRequest, err.Error(), err)
	default:
		return Fail(protocol.CodeInternal, err.Error(), err)
	}
}
