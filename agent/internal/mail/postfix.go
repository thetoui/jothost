package mail

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// The lookup table formats this package will use, in order of preference.
//
// Which of them a host has depends on how its Postfix was built, and getting it
// wrong is not a startup error: Postfix reports "unsupported dictionary type"
// per lookup, at delivery time, so the server starts cleanly and then refuses
// every recipient. The panel asks rather than assumes.
//
// lmdb first because it is an indexed table with no separate database library,
// which is what modern builds ship. hash second, for the builds that still have
// Berkeley DB. texthash last: it needs no compilation step at all, which makes
// it the safe fallback, at the cost of being read linearly and only re-read
// when a Postfix process starts.
var preferredMapTypes = []string{"lmdb", "hash", "texthash"}

// mapType asks Postfix which lookup table formats it supports and picks one.
func (p *Provider) mapType(ctx context.Context) (string, error) {
	result, err := p.runner.Run(ctx, CommandPostconf, "-m")
	if err != nil {
		return "", wrap("ask Postfix which lookup tables it supports", err)
	}
	if !result.Succeeded() {
		return "", fmt.Errorf("ask Postfix which lookup tables it supports: %s",
			firstLine(result.Stderr, result.Stdout))
	}

	supported := map[string]bool{}
	for _, line := range strings.Split(result.Stdout, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			supported[name] = true
		}
	}
	for _, candidate := range preferredMapTypes {
		if supported[candidate] {
			return candidate, nil
		}
	}
	return "", fmt.Errorf(
		"%w: this Postfix supports none of the lookup table formats the panel writes (%s)",
		ErrUnavailable, strings.Join(preferredMapTypes, ", "))
}

// needsPostmap reports whether a map type has to be compiled after the source
// file changes.
func needsPostmap(mapType string) bool { return mapType != "texthash" }

// mainSettings builds the main.cf keys the panel owns.
//
// The panel owns *keys*, not the file. Everything not named here is the
// distribution's and is left exactly as it was, which is what makes a Postfix
// upgrade merge cleanly on a host this panel manages.
//
// The map is returned rather than applied so it can be tested without a
// Postfix, and so the caller can diff it against what the server currently
// reports — which is how drift is found.
func (p *Provider) mainSettings(desired Desired, mapType string, signing bool) map[string]string {
	settings := desired.Settings

	values := map[string]string{
		// What the server calls itself. This is the single most consequential
		// value in the file: it is the EHLO name, and a receiving server that
		// cannot resolve it, or that resolves it to a different address than
		// the one connecting, treats the mail as suspicious before looking at
		// anything else.
		"myhostname": settings.Hostname,
		"myorigin":   "$myhostname",

		// What this host considers *local* mail, as opposed to virtual.
		//
		// The virtual domains are deliberately absent, and this is the mistake
		// that most often breaks a virtual mailbox setup: a domain listed in
		// both mydestination and virtual_mailbox_domains is delivered locally,
		// to a system account that does not exist, and the mail bounces with
		// "unknown user" for a mailbox the panel is showing as present.
		"mydestination": "$myhostname, localhost.$mydomain, localhost",

		// Who may relay without authenticating: this machine, and nothing
		// else.
		//
		// Not the local network. "The server is on a trusted network" is how
		// almost every open relay in the world came to be one — the network
		// stops being trusted the moment one machine on it is compromised, and
		// the mail server is then relaying for whatever that machine runs.
		"mynetworks": "127.0.0.0/8 [::1]/128",

		"inet_interfaces": "all",
		"smtpd_banner":    "$myhostname ESMTP",

		"message_size_limit": strconv.Itoa(settings.MaxMessageMB * 1024 * 1024),
		// Zero here means no limit *from Postfix*, which is correct: the limit
		// belongs to Dovecot, which knows how full each mailbox is. A limit in
		// both places is one that gets changed in one of them.
		"mailbox_size_limit": "0",

		// The virtual maps the panel generates.
		"virtual_mailbox_domains": mapType + ":" + p.paths.DomainsMap(),
		"virtual_mailbox_maps":    mapType + ":" + p.paths.MailboxMap(),
		"virtual_alias_maps":      mapType + ":" + p.paths.AliasMap(),
		// Delivery goes to Dovecot over LMTP rather than Postfix writing the
		// Maildir itself. Dovecot has to be the one that writes it: it is the
		// process that knows the quota, that maintains the index files, and
		// that would otherwise find a message in a mailbox it has not
		// accounted for.
		"virtual_transport": "lmtp:unix:private/dovecot-lmtp",

		// Authentication is Dovecot's, over a socket in Postfix's own chroot.
		// One user database for SMTP and IMAP, which is the only way a password
		// change can be a single act.
		"smtpd_sasl_type": "dovecot",
		"smtpd_sasl_path": "private/auth",
		// Off on port 25, where it has no business being: a mail server does
		// not authenticate the other mail servers of the world, and offering
		// AUTH there invites a password-guessing campaign against every
		// mailbox on the host. Submission turns it on for itself.
		"smtpd_sasl_auth_enable": "no",

		"smtpd_helo_required": "yes",

		// The restriction that decides whether this host is an open relay.
		//
		// Postfix evaluates these in order and stops at the first match, so
		// the order is the policy: this machine may relay, an authenticated
		// client may relay, and everything else is refused. defer_unauth
		// rather than reject_unauth is Postfix's own recommendation for this
		// list — a temporary failure, so a legitimate sender caught by a
		// misconfiguration retries rather than receiving a permanent bounce.
		//
		// This parameter exists precisely so that the relay decision cannot be
		// accidentally weakened by something later in
		// smtpd_recipient_restrictions. It is checked *first*, whatever else
		// is configured.
		"smtpd_relay_restrictions": "permit_mynetworks, permit_sasl_authenticated, defer_unauth_destination",
		// And the same answer again at the recipient stage, because a host
		// that gets this wrong is on a blocklist before anybody notices.
		"smtpd_recipient_restrictions": "permit_mynetworks, permit_sasl_authenticated, reject_unauth_destination",
	}

	// TLS.
	if settings.TLSCertificate != "" && settings.TLSKey != "" {
		values["smtpd_tls_cert_file"] = settings.TLSCertificate
		values["smtpd_tls_key_file"] = settings.TLSKey
		// "may" on port 25 and not "encrypt": a mail server that refuses
		// unencrypted connections from other mail servers refuses mail from
		// every sender that does not do TLS, and receives less mail rather
		// than more secure mail. Submission is where encryption is mandatory,
		// and it is set there.
		values["smtpd_tls_security_level"] = "may"
		values["smtpd_tls_mandatory_protocols"] = "!SSLv2, !SSLv3, !TLSv1, !TLSv1.1"
		values["smtpd_tls_protocols"] = "!SSLv2, !SSLv3, !TLSv1, !TLSv1.1"
		// Opportunistic TLS when this host is the sender. It costs nothing and
		// encrypts the majority of outbound mail, because nearly every
		// receiving server offers STARTTLS.
		values["smtp_tls_security_level"] = "may"
		if settings.RequireTLS {
			// No password may cross an unencrypted connection, on any port.
			values["smtpd_tls_auth_only"] = "yes"
		} else {
			values["smtpd_tls_auth_only"] = "no"
		}
	} else {
		values["smtpd_tls_cert_file"] = ""
		values["smtpd_tls_key_file"] = ""
		values["smtpd_tls_security_level"] = "none"
		values["smtpd_tls_auth_only"] = "no"
		values["smtp_tls_security_level"] = "may"
	}

	// The milter, which is Rspamd: it filters what arrives and signs what
	// leaves.
	if p.SupportsFiltering() && (settings.SpamEnabled || signing) {
		values["smtpd_milters"] = "inet:127.0.0.1:11332"
		values["non_smtpd_milters"] = "$smtpd_milters"
		values["milter_protocol"] = "6"
		// What happens when Rspamd is not answering, and this is a real
		// choice with a real cost.
		//
		// "accept" would keep mail flowing while the filter is down. It would
		// also send every outbound message **unsigned**, and an unsigned
		// message cannot be recalled: it is delivered, judged against a DKIM
		// policy it does not satisfy, and filed as spam — and the domain's
		// reputation carries the result for weeks.
		//
		// "tempfail" defers instead. The sender's server retries for days, so
		// nothing is lost; inbound mail waits at the sending server rather
		// than arriving unfiltered. The cost is that a stopped Rspamd stops
		// mail, which the panel reports as a running daemon being down rather
		// than leaving it to be discovered.
		values["milter_default_action"] = "tempfail"
	} else {
		values["smtpd_milters"] = ""
		values["non_smtpd_milters"] = ""
		values["milter_default_action"] = "accept"
	}

	return values
}

// applyMain writes the panel's keys into main.cf through postconf.
//
// Returns the keys that actually changed, so a caller can decide whether a
// reload is needed — and so the audit trail can say what was changed rather
// than that something was.
func (p *Provider) applyMain(ctx context.Context, values map[string]string) ([]string, error) {
	current, err := p.currentMain(ctx)
	if err != nil {
		return nil, err
	}

	// Sorted so the sequence of postconf calls is deterministic, which makes a
	// failure reproducible and the audit record stable.
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var changed []string
	for _, key := range keys {
		want := values[key]
		if existing, found := current[key]; found && existing == want {
			continue
		}
		// One key per call. postconf accepts several, but a batch that fails
		// halfway leaves no record of which half applied.
		result, err := p.runner.Run(ctx, CommandPostconf, "-e", key+"="+want)
		if err != nil {
			return changed, wrap("set the Postfix setting "+key, err)
		}
		if !result.Succeeded() {
			return changed, fmt.Errorf("set the Postfix setting %s: %s",
				key, firstLine(result.Stderr, result.Stdout))
		}
		changed = append(changed, key)
	}
	return changed, nil
}

// currentMain reads the settings this host has explicitly set.
//
// `postconf -n` rather than `postconf`: the full listing is several hundred
// built-in defaults, and the panel only ever compares against values somebody
// set. A key the panel wants at its default value is reported as absent here,
// so it is set explicitly the first time and skipped afterwards.
func (p *Provider) currentMain(ctx context.Context) (map[string]string, error) {
	result, err := p.runner.Run(ctx, CommandPostconf, "-n")
	if err != nil {
		return nil, wrap("read the Postfix configuration", err)
	}
	if !result.Succeeded() {
		return nil, fmt.Errorf("read the Postfix configuration: %s",
			firstLine(result.Stderr, result.Stdout))
	}
	return parsePostconf(result.Stdout), nil
}

// parsePostconf reads postconf's "key = value" output.
//
// Continuation lines — a value wrapped onto the next line, which postconf
// indents — are joined onto the value above. Without that, a long restriction
// list reads as several keys with no names, and the panel would rewrite it on
// every reconcile because it never matched.
func parsePostconf(output string) map[string]string {
	values := map[string]string{}
	lastKey := ""
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && lastKey != "" {
			values[lastKey] = strings.TrimSpace(values[lastKey] + " " + strings.TrimSpace(line))
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		values[key] = strings.TrimSpace(value)
		lastKey = key
	}
	return values
}

// A submission service: the port a customer's mail client sends through.
type submissionService struct {
	// name is postconf's own identifier for the service, "submission/inet".
	name string
	// definition is the master.cf line.
	definition string
	// overrides are the per-service settings.
	overrides map[string]string
	// port is what it listens on, for the status page.
	port int
}

// submissionServices builds the two submission ports.
//
// Both are offered rather than one, because the two halves of the world need
// different ones: 587 with STARTTLS is the standard, and 465 with implicit TLS
// is what every phone's mail app defaults to and what the standards caught up
// with in RFC 8314.
//
// The one rule both share is the one that matters: **only an authenticated
// client may connect at all.** smtpd_client_restrictions rejects everything
// else outright, so these ports cannot be used to deliver mail to this host's
// own domains either — which is deliberate. A port that both accepts anonymous
// mail and offers AUTH is a port worth attacking; one that refuses to talk to
// anybody who has not authenticated is not.
func submissionServices(settings Settings) []submissionService {
	shared := map[string]string{
		"smtpd_sasl_auth_enable": "yes",
		// Only authenticated clients, and no exceptions — not even
		// mynetworks. A process on this host that wants to send mail uses the
		// local sendmail interface, which does not come through here.
		"smtpd_client_restrictions": "permit_sasl_authenticated,reject",
		"smtpd_relay_restrictions":  "permit_sasl_authenticated,reject",
		// Tells the milter that this message originated here rather than
		// arriving from outside, which is what makes Rspamd sign it instead of
		// scoring it.
		"milter_macro_daemon_name": "ORIGINATING",
		// Separate log lines for submission, so "somebody is guessing
		// passwords" and "somebody is sending us mail" are distinguishable in
		// the log viewer and to fail2ban.
		"syslog_name": "postfix/submission",
	}

	starttls := map[string]string{}
	for key, value := range shared {
		starttls[key] = value
	}
	// Mandatory encryption on 587. Not "may": a submission port that accepts a
	// password in the clear is one where a client misconfigured once leaks the
	// password on every send, invisibly, forever.
	starttls["smtpd_tls_security_level"] = "encrypt"

	wrapped := map[string]string{}
	for key, value := range shared {
		wrapped[key] = value
	}
	wrapped["smtpd_tls_wrappermode"] = "yes"
	wrapped["smtpd_tls_security_level"] = "encrypt"
	wrapped["syslog_name"] = "postfix/submissions"

	services := []submissionService{
		{
			name:       "submission/inet",
			definition: "submission inet n - n - - smtpd",
			overrides:  starttls,
			port:       587,
		},
		{
			name:       "submissions/inet",
			definition: "submissions inet n - n - - smtpd",
			overrides:  wrapped,
			port:       465,
		},
	}

	// Without a certificate there is nothing to encrypt with, and both of these
	// demand encryption. An operator who has explicitly turned off the
	// requirement gets 587 in the clear — their decision, made visibly — and
	// never gets 465, which cannot exist without TLS by definition.
	if settings.TLSCertificate == "" || settings.TLSKey == "" {
		if settings.RequireTLS {
			return nil
		}
		plain := services[0]
		plain.overrides = map[string]string{}
		for key, value := range shared {
			plain.overrides[key] = value
		}
		plain.overrides["smtpd_tls_security_level"] = "none"
		return []submissionService{plain}
	}
	return services
}

// applyServices writes the submission ports into master.cf.
//
// Through `postconf -M` and `-P`, which is Postfix's own interface for exactly
// this: master.cf is a positional, whitespace-significant table, and a panel
// that edited it as text would be one bad substitution away from a mail server
// that will not start.
func (p *Provider) applyServices(ctx context.Context, services []submissionService) error {
	wanted := map[string]bool{}
	for _, service := range services {
		wanted[service.name] = true

		result, err := p.runner.Run(ctx, CommandPostconf, "-M", service.name+"="+service.definition)
		if err != nil {
			return wrap("add the "+service.name+" service", err)
		}
		if !result.Succeeded() {
			return fmt.Errorf("add the %s service: %s",
				service.name, firstLine(result.Stderr, result.Stdout))
		}

		keys := make([]string, 0, len(service.overrides))
		for key := range service.overrides {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			setting := service.name + "/" + key + "=" + service.overrides[key]
			result, err := p.runner.Run(ctx, CommandPostconf, "-P", setting)
			if err != nil {
				return wrap("configure "+service.name, err)
			}
			if !result.Succeeded() {
				return fmt.Errorf("configure %s: %s",
					service.name, firstLine(result.Stderr, result.Stdout))
			}
		}
	}

	// Remove any submission port the panel is no longer offering.
	//
	// This is what makes turning TLS off actually close port 465, rather than
	// leaving it listening with a certificate the panel has stopped managing.
	for _, name := range []string{"submission/inet", "submissions/inet"} {
		if wanted[name] {
			continue
		}
		// -X removes the service; a service that is not there is not an error,
		// so the failure is reported and not fatal — an inability to remove a
		// port must not stop the rest of a reconcile that is otherwise
		// correct.
		if result, err := p.runner.Run(ctx, CommandPostconf, "-MX", name); err != nil {
			p.log.Warn("could not remove a submission service", "service", name, "error", err)
		} else if !result.Succeeded() {
			p.log.Debug("submission service was not present to remove", "service", name)
		}
	}
	return nil
}

// reloadPostfix asks the server to re-read its configuration.
//
// A reload rather than a restart: Postfix's reload genuinely re-reads
// everything, including the lookup tables, and a restart would drop connections
// that are mid-delivery. The one thing it does not pick up is a change to
// inet_interfaces, which the panel does not change after the first reconcile.
func (p *Provider) reloadPostfix(ctx context.Context) error {
	if p.services == nil {
		return fmt.Errorf("this host has no service manager to reload the mail server with")
	}
	if !p.services.Running(ctx, DaemonPostfix) {
		// Not running is not a failure to reload: the next start reads the new
		// configuration. Saying so is better than reporting an error for a
		// server the operator has deliberately stopped.
		return nil
	}
	return p.services.Reload(ctx, DaemonPostfix)
}

// queueDepth reports how many messages are waiting and how old the oldest is.
//
// The oldest matters more than the count, and the two together are what
// separates the two states that look identical in a number: a busy server with
// two hundred messages moving through, and a broken one with two hundred
// messages that have been there since Tuesday.
func (p *Provider) queueDepth(ctx context.Context) (count, oldestSeconds int) {
	if !p.runner.Available(CommandPostqueue) {
		return 0, 0
	}
	result, err := p.runner.Run(ctx, CommandPostqueue, "-j")
	if err != nil || !result.Succeeded() {
		return 0, 0
	}
	return parseQueue(result.Stdout)
}
