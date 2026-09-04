package mail

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jothost/panel/shared/validate"
)

// Reconcile makes this host serve exactly the mail the panel has recorded.
//
// Desired state, not a delta, for the reason every reconcile in this panel
// works that way: a host that is converged to a description is one whose actual
// configuration can be reasoned about, and a host that has had a sequence of
// changes applied to it is one where nobody knows what is on it. Anything the
// panel does not list is removed.
//
// The order below is the order it has to be in, and each step is placed where
// it is because of what breaks otherwise:
//
//  1. Directories and the mail account, because everything else is written
//     into them.
//  2. The generated tables and configuration files, which are written but not
//     yet in force.
//  3. Postfix's own settings, through postconf.
//  4. Both daemons' validators, before anything is restarted — this is the
//     last moment a mistake is cheap.
//  5. Restarts, and only of the daemons whose configuration actually changed.
//
// Step 4 before step 5 is the whole point. A mail server restarted onto a
// configuration it refuses is a mail server that is down, and it goes down at
// the moment somebody changed a setting they thought was small.
func (p *Provider) Reconcile(ctx context.Context, desired Desired) (Result, error) {
	if !p.Available() {
		return Result{}, ErrUnavailable
	}
	if err := checkDesired(desired); err != nil {
		return Result{}, err
	}

	if !desired.Settings.Enabled {
		return p.disable(ctx)
	}

	uid, gid, err := p.ensureDirs(ctx)
	if err != nil {
		return Result{}, err
	}

	mapType, err := p.mapType(ctx)
	if err != nil {
		return Result{}, err
	}

	result := Result{MapType: mapType}
	var warnings []string

	// ---------------------------------------------------------------- tables
	domains, mailboxes, aliases, err := p.buildMaps(desired)
	if err != nil {
		return result, err
	}

	tablesChanged := false
	for _, table := range []struct {
		path    string
		content []byte
	}{
		{p.paths.DomainsMap(), domains},
		{p.paths.MailboxMap(), mailboxes},
		{p.paths.AliasMap(), aliases},
	} {
		same, err := fileMatches(table.path, table.content)
		if err != nil {
			return result, err
		}
		if same {
			// Still compile it if the indexed copy is missing — a source file
			// that matches with no .lmdb beside it is a host where the tables
			// were compiled by a Postfix that has since been reinstalled.
			if err := p.ensureCompiled(ctx, mapType, table.path); err != nil {
				return result, err
			}
			continue
		}
		// 0644: Postfix reads these as its own unprivileged user and there is
		// nothing secret in them. The passwd-file below is the one with
		// secrets in it, and it is not this.
		if err := writeFile(table.path, table.content, 0o644, -1, -1); err != nil {
			return result, err
		}
		if err := p.compile(ctx, mapType, table.path); err != nil {
			return result, err
		}
		tablesChanged = true
	}

	// ---------------------------------------------------------- user database
	passwd, err := buildPasswdFile(desired, uid, gid, p.paths.MailRoot)
	if err != nil {
		return result, err
	}
	// 0640, owned by root, group-owned by the account Dovecot authenticates as.
	//
	// Not 0600 root-only, and this is the single most misleading permission in
	// mail hosting to get wrong. Dovecot's authentication process drops from
	// root to its own internal user, so a file only root can read produces
	// "auth failed" for a correct password — with nothing anywhere saying the
	// problem is a permission. It was found by asking Dovecot to authenticate a
	// real mailbox, and the integration suite goes on asking, every run.
	//
	// Nothing needs a restart when this changes: Dovecot re-reads the
	// passwd-file on each authentication, so adding a mailbox or changing a
	// password takes effect immediately and drops nobody's IMAP connection.
	_, authGID := p.authOwnership()
	if err := p.writeIfChanged(p.paths.PasswdFile(), passwd, 0o640, 0, authGID); err != nil {
		return result, err
	}
	// And corrected even when the content did not change, because the content
	// check is what decides whether anything is written at all.
	if err := ensureMode(p.paths.PasswdFile(), 0o640, 0, authGID); err != nil {
		return result, err
	}

	// --------------------------------------------------------------- Maildirs
	if err := p.ensureMaildirs(desired, uid, gid); err != nil {
		return result, err
	}

	// -------------------------------------------------------------- Dovecot
	dovecotConf := buildDovecotConf(desired.Settings, p.paths, uid, gid)
	dovecotChanged, err := p.changed(p.paths.DovecotConf(), dovecotConf)
	if err != nil {
		return result, err
	}
	if dovecotChanged {
		if err := writeFile(p.paths.DovecotConf(), dovecotConf, 0o644, -1, -1); err != nil {
			return result, err
		}
	}

	sieveChanged, err := p.applySieve(ctx, desired, uid, gid)
	if err != nil {
		return result, err
	}

	// ----------------------------------------------------------------- DKIM
	dkimMap := buildDKIMMap(desired)
	if err := p.writeIfChanged(p.paths.DKIMMap(), dkimMap, 0o644, -1, -1); err != nil {
		return result, err
	}
	result.Signing = p.signingDomains(desired)

	rspamdChanged, err := p.applyRspamd(ctx, desired.Settings, len(result.Signing) > 0)
	if err != nil {
		return result, err
	}

	// -------------------------------------------------------------- Postfix
	main := p.mainSettings(desired, mapType, len(result.Signing) > 0)
	changedKeys, err := p.applyMain(ctx, main)
	if err != nil {
		return result, err
	}
	services := submissionServices(desired.Settings)
	if err := p.applyServices(ctx, services); err != nil {
		return result, err
	}

	// The distribution's own alias database, compiled if it has not been. See
	// ensureAliasDatabase: it is not the panel's file, and an uncompiled one
	// puts an error in the mail log on every connection.
	p.ensureAliasDatabase(ctx)

	// ------------------------------------------------------------ validation
	if err := p.ValidatePostfix(ctx); err != nil {
		return result, err
	}
	if err := p.ValidateDovecot(ctx); err != nil {
		return result, err
	}

	// -------------------------------------------------------------- restarts
	postfixChanged := tablesChanged || len(changedKeys) > 0
	reloaded, err := p.applyRestarts(ctx, postfixChanged, dovecotChanged || sieveChanged, rspamdChanged)
	if err != nil {
		return result, err
	}
	result.Reloaded = reloaded

	// -------------------------------------------------------------- counting
	for _, domain := range desired.Domains {
		if !domain.Active {
			continue
		}
		result.Domains++
		for _, box := range domain.Mailboxes {
			if box.Active {
				result.Mailboxes++
			}
		}
		for _, alias := range domain.Aliases {
			if alias.Active {
				result.Aliases++
			}
		}
	}

	result.Warnings = append(warnings, p.warnings(desired, services)...)
	return result, nil
}

// checkDesired refuses a request that cannot produce a working mail server.
//
// Refused here rather than at the API boundary as well as there, because this
// is the process that writes the files: a validation that lives only in the
// caller is one that a future caller can skip.
func checkDesired(desired Desired) error {
	if !desired.Settings.Enabled {
		return nil
	}
	if err := validate.MailHostname(desired.Settings.Hostname); err != nil {
		return fmt.Errorf("%w: %s", ErrNoHostname, err)
	}
	if err := validate.MessageSizeMB(desired.Settings.MaxMessageMB); err != nil {
		return err
	}
	if desired.Settings.SpamEnabled {
		if err := validate.SpamRejectScore(desired.Settings.SpamRejectScore); err != nil {
			return err
		}
	}

	seen := map[string]bool{}
	for _, domain := range desired.Domains {
		if err := validate.MailDomain(domain.Name); err != nil {
			return err
		}
		if seen[domain.Name] {
			return fmt.Errorf("%w: %s appears twice", ErrInvalidConfig, domain.Name)
		}
		seen[domain.Name] = true

		if domain.DKIMSelector != "" {
			if err := validate.DKIMSelector(domain.DKIMSelector); err != nil {
				return err
			}
		}
		if domain.CatchAll != "" {
			if _, err := validate.MailAddress(domain.CatchAll); err != nil {
				return err
			}
		}

		boxes := map[string]bool{}
		for _, box := range domain.Mailboxes {
			if err := validate.MailLocalPart(box.LocalPart); err != nil {
				return err
			}
			if boxes[box.LocalPart] {
				return fmt.Errorf("%w: %s appears twice in %s",
					ErrInvalidConfig, box.LocalPart, domain.Name)
			}
			boxes[box.LocalPart] = true
			if err := validate.MailQuotaMB(box.QuotaMB); err != nil {
				return err
			}
			if box.Autoresponder != nil {
				if err := validate.AutoresponderSubject(box.Autoresponder.Subject); err != nil {
					return err
				}
				if err := validate.AutoresponderBody(box.Autoresponder.Body); err != nil {
					return err
				}
				if err := validate.AutoresponderDays(box.Autoresponder.IntervalDays); err != nil {
					return err
				}
			}
		}

		for _, alias := range domain.Aliases {
			if err := validate.MailLocalPart(alias.Source); err != nil {
				return err
			}
			if _, err := validate.MailAddress(alias.Destination); err != nil {
				return err
			}
		}
	}
	return nil
}

// disable takes the panel's mail configuration back off the host.
//
// It empties the tables rather than deleting them, and removes the submission
// ports. What it deliberately does *not* do is stop Postfix or delete anybody's
// mail: the daemons belong to the service manager and the Maildirs belong to
// the customers. Turning mail off in the panel means this host stops accepting
// mail for the panel's domains — it does not mean the messages already
// delivered are destroyed.
func (p *Provider) disable(ctx context.Context) (Result, error) {
	result := Result{}

	mapType, err := p.mapType(ctx)
	if err != nil {
		return result, err
	}
	result.MapType = mapType

	empty := Desired{}
	domains, mailboxes, aliases, err := p.buildMaps(empty)
	if err != nil {
		return result, err
	}
	changed := false
	for _, table := range []struct {
		path    string
		content []byte
	}{
		{p.paths.DomainsMap(), domains},
		{p.paths.MailboxMap(), mailboxes},
		{p.paths.AliasMap(), aliases},
	} {
		same, err := fileMatches(table.path, table.content)
		if err != nil {
			return result, err
		}
		if same {
			continue
		}
		if err := writeFile(table.path, table.content, 0o644, -1, -1); err != nil {
			return result, err
		}
		if err := p.compile(ctx, mapType, table.path); err != nil {
			return result, err
		}
		changed = true
	}

	// The submission ports go, because they exist only for the panel's own
	// mailboxes and there are now none.
	if err := p.applyServices(ctx, nil); err != nil {
		return result, err
	}

	if changed {
		if err := p.reloadPostfix(ctx); err != nil {
			return result, err
		}
		result.Reloaded = true
	}
	result.Warnings = []string{
		"mail is turned off in the panel: this host no longer accepts mail for " +
			"any of its domains. Existing mailboxes and their messages are untouched.",
	}
	return result, nil
}

// ensureMaildirs creates the per-domain directory each mailbox lives under.
//
// Only the domain level. The mailbox's own Maildir is created by Dovecot at
// first delivery or first login, and letting it do that is deliberate: Dovecot
// creates the index files and the special-use folders at the same time, and a
// directory the panel made first is one Dovecot then has to adopt.
func (p *Provider) ensureMaildirs(desired Desired, uid, gid int) error {
	for _, domain := range desired.Domains {
		if !domain.Active {
			continue
		}
		path := p.paths.DomainRoot(domain.Name)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create the mail directory for %s: %w", domain.Name, err)
		}
		if err := os.Chown(path, uid, gid); err != nil {
			return fmt.Errorf("give the mail directory for %s to %s: %w",
				domain.Name, VmailUser, err)
		}
	}
	return nil
}

// applyRestarts restarts only what changed.
//
// Only what changed, because each of these costs something visible. Restarting
// Dovecot disconnects every mail client on the host; restarting Rspamd stops
// the milter, and with milter_default_action at tempfail that defers mail for
// as long as it takes to come back. A reconcile that ran on a schedule and
// restarted everything would be a mail server that hiccuped on a timer.
func (p *Provider) applyRestarts(ctx context.Context, postfix, dovecot, rspamd bool) (bool, error) {
	if p.services == nil {
		if postfix || dovecot || rspamd {
			return false, fmt.Errorf(
				"this host has no service manager, so the new mail configuration is written but not in force")
		}
		return false, nil
	}

	restarted := false
	if postfix {
		if err := p.reloadPostfix(ctx); err != nil {
			return restarted, fmt.Errorf("reload Postfix: %w", err)
		}
		restarted = true
	}
	if dovecot && p.services.Running(ctx, DaemonDovecot) {
		// A restart rather than a reload: Dovecot caches its TLS material and
		// its socket definitions at startup, so a reload leaves a daemon
		// reporting the new configuration while still serving the old one.
		if err := p.services.Restart(ctx, DaemonDovecot); err != nil {
			return restarted, fmt.Errorf("restart Dovecot: %w", err)
		}
		restarted = true
	}
	if rspamd && p.services.Running(ctx, DaemonRspamd) {
		// A reload rather than a restart, and this one is measured rather than
		// preferred on principle. Rspamd compiles ten thousand TLD suffixes at
		// startup, which takes the better part of a minute — and because
		// milter_default_action is tempfail, every message that arrives during
		// that minute is deferred. A reload re-reads the configuration without
		// the outage.
		//
		// The service layer falls back to a restart where the init script has
		// no reload, so a host without one still gets the change applied.
		if err := p.services.Reload(ctx, DaemonRspamd); err != nil {
			return restarted, fmt.Errorf("reload Rspamd: %w", err)
		}
		restarted = true
	}
	return restarted, nil
}

// warnings reports what is working now and is not what the operator thinks.
//
// Every one of these is a state the host is *in*, successfully, that will
// disappoint somebody later. They are not errors — the reconcile worked — and
// that is exactly why they have to be surfaced: nothing else will say them.
func (p *Provider) warnings(desired Desired, services []submissionService) []string {
	var warnings []string
	settings := desired.Settings

	if settings.TLSCertificate == "" || settings.TLSKey == "" {
		warnings = append(warnings,
			"the mail server has no certificate, so nothing it carries is encrypted "+
				"and no mail client will connect without being told to accept that")
	}
	if len(services) == 0 {
		warnings = append(warnings,
			"no submission port is offered, because encryption is required and there "+
				"is no certificate: customers can receive mail here but cannot send through it")
	}
	for _, service := range services {
		if service.overrides["smtpd_tls_security_level"] == "none" {
			warnings = append(warnings, fmt.Sprintf(
				"port %d accepts a password over an unencrypted connection, because the "+
					"TLS requirement was turned off", service.port))
		}
	}

	if settings.SpamEnabled && !p.SupportsFiltering() {
		warnings = append(warnings,
			"spam filtering is switched on and Rspamd is not installed, so no message "+
				"is being filtered")
	}

	var unsigned []string
	for _, domain := range desired.Domains {
		if !domain.Active {
			continue
		}
		if domain.DKIMSelector == "" {
			unsigned = append(unsigned, domain.Name)
			continue
		}
		if !p.dkimKeyExists(domain.Name, domain.DKIMSelector) {
			warnings = append(warnings, fmt.Sprintf(
				"%s is recorded as signing with the selector %q and this host has no such "+
					"key, so its mail is going out unsigned",
				domain.Name, domain.DKIMSelector))
		}
	}
	if len(unsigned) > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"%d domain(s) have no DKIM key, so their mail is unsigned and more likely to "+
				"be filed as spam: %s", len(unsigned), describeList(unsigned)))
	}

	if settings.VirusEnabled && !p.SupportsFiltering() {
		warnings = append(warnings,
			"virus scanning is switched on and Rspamd is not installed, so nothing calls "+
				"the scanner")
	}
	return warnings
}

// describeList names a few items and counts the rest.
//
// Grouped rather than listed, for the reason Phase 15 arrived at the hard way:
// eleven rows saying the same thing bury the one row that says something else.
func describeList(items []string) string {
	const shown = 3
	if len(items) <= shown {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more",
		strings.Join(items[:shown], ", "), len(items)-shown)
}

// writeIfChanged writes a file only when its content differs.
func (p *Provider) writeIfChanged(path string, content []byte, mode os.FileMode, uid, gid int) error {
	same, err := fileMatches(path, content)
	if err != nil {
		return err
	}
	if same {
		return nil
	}
	return writeFile(path, content, mode, uid, gid)
}

// changed reports whether a file's content would differ.
func (p *Provider) changed(path string, content []byte) (bool, error) {
	same, err := fileMatches(path, content)
	return !same, err
}

// ensureCompiled compiles a lookup table whose indexed copy is missing.
func (p *Provider) ensureCompiled(ctx context.Context, mapType, path string) error {
	if !needsPostmap(mapType) {
		return nil
	}
	suffix := "." + mapType
	if mapType == "hash" {
		suffix = ".db"
	}
	if _, err := os.Stat(path + suffix); err == nil {
		return nil
	}
	return p.compile(ctx, mapType, path)
}
