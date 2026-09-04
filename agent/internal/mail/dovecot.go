package mail

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jothost/panel/shared/crypt"
)

// Where Postfix expects to find Dovecot's two sockets.
//
// Inside Postfix's queue directory, because the SMTP daemon runs chrooted into
// it on most distributions — a socket anywhere else is one it cannot see, and
// the failure is "Connection refused" from a socket that exists.
const (
	postfixQueueDir  = "/var/spool/postfix"
	authSocketPath   = postfixQueueDir + "/private/auth"
	lmtpSocketPath   = postfixQueueDir + "/private/dovecot-lmtp"
	postfixOwnerUser = "postfix"
)

// buildDovecotConf renders the one Dovecot file the panel owns.
//
// Whole, every time, from the panel's own record. The file is numbered 99 so it
// is included last: Dovecot takes the *last* value for a repeated setting, so
// this is the file that decides. See the package comment for why the FTP
// drop-in is numbered the other way round.
func buildDovecotConf(settings Settings, paths Paths, uid, gid int) []byte {
	var out strings.Builder

	out.WriteString("# Dovecot settings owned by JotHost Panel.\n")
	out.WriteString("#\n")
	out.WriteString("# This file is generated and rewritten whole on every change. Edits\n")
	out.WriteString("# made here are lost. It is included last, so what it sets is what\n")
	out.WriteString("# Dovecot runs — a setting here overrides the distribution's.\n")
	out.WriteString("#\n")
	out.WriteString("# To override the panel, use a file that sorts after this one.\n\n")

	// The protocols. Every one of them is a decision:
	//
	//   imap  — what everybody actually uses.
	//   pop3  — what a surprising number of people still use, and turning it
	//           off silently is how a customer's mail stops arriving on the
	//           machine in their shop.
	//   lmtp  — how Postfix hands mail over. Without it nothing is delivered.
	//   sieve — the managesieve service, which is what makes an autoresponder
	//           editable rather than a file only the panel can write.
	out.WriteString("protocols = imap pop3 lmtp sieve\n\n")

	out.WriteString("# Where mail lives. One directory per domain, one per mailbox inside\n")
	out.WriteString("# it, so a domain can be moved or archived as a unit.\n")
	fmt.Fprintf(&out, "mail_home = %s/%%d/%%n\n", paths.MailRoot)
	fmt.Fprintf(&out, "mail_location = maildir:%s/%%d/%%n\n", paths.MailRoot)
	fmt.Fprintf(&out, "mail_uid = %d\n", uid)
	fmt.Fprintf(&out, "mail_gid = %d\n\n", gid)

	// The one account allowed to hold mail, named exactly.
	//
	// Dovecot refuses to open a mailbox for a uid below first_valid_uid, which
	// defaults to 500 — and the mail account is a *system* account, so its uid
	// is below that on every distribution. The symptom is delivery deferred
	// with "Mail access for users with UID n not permitted", which reads as a
	// permission problem on the Maildir and is not one.
	//
	// Pinning both ends to the mail account is stricter than the default as
	// well as correct: Dovecot will now refuse to run mail as any other
	// account, including root.
	fmt.Fprintf(&out, "first_valid_uid = %d\n", uid)
	fmt.Fprintf(&out, "last_valid_uid = %d\n", uid)
	fmt.Fprintf(&out, "first_valid_gid = %d\n", gid)
	fmt.Fprintf(&out, "last_valid_gid = %d\n\n", gid)

	out.WriteString("# The user database is one file, and both SMTP and IMAP authenticate\n")
	out.WriteString("# against it — which is what makes changing a password a single act\n")
	out.WriteString("# rather than two that can disagree.\n")
	out.WriteString("passdb {\n")
	out.WriteString("  driver = passwd-file\n")
	// username_format=%Lu lower-cases the address before the lookup, which is
	// what makes "Sales@example.com" and "sales@example.com" the same mailbox.
	// Without it, half the people who typed their own address with a capital
	// letter cannot log in.
	fmt.Fprintf(&out, "  args = scheme=%s username_format=%%Lu %s\n",
		crypt.Scheme, paths.PasswdFile())
	out.WriteString("}\n\n")
	out.WriteString("userdb {\n")
	out.WriteString("  driver = passwd-file\n")
	fmt.Fprintf(&out, "  args = username_format=%%Lu %s\n", paths.PasswdFile())
	out.WriteString("}\n\n")

	// Authentication mechanisms.
	//
	// PLAIN and LOGIN only, and that is not a weakness here: both send the
	// password over the connection, which is precisely why the connection is
	// required to be encrypted. The challenge-response mechanisms that avoid
	// it — CRAM-MD5, DIGEST-MD5 — need the password stored recoverably, so
	// offering them would mean keeping every customer's mail password in a
	// form that can be read back. That is a worse trade than it looks.
	out.WriteString("auth_mechanisms = plain login\n")
	if settings.RequireTLS && settings.TLSCertificate != "" {
		out.WriteString("disable_plaintext_auth = yes\n\n")
	} else {
		out.WriteString("# No certificate, or the requirement was explicitly turned off:\n")
		out.WriteString("# passwords cross the network readable. The panel reports this.\n")
		out.WriteString("disable_plaintext_auth = no\n\n")
	}

	if settings.TLSCertificate != "" && settings.TLSKey != "" {
		out.WriteString("ssl = yes\n")
		// The "<" is Dovecot's syntax for "read the file at this path". Without
		// it the value is taken as the certificate itself.
		fmt.Fprintf(&out, "ssl_cert = <%s\n", settings.TLSCertificate)
		fmt.Fprintf(&out, "ssl_key = <%s\n", settings.TLSKey)
		out.WriteString("ssl_min_protocol = TLSv1.2\n")
		out.WriteString("ssl_prefer_server_ciphers = yes\n\n")
	} else {
		out.WriteString("ssl = no\n\n")
	}

	out.WriteString("# The two sockets Postfix talks to Dovecot through. They are inside\n")
	out.WriteString("# Postfix's queue directory because its SMTP daemon runs chrooted\n")
	out.WriteString("# into it: a socket anywhere else is one it cannot reach, and the\n")
	out.WriteString("# error it gives is about a connection rather than about a path.\n")
	out.WriteString("#\n")
	out.WriteString("# Named absolutely. A relative path here is resolved against Dovecot's\n")
	out.WriteString("# own base directory rather than Postfix's queue, and Dovecot then\n")
	out.WriteString("# refuses to start at all because that directory does not exist.\n")
	out.WriteString("service auth {\n")
	fmt.Fprintf(&out, "  unix_listener %s {\n", authSocketPath)
	out.WriteString("    mode = 0660\n")
	fmt.Fprintf(&out, "    user = %s\n", postfixOwnerUser)
	fmt.Fprintf(&out, "    group = %s\n", postfixOwnerUser)
	out.WriteString("  }\n")
	out.WriteString("}\n\n")

	out.WriteString("service lmtp {\n")
	fmt.Fprintf(&out, "  unix_listener %s {\n", lmtpSocketPath)
	out.WriteString("    mode = 0600\n")
	fmt.Fprintf(&out, "    user = %s\n", postfixOwnerUser)
	fmt.Fprintf(&out, "    group = %s\n", postfixOwnerUser)
	out.WriteString("  }\n")
	out.WriteString("}\n\n")

	// Quota and Sieve.
	//
	// The plugins are attached per protocol rather than globally because they
	// are not wanted everywhere: Sieve runs at delivery, so it belongs to LMTP,
	// and imap_quota is what lets a mail client show the customer how full
	// their mailbox is instead of failing at a threshold they cannot see.
	out.WriteString("mail_plugins = $mail_plugins quota\n\n")
	out.WriteString("protocol lmtp {\n")
	out.WriteString("  mail_plugins = $mail_plugins quota sieve\n")
	// Required by Sieve, and by every bounce Dovecot generates. Without it
	// delivery fails with an error about a missing postmaster address, which
	// reads as a configuration problem rather than as the one-line omission it
	// is.
	fmt.Fprintf(&out, "  postmaster_address = postmaster@%s\n", postmasterDomain(settings.Hostname))
	out.WriteString("}\n\n")
	out.WriteString("protocol imap {\n")
	out.WriteString("  mail_plugins = $mail_plugins quota imap_quota\n")
	out.WriteString("}\n\n")

	out.WriteString("plugin {\n")
	out.WriteString("  # Maildir++ quota. The per-mailbox limit comes from the user\n")
	out.WriteString("  # database, so it is set per mailbox rather than per host.\n")
	out.WriteString("  quota = maildir:User quota\n")
	out.WriteString("  quota_rule = *:storage=0\n")
	out.WriteString("\n")
	out.WriteString("  # A full mailbox is refused at delivery time with a permanent\n")
	out.WriteString("  # error, so the sender is told. The alternative — accepting the\n")
	out.WriteString("  # message and dropping it — loses mail silently, which is the\n")
	out.WriteString("  # worst thing a mail server can do.\n")
	out.WriteString("  quota_status_success = DUNNO\n")
	out.WriteString("  quota_status_nouser = DUNNO\n")
	out.WriteString("  quota_status_overquota = 552 5.2.2 Mailbox is full\n")
	out.WriteString("\n")
	out.WriteString("  # Autoresponders. One script per address, written by the panel\n")
	out.WriteString("  # and compiled before it is installed — an uncompilable script is\n")
	out.WriteString("  # one Dovecot skips in silence at delivery time.\n")
	fmt.Fprintf(&out, "  sieve = file:%s/%%u.sieve\n", paths.SieveDir())
	out.WriteString("}\n\n")

	// The special-use mailboxes.
	//
	// Named here so that every client agrees which folder is which. Without
	// the \Sent and \Trash attributes a mail client invents its own — so mail
	// sent from a phone lands in "Sent Messages" and mail sent from webmail
	// lands in "Sent", and the customer has two halves of their sent mail.
	out.WriteString("namespace inbox {\n")
	out.WriteString("  inbox = yes\n")
	for _, box := range []struct{ name, use string }{
		{"Drafts", "\\Drafts"},
		{"Sent", "\\Sent"},
		{"Trash", "\\Trash"},
		{"Junk", "\\Junk"},
		{"Archive", "\\Archive"},
	} {
		fmt.Fprintf(&out, "  mailbox %s {\n", box.name)
		fmt.Fprintf(&out, "    special_use = %s\n", box.use)
		// auto=subscribe creates the folder on first login and subscribes the
		// client to it, so it is visible rather than merely present.
		out.WriteString("    auto = subscribe\n")
		out.WriteString("  }\n")
	}
	out.WriteString("}\n")

	return []byte(out.String())
}

// postmasterDomain picks the domain half of the postmaster address.
//
// The mail hostname, which is a fully qualified name by validation. If it
// somehow is not, "localhost" is used rather than an address with no domain:
// Dovecot refuses to start on a malformed postmaster_address, and a mail server
// that will not start is a worse outcome than a postmaster address nobody
// writes to.
func postmasterDomain(hostname string) string {
	trimmed := strings.TrimSpace(hostname)
	if trimmed == "" || !strings.Contains(trimmed, ".") {
		return "localhost"
	}
	return trimmed
}

// Quota asks Dovecot how full a mailbox is.
//
// Dovecot rather than the filesystem, and the difference is not pedantry: the
// Maildir on disk includes index files and the messages a client has deleted
// but not expunged, so a directory size is consistently larger than the number
// the customer's mail client is showing them. A panel reporting one and a
// client reporting the other is a support call.
func (p *Provider) Quota(ctx context.Context, address string) (usedMB int, limitMB int, err error) {
	if !p.runner.Available(CommandDoveadm) {
		return 0, 0, fmt.Errorf("%w: this host has no doveadm", ErrUnavailable)
	}
	if strings.ContainsAny(address, " \t\r\n") {
		return 0, 0, fmt.Errorf("a mailbox address may not contain whitespace")
	}

	result, err := p.runner.Run(ctx, CommandDoveadm, "-f", "tab", "quota", "get", "-u", address)
	if err != nil {
		return 0, 0, wrap("read the quota for "+address, err)
	}
	if !result.Succeeded() {
		// A mailbox nobody has logged into yet has no Maildir, and doveadm
		// says so. That is a real state and not an error: it is a new mailbox
		// with nothing in it.
		return 0, 0, nil
	}
	return parseQuota(result.Stdout)
}

// parseQuota reads doveadm's tab-separated quota output.
//
// The interesting column is "STORAGE", whose value and limit are in kibibytes.
// A limit of "-" means unlimited, which is reported as zero — the same
// convention the rest of the panel uses.
func parseQuota(output string) (usedMB, limitMB int, err error) {
	// The output is a header line and one row per resource:
	//
	//   Quota name<TAB>Type<TAB>Value<TAB>Limit<TAB>%
	//   User quota<TAB>STORAGE<TAB>12<TAB>102400<TAB>0
	//   User quota<TAB>MESSAGE<TAB>3<TAB>-<TAB>0
	//
	// Only STORAGE is wanted. The MESSAGE row counts messages rather than
	// bytes, and reading it as a size would report a mailbox holding three
	// large messages as three kilobytes.
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) < 4 || strings.TrimSpace(fields[1]) != "STORAGE" {
			continue
		}
		used, convErr := strconv.Atoi(strings.TrimSpace(fields[2]))
		if convErr != nil {
			continue
		}
		limit := 0
		if trimmed := strings.TrimSpace(fields[3]); trimmed != "-" && trimmed != "" {
			if parsed, convErr := strconv.Atoi(trimmed); convErr == nil {
				limit = parsed / 1024
			}
		}
		return used / 1024, limit, nil
	}
	return 0, 0, nil
}
