package ftp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jothost/panel/shared/validate"
)

// Reconciling the host with what the panel has recorded.
//
// The panel's database is the record of which FTP accounts exist; this package
// makes the host match it. That direction is deliberate and it is what makes
// the operation idempotent in the sense CLAUDE.md section 17 asks for: applying
// the same desired state twice is a no-op, and applying it to a host that has
// drifted — an account deleted by hand, a quota changed in the file — puts it
// back.
//
// The alternative, a set of per-account verbs each of which edits one thing,
// was rejected for a specific reason rather than on taste. The drop-in has to
// name every read-only account in one <Limit> block, so *any* change to any
// account rewrites a file derived from *all* of them. A per-account verb would
// therefore need the Agent to keep its own record of who is read-only, and that
// record would be a second source of truth about access — free to disagree with
// the panel's, and with nothing to notice when it did.

// Desired is the state the panel wants this host to be in.
type Desired struct {
	// Settings are the server-wide options.
	Settings Settings
	// Users is the complete set of accounts. An account on the host and not in
	// this list is removed, so a partial list would delete the rest.
	Users []User
	// Passwords holds new passwords by account name. Only accounts being
	// created or reset appear here: the panel does not store FTP passwords, so
	// an existing account's is not something it could send.
	Passwords map[string]string
}

// ReconcileResult reports what changed.
type ReconcileResult struct {
	ApplyResult
	Created []string `json:"created,omitempty"`
	Updated []string `json:"updated,omitempty"`
	Removed []string `json:"removed,omitempty"`
	// NeedPassword names accounts the panel has recorded that this host does
	// not have and could not be created, because nothing holds their password.
	// They are skipped rather than failing the whole change; setting a new
	// password puts each one back.
	NeedPassword []string `json:"need_password,omitempty"`
}

// Reconcile makes the host match the panel.
//
// Accounts first, configuration second. The drop-in names the read-only
// accounts, so it has to be rendered from the set that actually ended up on
// disk — writing it first would produce a file describing accounts that failed
// to be created.
func (p *Provider) Reconcile(ctx context.Context, desired Desired) (ReconcileResult, error) {
	var result ReconcileResult
	if !p.Available() {
		return result, ErrUnavailable
	}
	if err := validateDesired(desired); err != nil {
		return result, err
	}
	if err := p.checkSupported(ctx, desired); err != nil {
		return result, err
	}
	if err := p.ensureDirs(); err != nil {
		return result, err
	}

	existing, err := p.Accounts()
	if err != nil {
		return result, err
	}
	present := make(map[string]Account, len(existing))
	for _, account := range existing {
		present[account.Name] = account
	}

	wanted := make(map[string]bool, len(desired.Users))
	for _, user := range desired.Users {
		wanted[user.Name] = true

		current, exists := present[user.Name]
		if !exists {
			password, given := desired.Passwords[user.Name]
			if !given {
				// An account the panel has recorded that the host does not
				// have, and no password to create it with.
				//
				// Reported and skipped rather than treated as an error. It
				// happens when the host's password file is lost — a rebuilt
				// machine, a restore from a backup taken before the account
				// existed — and failing here would mean one unusable account
				// blocked every later FTP change on the host, permanently,
				// because the panel does not keep passwords and so can never
				// supply one on its own.
				//
				// Inventing one is not the alternative: that would create an
				// account nobody can use and nobody knows is unusable. Saying
				// so is, and the operator sets a new password.
				result.NeedPassword = append(result.NeedPassword, user.Name)
				continue
			}
			if err := p.CreateAccount(ctx, user, password); err != nil {
				return result, err
			}
			result.Created = append(result.Created, user.Name)
		} else if current.Home != user.Home {
			// The mapping is fixed at creation; only the home can move, and
			// moving it is what changes which files an account can reach.
			if err := p.SetHome(ctx, user.Name, user.Home); err != nil {
				return result, err
			}
			result.Updated = append(result.Updated, user.Name)
		}

		if password, given := desired.Passwords[user.Name]; given && exists {
			if err := p.SetPassword(ctx, user.Name, password); err != nil {
				return result, err
			}
			result.Updated = appendOnce(result.Updated, user.Name)
		}

		if err := p.SetQuota(ctx, user.Name, user.QuotaMB); err != nil {
			return result, err
		}

		// Suspension is reconciled rather than toggled: the host is made to
		// agree with the record, so an account locked by hand is unlocked again
		// if the panel says it should be usable.
		if current, err := p.Account(user.Name); err == nil && current.Locked != user.Suspended {
			if err := p.SetLocked(ctx, user.Name, user.Suspended); err != nil {
				return result, err
			}
			result.Updated = appendOnce(result.Updated, user.Name)
		}
	}

	// Anything on the host the panel does not know about. This is what makes a
	// deleted website's accounts actually go away, and what removes an account
	// somebody added to the file by hand.
	for _, account := range existing {
		if wanted[account.Name] {
			continue
		}
		if err := p.DeleteAccount(ctx, account.Name); err != nil {
			return result, err
		}
		// Its quota record goes too, or the next account to reuse the name
		// would inherit a limit nobody set for it.
		if err := p.ClearQuota(ctx, account.Name); err != nil {
			return result, err
		}
		if err := p.ClearTally(ctx, account.Name); err != nil {
			p.log.Warn("could not clear an FTP quota tally",
				"account", account.Name, "error", err.Error())
		}
		result.Removed = append(result.Removed, account.Name)
	}

	applied, err := p.Apply(ctx, desired.Settings, desired.Users)
	result.ApplyResult = applied
	if err != nil {
		return result, err
	}

	sort.Strings(result.Created)
	sort.Strings(result.Updated)
	sort.Strings(result.Removed)
	sort.Strings(result.NeedPassword)
	return result, nil
}

// checkSupported refuses settings this build of the server cannot honour.
//
// It refuses rather than writes them, and that is the whole point. proftpd
// skips an <IfModule> block whose module is absent *without an error*, so
// writing the FTPS settings on a host with no mod_tls produces a configuration
// that validates, a daemon that restarts, a panel that says FTPS is on, and a
// server that accepts passwords in clear text. The same is true of a quota
// nothing enforces, where the cost is a disk that fills instead.
func (p *Provider) checkSupported(ctx context.Context, desired Desired) error {
	modules := p.Modules(ctx)

	if desired.Settings.TLSCertificate != "" && !modules["mod_tls"] {
		return fmt.Errorf(
			"%w: this FTP server has no TLS module, so FTPS cannot be offered — "+
				"install proftpd's mod_tls package", ErrNoCertificate)
	}

	if !modules["mod_quotatab"] || !modules["mod_quotatab_file"] {
		for _, user := range desired.Users {
			if user.QuotaMB > 0 {
				return fmt.Errorf(
					"this FTP server has no quota module, so a disk limit for %q "+
						"would not be enforced — install proftpd's mod_quotatab "+
						"and mod_quotatab_file packages", user.Name)
			}
		}
	}
	return nil
}

// validateDesired checks the whole request before any of it is written.
//
// All of it up front rather than as each account is reached: a set that fails
// halfway leaves the host in a state that is neither what it was nor what was
// asked for, and the accounts already written would have to be unwound.
func validateDesired(desired Desired) error {
	if err := checkSettings(desired.Settings); err != nil {
		return err
	}

	seen := map[string]bool{}
	for _, user := range desired.Users {
		if seen[user.Name] {
			return fmt.Errorf("%w: %q appears twice", ErrUserExists, user.Name)
		}
		seen[user.Name] = true

		if err := (&Provider{}).checkUser(user); err != nil {
			return err
		}
	}
	for name := range desired.Passwords {
		if !seen[name] {
			return fmt.Errorf("%w: a password was given for %q, which is not in the list",
				ErrUnknownUser, name)
		}
	}
	return nil
}

func appendOnce(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

// checkSettings validates the server-wide options.
func checkSettings(s Settings) error {
	if err := validate.PassivePortRange(s.PassiveFrom, s.PassiveTo); err != nil {
		return err
	}
	// A certificate without a key, or the other way round, would render a TLS
	// block proftpd refuses — caught here so the message names the missing
	// half rather than quoting a parser.
	if (s.TLSCertificate == "") != (s.TLSKey == "") {
		return fmt.Errorf("%w: FTPS needs both a certificate and its key", ErrNoCertificate)
	}
	if s.RequireTLS && s.TLSCertificate == "" {
		return fmt.Errorf("%w: it cannot be required with nothing to present", ErrNoCertificate)
	}
	for _, path := range []string{s.TLSCertificate, s.TLSKey} {
		if path != "" && !strings.HasPrefix(path, "/") {
			return fmt.Errorf("%w: %q is not an absolute path", ErrNoCertificate, path)
		}
	}
	if s.MaxClients < 0 {
		return fmt.Errorf("the FTP client limit cannot be negative")
	}
	return nil
}
