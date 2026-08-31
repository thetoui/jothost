package ssh

import "fmt"

// Security recommendations.
//
// What this is not: a score. A number between 0 and 100 invites an operator to
// chase the number rather than read the findings, and the findings are the whole
// value — "password authentication is on" means something specific and has a
// specific fix, while "your score is 72" means nothing at all. Phase 15 builds a
// security centre; this contributes findings to it rather than a rating.
//
// Each finding is a fact about this host, the reason it matters, and what to do.
// A recommendation nobody can act on is a recommendation that trains people to
// ignore the list.

// Severities, in the order an operator should read them.
const (
	SeverityHigh = "high"
	SeverityWarn = "warn"
	SeverityInfo = "info"
)

// Finding is one recommendation.
type Finding struct {
	// ID is stable, so a later phase can record that one was dismissed.
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	// Detail says what is true now and why it matters.
	Detail string `json:"detail"`
	// Action says what to do about it, in the panel where possible.
	Action string `json:"action"`
}

// Audit reports what is worth changing about this host's SSH configuration.
func Audit(config Config, accounts []Account) []Finding {
	findings := make([]Finding, 0, 8)

	if !config.Available {
		return findings
	}

	keyed := 0
	for _, account := range accounts {
		keyed += account.Keys
	}

	switch {
	case config.PermitEmptyPasswords:
		findings = append(findings, Finding{
			ID:       "ssh.empty-passwords",
			Severity: SeverityHigh,
			Title:    "Accounts with no password can log in",
			Detail: "PermitEmptyPasswords is on, so any account whose password is " +
				"blank can be logged into from anywhere. This is almost never " +
				"deliberate.",
			Action: "Turn it off.",
		})
	}

	if config.RootLogin == "yes" {
		detail := "PermitRootLogin is \"yes\", so root can log in over the network " +
			"with a password. Root is the one account name every scanner on the " +
			"internet already knows, which makes it the account they spend their " +
			"guesses on."
		findings = append(findings, Finding{
			ID:       "ssh.root-login",
			Severity: SeverityHigh,
			Title:    "Root can log in with a password",
			Detail:   detail,
			Action: "Set it to \"prohibit-password\" to keep root access by key, or " +
				"\"no\" once another administrative account works.",
		})
	}

	if config.PasswordAuthentication {
		severity := SeverityWarn
		action := "Add an SSH key for an administrator, then turn password " +
			"authentication off."
		if keyed == 0 {
			// Saying "turn it off" to a host with no keys is telling somebody to
			// lock themselves out. The order matters, so the advice says it.
			action = "Add an SSH key first — the panel will refuse to turn passwords " +
				"off until one exists, because doing so would leave no way in."
		}
		findings = append(findings, Finding{
			ID:       "ssh.password-authentication",
			Severity: severity,
			Title:    "Passwords can be used to log in",
			Detail: "PasswordAuthentication is on. A password can be guessed at a " +
				"few thousand attempts a second by anyone who can reach the port; " +
				"a key cannot.",
			Action: action,
		})
	}

	if !config.PubkeyAuthentication {
		findings = append(findings, Finding{
			ID:       "ssh.pubkey-authentication",
			Severity: SeverityHigh,
			Title:    "Key authentication is switched off",
			Detail: "PubkeyAuthentication is off, so the only way in is a password — " +
				"and the recommended way to secure this host is unavailable while " +
				"it stays that way.",
			Action: "Turn it on.",
		})
	}

	if keyed == 0 {
		findings = append(findings, Finding{
			ID:       "ssh.no-keys",
			Severity: SeverityWarn,
			Title:    "No account has an SSH key",
			Detail: "Nobody can log in to this host with a key, so passwords are the " +
				"only way in and every hardening step below depends on changing that.",
			Action: "Add a public key for an administrative account.",
		})
	}

	if config.MaxAuthTries > 6 {
		findings = append(findings, Finding{
			ID:       "ssh.max-auth-tries",
			Severity: SeverityWarn,
			Title:    "Each connection may try many passwords",
			Detail: fmt.Sprintf("MaxAuthTries is %d, so one connection gets %d "+
				"attempts before it is closed. The default is 6.", config.MaxAuthTries,
				config.MaxAuthTries),
			Action: "Lower it to 6 or fewer.",
		})
	}

	if config.X11Forwarding {
		findings = append(findings, Finding{
			ID:       "ssh.x11-forwarding",
			Severity: SeverityInfo,
			Title:    "X11 forwarding is enabled",
			Detail: "A server that forwards X11 lets a session reach the client's " +
				"display. A hosting server has no use for it.",
			Action: "Turn it off unless something here needs it.",
		})
	}

	if samePort(config.Ports, 22) {
		// Deliberately "info", and deliberately honest about what it buys. Every
		// panel offers this and most of them oversell it.
		findings = append(findings, Finding{
			ID:       "ssh.default-port",
			Severity: SeverityInfo,
			Title:    "SSH is on the default port",
			Detail: "Port 22 is where untargeted scanners look first. Moving it " +
				"removes most of the noise in the authentication log; it stops " +
				"nobody who is looking for this host in particular, and it is not " +
				"a substitute for keys.",
			Action: "Optional. Allow the new port in the firewall first, then change " +
				"it here.",
		})
	}

	if !config.Running {
		findings = append(findings, Finding{
			ID:       "ssh.not-running",
			Severity: SeverityInfo,
			Title:    "The SSH server is not running",
			Detail: "The configuration below is what it would use if it were " +
				"started. Nothing is listening for connections now.",
			Action: "Start it from the Services page if this host should accept SSH.",
		})
	}

	return findings
}
