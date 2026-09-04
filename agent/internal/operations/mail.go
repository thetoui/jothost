package operations

import (
	"context"
	"errors"
	"fmt"

	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/mail"
	"github.com/jothost/panel/agent/internal/sites"
	"github.com/jothost/panel/shared/protocol"
)

// The mail operations' request boundary.
//
// A reconcile request carries the mail this host should serve, exactly as the
// panel recorded it: domains, mailboxes, forwarders, and settings whose every
// field is a number, a flag, or a name that has been validated. The Agent
// validates all of it again — see mail.checkDesired — because this is the
// process that writes the files.
//
// Two things it deliberately cannot carry. It cannot carry a **plaintext
// password**: the API hashes at the HTTP boundary, so the privileged process
// never holds one and a request that has been captured contains no credential
// to replay. And it cannot carry a **path**, except the TLS certificate and key,
// which are checked to be absolute here and then checked to exist by the
// daemons' own validators before either is restarted.

// mailPayload carries a reconcile.
type mailPayload struct {
	Settings mailSettingsPayload `json:"settings"`
	Domains  []mailDomainPayload `json:"domains"`
}

type mailSettingsPayload struct {
	Enabled         bool   `json:"enabled"`
	Hostname        string `json:"hostname"`
	TLSCertificate  string `json:"tls_certificate"`
	TLSKey          string `json:"tls_key"`
	RequireTLS      bool   `json:"require_tls"`
	SpamEnabled     bool   `json:"spam_enabled"`
	SpamRejectScore int    `json:"spam_reject_score"`
	VirusEnabled    bool   `json:"virus_enabled"`
	MaxMessageMB    int    `json:"max_message_mb"`
}

type mailDomainPayload struct {
	Name         string               `json:"name"`
	Active       bool                 `json:"active"`
	CatchAll     string               `json:"catch_all"`
	DKIMSelector string               `json:"dkim_selector"`
	Mailboxes    []mailMailboxPayload `json:"mailboxes"`
	Aliases      []mailAliasPayload   `json:"aliases"`
}

type mailMailboxPayload struct {
	LocalPart     string                    `json:"local_part"`
	PasswordHash  string                    `json:"password_hash"`
	QuotaMB       int                       `json:"quota_mb"`
	Active        bool                      `json:"active"`
	Autoresponder *mailAutoresponderPayload `json:"autoresponder,omitempty"`
}

type mailAliasPayload struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Active      bool   `json:"active"`
}

type mailAutoresponderPayload struct {
	Subject      string `json:"subject"`
	Body         string `json:"body"`
	IntervalDays int    `json:"interval_days"`
	Active       bool   `json:"active"`
}

// desired converts the payload into the provider's own type.
func (p mailPayload) desired() (mail.Desired, error) {
	if p.Settings.TLSCertificate != "" || p.Settings.TLSKey != "" {
		if err := absolutePath("TLS certificate", p.Settings.TLSCertificate); err != nil {
			return mail.Desired{}, err
		}
		if err := absolutePath("TLS key", p.Settings.TLSKey); err != nil {
			return mail.Desired{}, err
		}
	}

	domains := make([]mail.Domain, 0, len(p.Domains))
	for _, entry := range p.Domains {
		boxes := make([]mail.Mailbox, 0, len(entry.Mailboxes))
		for _, box := range entry.Mailboxes {
			converted := mail.Mailbox{
				LocalPart:    box.LocalPart,
				PasswordHash: box.PasswordHash,
				QuotaMB:      box.QuotaMB,
				Active:       box.Active,
			}
			if box.Autoresponder != nil {
				converted.Autoresponder = &mail.Autoresponder{
					Subject:      box.Autoresponder.Subject,
					Body:         box.Autoresponder.Body,
					IntervalDays: box.Autoresponder.IntervalDays,
					Active:       box.Autoresponder.Active,
				}
			}
			boxes = append(boxes, converted)
		}

		aliases := make([]mail.Alias, 0, len(entry.Aliases))
		for _, alias := range entry.Aliases {
			aliases = append(aliases, mail.Alias{
				Source:      alias.Source,
				Destination: alias.Destination,
				Active:      alias.Active,
			})
		}

		domains = append(domains, mail.Domain{
			Name:         entry.Name,
			Active:       entry.Active,
			CatchAll:     entry.CatchAll,
			DKIMSelector: entry.DKIMSelector,
			Mailboxes:    boxes,
			Aliases:      aliases,
		})
	}

	return mail.Desired{
		Settings: mail.Settings{
			Enabled:         p.Settings.Enabled,
			Hostname:        p.Settings.Hostname,
			TLSCertificate:  p.Settings.TLSCertificate,
			TLSKey:          p.Settings.TLSKey,
			RequireTLS:      p.Settings.RequireTLS,
			SpamEnabled:     p.Settings.SpamEnabled,
			SpamRejectScore: p.Settings.SpamRejectScore,
			VirusEnabled:    p.Settings.VirusEnabled,
			MaxMessageMB:    p.Settings.MaxMessageMB,
		},
		Domains: domains,
	}, nil
}

// absolutePath refuses a relative path in a request.
//
// A relative path here would be resolved against the Agent's working directory,
// which is "/" — so "etc/passwd" would become a certificate path pointing at
// the password file. It fails safely either way, because the daemon's own
// validator would reject it, but failing at the boundary says which field was
// wrong.
func absolutePath(what, value string) error {
	if value == "" {
		return Fail(protocol.CodeInvalidPayload,
			fmt.Sprintf("the %s is required when TLS is configured", what), nil)
	}
	if value[0] != '/' {
		return Fail(protocol.CodeInvalidPayload,
			fmt.Sprintf("the %s must be an absolute path", what), nil)
	}
	return nil
}

// mailProvider returns the provider, or the error a caller should see when this
// host has no mail server.
func (r *Registry) mailProvider() (*mail.Provider, error) {
	if r.deps.Mail == nil || !r.deps.Mail.Available() {
		return nil, Fail(protocol.CodeUnsupported,
			"a mail server is not installed on this host", nil)
	}
	return r.deps.Mail, nil
}

// handleMailStatus reports what the host's mail server is doing.
func (r *Registry) handleMailStatus(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	var payload struct {
		// WebmailRoot is where webmail was installed, so the status can report
		// whether it is still there. The panel knows it; the Agent does not
		// keep a record of its own.
		WebmailRoot string `json:"webmail_root"`
	}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	if r.deps.Mail == nil {
		return structToMap(mail.Status{Reason: mail.ErrUnavailable.Error()})
	}
	status := r.deps.Mail.Status(ctx, r.canInstallPackages())
	data, err := structToMap(status)
	if err != nil {
		return nil, err
	}
	data["webmail"] = r.deps.Mail.WebmailStatus(payload.WebmailRoot)
	return data, nil
}

// handleMailInstall puts a mail server on the host.
func (r *Registry) handleMailInstall(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	var payload struct {
		// Filtering asks for the spam and virus machinery as well. Separate
		// because it is a much larger install — ClamAV's signature database
		// alone is hundreds of megabytes — and a host that only needs to
		// deliver mail should not have to wait for it.
		Filtering bool `json:"filtering"`
		Antivirus bool `json:"antivirus"`
	}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if r.deps.PHPInstaller == nil || !r.deps.PHPInstaller.Available() {
		return nil, Fail(protocol.CodeUnsupported,
			"this host has no package manager the panel can install with", nil)
	}

	// Both halves of the server, always. A host with one of them is worse than
	// a host with neither: Postfix alone accepts mail nobody can read, and
	// Dovecot alone offers mailboxes nothing delivers into — and both look
	// installed.
	packages := []string{"postfix", "dovecot", "dovecot-lmtpd", "dovecot-pop3d"}
	// Sieve is what makes autoresponders work. Without it a vacation script is
	// written, compiled by nothing, and ignored at delivery.
	packages = append(packages, "dovecot-pigeonhole-plugin")
	if payload.Filtering {
		packages = append(packages, "rspamd", "rspamd-client")
	}
	if payload.Antivirus {
		packages = append(packages, "clamav", "clamav-daemon")
	}

	for _, name := range packages {
		if err := r.deps.PHPInstaller.InstallPackage(ctx, name, reporterFunc(reporter)); err != nil {
			return nil, err
		}
	}

	if r.deps.Mail == nil {
		return map[string]any{"available": true}, nil
	}
	return structToMap(r.deps.Mail.Status(ctx, r.canInstallPackages()))
}

// handleMailReconcile makes the host serve exactly the mail the panel records.
func (r *Registry) handleMailReconcile(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.mailProvider()
	if err != nil {
		return nil, err
	}
	var payload mailPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	desired, err := payload.desired()
	if err != nil {
		return nil, err
	}

	result, err := provider.Reconcile(ctx, desired)
	if err != nil {
		return nil, mailError(err)
	}
	return structToMap(result)
}

// handleMailDKIMGenerate creates a signing key for one domain.
//
// The private key is written to this host and is not in the reply. The reply
// carries the public half, which the API records and publishes — see
// docs/PHASE26.md on why the two halves live in different places.
func (r *Registry) handleMailDKIMGenerate(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.mailProvider()
	if err != nil {
		return nil, err
	}
	var payload struct {
		Domain   string `json:"domain"`
		Selector string `json:"selector"`
	}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	key, err := provider.GenerateDKIM(ctx, payload.Domain, payload.Selector)
	if err != nil {
		return nil, mailError(err)
	}
	return structToMap(key)
}

// handleMailDKIMRemove deletes a signing key.
func (r *Registry) handleMailDKIMRemove(_ context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.mailProvider()
	if err != nil {
		return nil, err
	}
	var payload struct {
		Domain   string `json:"domain"`
		Selector string `json:"selector"`
	}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	if err := provider.RemoveDKIM(payload.Domain, payload.Selector); err != nil {
		return nil, mailError(err)
	}
	return map[string]any{"domain": payload.Domain, "removed": true}, nil
}

// handleMailQuota reports how full mailboxes are.
//
// Asked of Dovecot rather than measured from the filesystem: a Maildir on disk
// includes index files and messages a client has deleted but not expunged, so
// a directory size is consistently larger than what the customer's mail client
// shows them.
func (r *Registry) handleMailQuota(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.mailProvider()
	if err != nil {
		return nil, err
	}
	var payload struct {
		Addresses []string `json:"addresses"`
	}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	const maxAddresses = 500
	if len(payload.Addresses) > maxAddresses {
		return nil, Fail(protocol.CodeInvalidPayload,
			"too many mailboxes were asked about at once", nil)
	}

	usage := make(map[string]any, len(payload.Addresses))
	for _, address := range payload.Addresses {
		used, limit, err := provider.Quota(ctx, address)
		if err != nil {
			// One mailbox that cannot be read must not fail the whole page.
			// Reporting it as unknown is honest; reporting it as empty would
			// show a full mailbox as having room.
			usage[address] = map[string]any{"known": false, "detail": err.Error()}
			continue
		}
		usage[address] = map[string]any{
			"known": true, "used_mb": used, "limit_mb": limit,
		}
	}
	return map[string]any{"usage": usage}, nil
}

// handleWebmailInstall unpacks webmail into a website's document root.
func (r *Registry) handleWebmailInstall(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.mailProvider()
	if err != nil {
		return nil, err
	}
	var payload struct {
		DocumentRoot string `json:"document_root"`
		Owner        string `json:"owner"`
		Domain       string `json:"domain"`
		IMAPHost     string `json:"imap_host"`
		SMTPHost     string `json:"smtp_host"`
	}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	result, err := provider.InstallWebmail(ctx, mail.WebmailRequest{
		DocumentRoot: payload.DocumentRoot,
		Owner:        payload.Owner,
		Domain:       payload.Domain,
		IMAPHost:     payload.IMAPHost,
		SMTPHost:     payload.SMTPHost,
	}, reporterFunc(reporter))
	if err != nil {
		return nil, mailError(err)
	}
	return structToMap(result)
}

// handleWebmailRemove deletes webmail from a document root.
func (r *Registry) handleWebmailRemove(_ context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.mailProvider()
	if err != nil {
		return nil, err
	}
	var payload struct {
		DocumentRoot string `json:"document_root"`
	}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if err := provider.RemoveWebmail(payload.DocumentRoot); err != nil {
		return nil, mailError(err)
	}
	return map[string]any{"removed": true}, nil
}

// mailServices drives the mail daemons through the host's service manager.
//
// The same arrangement the FTP reloader uses, and for the same reason: exactly
// one thing on this host owns daemon lifecycles, and it is the service manager
// the registry already holds.
type mailServices struct{ registry *Registry }

// MailServicesFor returns the daemon controller for a registry, for the Agent
// to hand back to the mail provider once both exist.
func MailServicesFor(r *Registry) mail.Services { return mailServices{registry: r} }

func (m mailServices) Restart(ctx context.Context, name string) error {
	provider := m.registry.deps.Services
	if provider == nil || !provider.Available() {
		return fmt.Errorf("this host has no service manager to restart %s with", name)
	}
	_, unit, err := provider.UnitFor(ctx, name, nil)
	if err != nil {
		return fmt.Errorf("find the %s service: %w", name, err)
	}
	return provider.Restart(ctx, unit)
}

func (m mailServices) Reload(ctx context.Context, name string) error {
	provider := m.registry.deps.Services
	if provider == nil || !provider.Available() {
		return fmt.Errorf("this host has no service manager to reload %s with", name)
	}
	_, unit, err := provider.UnitFor(ctx, name, nil)
	if err != nil {
		return fmt.Errorf("find the %s service: %w", name, err)
	}
	// A reload where the service manager offers one, and a restart where it
	// does not. Postfix is the only daemon here whose reload genuinely re-reads
	// everything, and it is the one that most needs not to be restarted: a
	// restart drops deliveries that are in progress.
	if err := provider.Reload(ctx, unit); err != nil {
		return provider.Restart(ctx, unit)
	}
	return nil
}

func (m mailServices) Running(ctx context.Context, name string) bool {
	provider := m.registry.deps.Services
	if provider == nil || !provider.Available() {
		return false
	}
	status, err := provider.Status(ctx, name)
	if err != nil {
		return false
	}
	return status.Running
}

// mailAccounts creates the unprivileged account that owns every Maildir.
type mailAccounts struct{ users *sites.UserProvider }

// MailAccountsFor adapts the site account provider for the mail package.
func MailAccountsFor(users *sites.UserProvider) mail.Accounts {
	return mailAccounts{users: users}
}

func (m mailAccounts) EnsureAccount(ctx context.Context, name, home string) (int, int, error) {
	if m.users == nil || !m.users.Available() {
		return 0, 0, fmt.Errorf("this host has no way to create the %s account", name)
	}
	account, err := m.users.Ensure(ctx, name, home)
	if err != nil {
		return 0, 0, err
	}
	return account.UID, account.GID, nil
}

// mailError maps this package's errors onto protocol codes.
//
// Each one is a different thing for an operator to do. ErrWebmailChecksum in
// particular is not collapsed into a generic failure: every other error here is
// a misconfiguration, and that one means the download was not what it should
// have been.
func mailError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, mail.ErrUnavailable), errors.Is(err, mail.ErrWebmailUnavailable):
		return Fail(protocol.CodeUnsupported, err.Error(), err)
	case errors.Is(err, mail.ErrNotRunning):
		return Fail(protocol.CodeInvalidRequest, err.Error(), err)
	case errors.Is(err, mail.ErrUnknownDomain):
		return Fail(protocol.CodeNotFound, err.Error(), err)
	case errors.Is(err, mail.ErrInvalidConfig),
		errors.Is(err, mail.ErrNoCertificate),
		errors.Is(err, mail.ErrNoHostname):
		return Fail(protocol.CodeInvalidRequest, err.Error(), err)
	case errors.Is(err, mail.ErrWebmailChecksum):
		return Fail(protocol.CodeInternal,
			"the webmail download did not match the checksum this panel pins, so it was "+
				"not installed: "+err.Error(), err)
	default:
		return Fail(protocol.CodeInternal, err.Error(), err)
	}
}
