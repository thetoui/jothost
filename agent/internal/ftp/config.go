package ftp

import (
	"fmt"
	"sort"
	"strings"
)

// Settings are the server-wide options the panel owns.
type Settings struct {
	// PassiveFrom and PassiveTo bound the ports data connections use.
	//
	// A range is mandatory rather than optional. Without one proftpd picks an
	// ephemeral port per transfer, and on a firewalled host — which is every
	// host this panel manages, because Phase 16 turns the firewall on — every
	// passive transfer then hangs until the client times out. It is the single
	// most common way an FTP server appears to work (the login succeeds) and
	// does nothing (no listing ever arrives).
	PassiveFrom int
	PassiveTo   int

	// TLSCertificate and TLSKey are the material FTPS presents, copied into the
	// panel's own state directory. Empty means FTPS is not offered.
	TLSCertificate string
	TLSKey         string

	// RequireTLS refuses plain-text logins outright.
	//
	// Off by default, and that default is a considered one: turning it on
	// breaks every client configured for plain FTP, at once, with an error
	// message most of them render as "login incorrect". It is an operator's
	// decision on a host where they know who connects.
	RequireTLS bool

	// MasqueradeAddress is the public address proftpd advertises for passive
	// connections. Behind NAT the server otherwise hands the client the
	// address of an interface the client cannot reach, and every transfer
	// stalls after a successful login.
	MasqueradeAddress string

	// MaxClients caps concurrent sessions. Zero means proftpd's own default.
	MaxClients int
}

// User is one virtual FTP account.
type User struct {
	// Name is what the client logs in with.
	Name string
	// UID and GID are the system account the session runs as — the website's
	// own, so uploaded files are owned by what serves them.
	UID int
	GID int
	// Home is the absolute directory the session is confined to. It is
	// resolved by the caller against the website's root; this package writes
	// it and does not compute it.
	Home string
	// ReadOnly withholds every writing verb.
	ReadOnly bool
	// QuotaMB is the account's disk limit, zero for none.
	QuotaMB int
	// Suspended stops the account logging in without deleting it, so an
	// operator who suspects a credential is loose can act now and decide later.
	Suspended bool
}

// Render produces the whole of the panel's drop-in.
//
// The file is generated in full from the panel's state on every change rather
// than edited in place. An edited file is one where a failed edit leaves a
// half-applied configuration, and where two settings that disagree can both be
// present with the winner decided by line order.
func Render(p Paths, s Settings, users []User) string {
	var b strings.Builder

	b.WriteString("# Managed by JotHost Panel. Changes here are overwritten.\n")
	b.WriteString("#\n")
	b.WriteString("# FTP accounts are virtual: they exist in the AuthUserFile below and\n")
	b.WriteString("# nowhere else on this host. Each maps to the system account that owns\n")
	b.WriteString("# its website, so uploads are owned by what serves them.\n\n")

	// The scoreboard has to exist for ftpwho to report anything, and its
	// directory has to exist for proftpd to create it.
	fmt.Fprintf(&b, "ScoreboardFile %s\n\n", p.Scoreboard())

	b.WriteString("# Virtual users only. AuthOrder naming just mod_auth_file means a name in\n")
	b.WriteString("# /etc/passwd cannot be used to log in here: an FTP password must never\n")
	b.WriteString("# be a way to try a system account's credentials.\n")
	fmt.Fprintf(&b, "AuthUserFile %s\n", p.Passwd())
	b.WriteString("AuthOrder mod_auth_file.c\n")
	// The accounts have /sbin/nologin as their shell, deliberately — see
	// users.go — so the shell check has to be off or none of them can log in.
	b.WriteString("RequireValidShell off\n")
	b.WriteString("AuthAliasOnly off\n\n")

	b.WriteString("# Every session is confined to its own home directory. This is the\n")
	b.WriteString("# boundary between one customer's files and another's.\n")
	b.WriteString("DefaultRoot ~\n\n")

	b.WriteString("# Named explicitly rather than left to the distribution, so the log\n")
	b.WriteString("# viewer knows where connections and transfers are recorded instead of\n")
	b.WriteString("# guessing at a path that differs between distributions.\n")
	fmt.Fprintf(&b, "SystemLog %s\n", p.SystemLog())
	fmt.Fprintf(&b, "TransferLog %s\n\n", p.TransferLog())

	fmt.Fprintf(&b, "PassivePorts %d %d\n", s.PassiveFrom, s.PassiveTo)
	if s.MasqueradeAddress != "" {
		fmt.Fprintf(&b, "MasqueradeAddress %s\n", s.MasqueradeAddress)
	}
	if s.MaxClients > 0 {
		fmt.Fprintf(&b, "MaxClients %d\n", s.MaxClients)
	}
	b.WriteString("\n")

	// Read-only accounts, as one deny list rather than a block each: proftpd
	// evaluates <Limit> sections in order and a single section is one rule to
	// reason about.
	readOnly := make([]string, 0, len(users))
	for _, u := range users {
		if u.ReadOnly {
			readOnly = append(readOnly, u.Name)
		}
	}
	if len(readOnly) > 0 {
		sort.Strings(readOnly)
		b.WriteString("# Read-only accounts. WRITE covers every verb that changes anything:\n")
		b.WriteString("# uploads, deletes, renames and directory creation.\n")
		b.WriteString("<Limit WRITE>\n")
		fmt.Fprintf(&b, "  DenyUser %s\n", strings.Join(readOnly, ","))
		b.WriteString("</Limit>\n\n")
	}

	if s.TLSCertificate != "" && s.TLSKey != "" {
		b.WriteString("<IfModule mod_tls.c>\n")
		b.WriteString("  TLSEngine on\n")
		fmt.Fprintf(&b, "  TLSRSACertificateFile %s\n", s.TLSCertificate)
		fmt.Fprintf(&b, "  TLSRSACertificateKeyFile %s\n", s.TLSKey)
		if s.RequireTLS {
			b.WriteString("  # Plain-text logins are refused outright.\n")
			b.WriteString("  TLSRequired on\n")
		} else {
			b.WriteString("  # Offered, not required: turning this on rejects every client still\n")
			b.WriteString("  # configured for plain FTP, and most render that as \"login incorrect\".\n")
			b.WriteString("  TLSRequired off\n")
		}
		// Many clients open the data connection without resuming the control
		// connection's TLS session. Requiring it is correct in theory and
		// breaks FileZilla and curl in practice.
		b.WriteString("  TLSOptions NoSessionReuseRequired\n")
		b.WriteString("</IfModule>\n\n")
	}

	if hasQuota(users) {
		b.WriteString("# Per-account disk limits. The tally table is what has been used and\n")
		b.WriteString("# proftpd updates it as files arrive.\n")
		b.WriteString("<IfModule mod_quotatab_file.c>\n")
		b.WriteString("  QuotaEngine on\n")
		fmt.Fprintf(&b, "  QuotaLimitTable file:%s\n", p.QuotaLimit())
		fmt.Fprintf(&b, "  QuotaTallyTable file:%s\n", p.QuotaTally())
		// Without this an account over quota gets a bare "permission denied"
		// and no idea why the upload failed.
		b.WriteString("  QuotaDisplayUnits Mb\n")
		b.WriteString("  QuotaShowQuotas on\n")
		b.WriteString("</IfModule>\n")
	}

	return b.String()
}

func hasQuota(users []User) bool {
	for _, u := range users {
		if u.QuotaMB > 0 {
			return true
		}
	}
	return false
}
