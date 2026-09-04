package mail

// The request boundary for everything in this package.
//
// A reconcile request carries **the mail this host should serve**, as the panel
// has recorded it: domains that have been through validate.MailDomain, local
// parts that have been through validate.MailLocalPart, password hashes that are
// already hashes, and numbers.
//
// What it cannot carry is a configuration directive, a file path, or a
// plaintext password. The paths are all derived here from names that have been
// checked to contain no separator; the only paths that arrive from outside are
// the TLS certificate and key, which are checked to be absolute and then
// checked to exist by the daemons' own validators before either is restarted.

// Desired is the complete mail configuration this host should have.
//
// Complete, not a delta. Anything on the host that is not in here is removed,
// which is what makes a deleted domain's mailboxes stop authenticating rather
// than lingering as a login nothing on the panel lists.
type Desired struct {
	Settings Settings `json:"settings"`
	Domains  []Domain `json:"domains"`
}

// Settings are the server-wide options.
type Settings struct {
	// Enabled is whether this host serves mail at all. False tears the panel's
	// configuration back down rather than leaving a server listening on 25
	// that the panel has stopped managing.
	Enabled bool `json:"enabled"`

	// Hostname is what the server calls itself in EHLO and in Received
	// headers.
	Hostname string `json:"hostname"`

	// TLSCertificate and TLSKey are the material the server presents. Empty
	// means no TLS, which the panel reports rather than hides.
	TLSCertificate string `json:"tls_certificate"`
	TLSKey         string `json:"tls_key"`

	// RequireTLS refuses a password over an unencrypted connection.
	//
	// It never refuses *mail* over one: port 25 has to accept plain
	// connections from other mail servers or this host receives nothing. The
	// distinction is the same one Phase 20 arrived at for its own SMTP
	// sender — an unencrypted connection may carry a message, and may never
	// carry a password.
	RequireTLS bool `json:"require_tls"`

	SpamEnabled     bool `json:"spam_enabled"`
	SpamRejectScore int  `json:"spam_reject_score"`
	VirusEnabled    bool `json:"virus_enabled"`

	// MaxMessageMB is the largest message the server accepts.
	MaxMessageMB int `json:"max_message_mb"`
}

// Domain is one domain this host accepts mail for.
type Domain struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`

	// CatchAll is where mail for an address that does not exist goes. Empty —
	// the default — refuses it, which is both correct and the only way to
	// avoid becoming a backscatter source.
	CatchAll string `json:"catch_all"`

	// DKIMSelector names the key this domain signs with. Empty means the panel
	// has no key recorded for it, and the domain's mail goes out unsigned.
	DKIMSelector string `json:"dkim_selector"`

	Mailboxes []Mailbox `json:"mailboxes"`
	Aliases   []Alias   `json:"aliases"`
}

// Mailbox is one address that receives mail and can log in.
type Mailbox struct {
	LocalPart string `json:"local_part"`
	// PasswordHash is already a hash, in Dovecot's schemed form. A plaintext
	// password never crosses the socket to the Agent: the API hashes it at the
	// HTTP boundary, so the privileged process never holds one.
	PasswordHash string `json:"password_hash"`
	QuotaMB      int    `json:"quota_mb"`
	Active       bool   `json:"active"`

	// Autoresponder is the vacation reply, if one is set.
	Autoresponder *Autoresponder `json:"autoresponder,omitempty"`
}

// Address returns the mailbox's full address within a domain.
func (m Mailbox) Address(domain string) string { return m.LocalPart + "@" + domain }

// Alias forwards mail from one local part to another address.
type Alias struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Active      bool   `json:"active"`
}

// Autoresponder is a vacation reply.
type Autoresponder struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
	// IntervalDays is how long before the same sender gets another copy.
	IntervalDays int  `json:"interval_days"`
	Active       bool `json:"active"`
}

// Result is what a reconcile did.
type Result struct {
	// Domains, Mailboxes and Aliases are what the host now serves.
	Domains   int `json:"domains"`
	Mailboxes int `json:"mailboxes"`
	Aliases   int `json:"aliases"`

	// MapType is the lookup table format in use, which differs between builds
	// of Postfix. Reported because "why is my alias not working" has a
	// different answer for an indexed table than for a text one.
	MapType string `json:"map_type"`

	// Reloaded reports whether the daemons were restarted. A reconcile that
	// changed nothing does not restart them, because a restart drops every
	// connected IMAP client.
	Reloaded bool `json:"reloaded"`

	// Signing lists the domains that have a usable private key on this host.
	//
	// It is a list rather than a count because it is half of the phase's
	// central comparison: the API holds the other half, which is what DNS
	// actually publishes, and a domain in one list and not the other is a
	// domain whose mail is being treated as unsigned by everybody who receives
	// it.
	Signing []string `json:"signing"`

	// Warnings are things that are working now and will not stay working, or
	// are configured and are not doing what the operator thinks. They are not
	// errors: the reconcile succeeded.
	Warnings []string `json:"warnings,omitempty"`
}

// Status is what the panel shows about this host's mail server.
type Status struct {
	// Available reports whether the panel can manage mail here at all.
	Available bool `json:"available"`
	// Reason says why not, when Available is false.
	Reason string `json:"reason,omitempty"`
	// CanInstall reports whether the panel could install a mail server.
	CanInstall bool `json:"can_install"`

	// The three daemons, each reported separately.
	//
	// Separately because they fail separately and the failures are completely
	// different: Postfix down means no mail is accepted, Dovecot down means it
	// is accepted and nobody can read it, Rspamd down means it is accepted,
	// readable, and unfiltered — and, because Rspamd is what signs, unsigned.
	Postfix Daemon `json:"postfix"`
	Dovecot Daemon `json:"dovecot"`
	Rspamd  Daemon `json:"rspamd"`

	// Antivirus is ClamAV, reported through Rspamd because that is what
	// actually calls it.
	Antivirus Daemon `json:"antivirus"`

	// Hostname is what the server currently calls itself, read back from
	// Postfix rather than from the panel's record — the two disagreeing is
	// exactly the drift this reports.
	Hostname string `json:"hostname"`

	// TLS reports whether the server presents a certificate, and what is wrong
	// if it does not.
	TLS TLSStatus `json:"tls"`

	// Ports lists the SMTP services Postfix is configured to run and whether
	// each is actually listening.
	Ports []PortStatus `json:"ports"`

	// QueueLength is how many messages are waiting. A queue that is growing is
	// the earliest sign that delivery has stopped working.
	QueueLength int `json:"queue_length"`
	// QueueOldest is the age in seconds of the oldest queued message. A long
	// queue of new mail is a busy server; one message stuck for three days is
	// a broken one, and the count alone cannot tell them apart.
	QueueOldest int `json:"queue_oldest_seconds"`

	// OpenRelay reports the answer to the only question that can take every
	// customer on this host off the internet. See Provider.relayCheck.
	OpenRelay RelayStatus `json:"open_relay"`

	// Signing lists domains with a private key on this host.
	Signing []string `json:"signing"`

	// MapType is the lookup table format in use.
	MapType string `json:"map_type"`

	// Warnings are host-level problems the panel found while looking.
	Warnings []string `json:"warnings,omitempty"`
}

// Daemon is one piece of the mail server.
type Daemon struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Version   string `json:"version,omitempty"`
	// Detail says what is wrong, when something is.
	Detail string `json:"detail,omitempty"`
}

// TLSStatus reports what the mail server presents to a connecting client.
type TLSStatus struct {
	Configured bool `json:"configured"`
	// CertificatePath is where the material is, so a page can say which
	// website's certificate is in use.
	CertificatePath string `json:"certificate_path,omitempty"`
	// NotAfter is when it expires, as an RFC 3339 string. A mail server's
	// certificate expiring is quieter than a website's: browsers shout, and
	// mail clients mostly just stop syncing.
	NotAfter string `json:"not_after,omitempty"`
	// Detail says what is wrong.
	Detail string `json:"detail,omitempty"`
}

// PortStatus is one SMTP or IMAP service.
type PortStatus struct {
	Name string `json:"name"`
	Port int    `json:"port"`
	// Configured reports whether the server is set up to run it.
	Configured bool `json:"configured"`
	// Listening reports whether something is actually bound to it. The two
	// differ when a daemon failed to start, which is the case a page has to be
	// able to show.
	Listening bool `json:"listening"`
	// RequiresTLS reports whether a password may cross this port unencrypted.
	RequiresTLS bool `json:"requires_tls"`
}

// RelayStatus is the answer to "will this server carry a stranger's mail".
type RelayStatus struct {
	// Checked reports whether the panel was able to ask.
	Checked bool `json:"checked"`
	// Open is the answer. True is an emergency: a host that relays is on a
	// blocklist within hours, and every customer on it stops being able to
	// send mail to anybody.
	Open bool `json:"open"`
	// Detail is what the server said when asked.
	Detail string `json:"detail,omitempty"`
}

// DKIMKey is a generated signing key, as the panel records it.
type DKIMKey struct {
	Domain   string `json:"domain"`
	Selector string `json:"selector"`
	// PublicKey is the base64 body of the DNS record, without the "v=DKIM1"
	// wrapper. The Agent does not build the record text: the record is a DNS
	// concern and Phase 13 owns it.
	PublicKey string `json:"public_key"`
	// KeyPath is where the private half lives on this host. Reported so an
	// operator can find it; never its contents.
	KeyPath string `json:"key_path"`
	// Bits is the key size, so a page can say what was generated rather than
	// what the panel assumes it generates.
	Bits int `json:"bits"`
}

// Webmail is what the panel knows about the webmail installation.
type Webmail struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	// Path is where the application was unpacked.
	Path string `json:"path,omitempty"`
	// Detail says what is wrong, when something is.
	Detail string `json:"detail,omitempty"`
}
