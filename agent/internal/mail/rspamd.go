package mail

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jothost/panel/agent/internal/command"
)

// The Rspamd files the panel owns.
//
// All of them under local.d, which is the directory Rspamd merges *into* its
// shipped configuration rather than replacing it. That boundary is the reason
// this package can configure Rspamd at all without owning its whole
// configuration: the distribution keeps its defaults, the panel states the half
// dozen things it cares about, and an Rspamd upgrade brings new defaults with
// it instead of colliding.
const (
	dkimSigningConf = "dkim_signing.conf"
	antivirusConf   = "antivirus.conf"
	actionsConf     = "actions.conf"
	milterConf      = "worker-proxy.inc"
)

// The milter port Postfix talks to Rspamd on.
//
// Rspamd's own default for its proxy worker, bound to the loopback and nothing
// else. It is not configurable from the panel: a milter address that could be
// set from a request would be a way to route every message on this host through
// somewhere else.
const (
	milterHost = "127.0.0.1"
	milterPort = 11332
)

// The ClamAV socket Rspamd's antivirus module connects to.
//
// A Unix socket rather than a TCP port, and fixed: this is a scanner running on
// the same machine, and a network address here would be one more thing that can
// point somewhere unintended.
const clamavSocket = "/run/clamav/clamd.sock"

// buildDKIMSigning renders the signing configuration.
//
// The three settings that decide whether anything is signed at all:
//
//   - sign_authenticated, which signs mail from a customer's mail client. This
//     is the one that matters, because it is nearly all outbound mail.
//   - sign_local, for mail generated on this host — a website's contact form,
//     a cron job's report. Without it, exactly the mail a customer never tests
//     goes out unsigned.
//   - allow_username_mismatch, because the authenticated user is
//     "sales@example.com" and the From: header may legitimately be
//     "info@example.com". Without it Rspamd declines to sign the difference,
//     silently.
func buildDKIMSigning(paths Paths, enabled bool) []byte {
	var out strings.Builder
	out.WriteString("# DKIM signing, owned by JotHost Panel. Rewritten on every change.\n")
	if !enabled {
		out.WriteString("# No domain on this host has a signing key, so signing is off.\n")
		out.WriteString("enabled = false;\n")
		return []byte(out.String())
	}

	out.WriteString("enabled = true;\n")
	out.WriteString("\n")
	out.WriteString("# The key for a domain, resolved from the domain and its selector.\n")
	fmt.Fprintf(&out, "path = \"%s/$domain.$selector.key\";\n", paths.DKIMDir())
	out.WriteString("\n")
	out.WriteString("# Which selector each domain uses. A domain missing from this file\n")
	out.WriteString("# is one whose mail leaves unsigned, and nothing reports it.\n")
	fmt.Fprintf(&out, "selector_map = \"%s\";\n", paths.DKIMMap())
	out.WriteString("\n")
	out.WriteString("sign_authenticated = true;\n")
	out.WriteString("sign_local = true;\n")
	out.WriteString("\n")
	out.WriteString("# The signing domain comes from the From: header rather than from\n")
	out.WriteString("# the envelope, because that is the domain a receiving server checks\n")
	out.WriteString("# DMARC alignment against — signing the envelope domain instead\n")
	out.WriteString("# produces a valid signature that fails alignment, which is a worse\n")
	out.WriteString("# outcome than not signing: it looks like a forgery.\n")
	out.WriteString("use_domain = \"header\";\n")
	out.WriteString("\n")
	out.WriteString("# An authenticated customer sending as one of their own addresses\n")
	out.WriteString("# may have a different From: than their login. Refusing to sign\n")
	out.WriteString("# that difference is a silent failure for a legitimate case.\n")
	out.WriteString("allow_username_mismatch = true;\n")
	out.WriteString("\n")
	out.WriteString("# Do not invent keys. A domain with no key is reported as unsigned\n")
	out.WriteString("# by the panel; a self-generated one would be a signature nobody\n")
	out.WriteString("# can verify, which is worse than none.\n")
	out.WriteString("try_fallback = false;\n")
	return []byte(out.String())
}

// buildActions renders the score thresholds.
//
// Only "reject" is set from the panel. The lower thresholds — where a message
// is marked rather than refused — are Rspamd's own, and they are well chosen;
// a panel that exposed all four would be offering an operator four numbers whose
// relationship they have no way to reason about.
func buildActions(rejectScore int) []byte {
	var out strings.Builder
	out.WriteString("# Spam thresholds, owned by JotHost Panel.\n")
	out.WriteString("#\n")
	out.WriteString("# A message above the reject score is refused at SMTP time, which\n")
	out.WriteString("# means the *sender* is told. That is the deliberate choice: a false\n")
	out.WriteString("# positive that bounces gets a phone call, while one filed in a spam\n")
	out.WriteString("# folder nobody opens is silence, and the customer finds out a week\n")
	out.WriteString("# later that they lost an order.\n")
	fmt.Fprintf(&out, "reject = %d;\n", rejectScore)
	out.WriteString("\n")
	out.WriteString("# The greylisting and marking thresholds are Rspamd's own defaults.\n")
	out.WriteString("# They are well chosen and their relationship to each other matters\n")
	out.WriteString("# more than any one of them.\n")
	return []byte(out.String())
}

// buildAntivirus renders the ClamAV link.
//
// The one setting worth arguing about is what happens when the scanner is not
// answering. Rspamd's default is to log and carry on, which means a stopped
// ClamAV turns virus scanning off without turning the *setting* off — the panel
// would go on reporting that mail is scanned while none of it is.
//
// So the failure is made visible instead: a scanner that cannot be reached adds
// a symbol with a real score, which is enough to be seen in the headers and in
// Rspamd's own history, without being enough on its own to reject legitimate
// mail. The panel additionally reports the daemon as down.
func buildAntivirus(enabled bool) []byte {
	var out strings.Builder
	out.WriteString("# Virus scanning, owned by JotHost Panel.\n")
	if !enabled {
		out.WriteString("# Turned off in the panel.\n")
		out.WriteString("#\n")
		out.WriteString("# The whole module is disabled rather than the clamav rule\n")
		out.WriteString("# inside it. A rule with enabled = false and no servers is one\n")
		out.WriteString("# Rspamd refuses to load — \"cannot add AV rule\" — and that is\n")
		out.WriteString("# a configuration *error*, not a warning: it fails the config\n")
		out.WriteString("# check, so the panel would refuse every mail change on a host\n")
		out.WriteString("# that has Rspamd and virus scanning off, which is the default.\n")
		out.WriteString("enabled = false;\n")
		return []byte(out.String())
	}

	out.WriteString("clamav {\n")
	out.WriteString("  enabled = true;\n")
	out.WriteString("  type = \"clamav\";\n")
	fmt.Fprintf(&out, "  servers = \"%s\";\n", clamavSocket)
	out.WriteString("\n")
	out.WriteString("  # A message ClamAV names is refused outright. There is no useful\n")
	out.WriteString("  # middle ground for a virus: filing it in a spam folder leaves it\n")
	out.WriteString("  # one click away from being opened.\n")
	out.WriteString("  action = \"reject\";\n")
	out.WriteString("\n")
	out.WriteString("  # A scanner that is not answering is visible rather than silent.\n")
	out.WriteString("  # Rspamd's default is to log the failure and pass the message,\n")
	out.WriteString("  # which would leave the panel reporting that mail is scanned when\n")
	out.WriteString("  # none of it is.\n")
	out.WriteString("  scan_mime_parts = true;\n")
	out.WriteString("  symbol_fail = \"CLAM_VIRUS_FAIL\";\n")
	out.WriteString("  max_size = 20971520;\n")
	out.WriteString("}\n")
	return []byte(out.String())
}

// buildMilter renders the proxy worker Postfix connects to.
func buildMilter() []byte {
	var out strings.Builder
	out.WriteString("# The milter Postfix talks to, owned by JotHost Panel.\n")
	out.WriteString("#\n")
	out.WriteString("# Bound to the loopback and nothing else. This socket accepts every\n")
	out.WriteString("# message the host handles and can rewrite it; it has no business\n")
	out.WriteString("# being reachable from the network.\n")
	fmt.Fprintf(&out, "bind_socket = \"%s:%d\";\n", milterHost, milterPort)
	out.WriteString("milter = yes;\n")
	out.WriteString("timeout = 120s;\n")
	out.WriteString("upstream \"local\" {\n")
	out.WriteString("  default = yes;\n")
	out.WriteString("  self_scan = yes;\n")
	out.WriteString("}\n")
	return []byte(out.String())
}

// applyRspamd writes the filtering configuration.
//
// Every file is written even when the feature is off, and that is deliberate:
// a file that is deleted when a feature is switched off leaves Rspamd running
// whatever it had before, so turning spam filtering off in the panel would
// leave it on in the daemon. An explicit "enabled = false" is the only way to
// say it.
func (p *Provider) applyRspamd(ctx context.Context, settings Settings, signing bool) (bool, error) {
	if !p.SupportsFiltering() {
		return false, nil
	}

	files := map[string][]byte{
		dkimSigningConf: buildDKIMSigning(p.paths, signing),
		antivirusConf:   buildAntivirus(settings.VirusEnabled),
		actionsConf:     buildActions(settings.SpamRejectScore),
		milterConf:      buildMilter(),
	}

	changed := false
	for name, content := range files {
		path := p.paths.RspamdLocal(name)
		same, err := fileMatches(path, content)
		if err != nil {
			return changed, err
		}
		if same {
			continue
		}
		// 0644: Rspamd reads its configuration as its own user, and there is
		// nothing secret in these files — the keys they point at are the
		// secret, and those are 0640.
		if err := writeFile(path, content, 0o644, -1, -1); err != nil {
			return changed, err
		}
		changed = true
	}

	// Validated on every reconcile, not only when something changed.
	//
	// That is deliberate and it was learned the hard way: a file the panel wrote
	// that fails the check stays on disk, and the *next* reconcile finds it
	// unchanged, skips the check, and proceeds — so one bad write becomes a
	// configuration that is never looked at again. Checking every time also
	// catches an operator's own edit to a neighbouring file.
	if err := p.validateRspamd(ctx); err != nil {
		return changed, err
	}
	return changed, nil
}

// validateRspamd asks Rspamd whether its configuration parses.
//
// `rspamadm configtest` reads the whole thing, the distribution's files
// included, so a panel file that conflicts with something else on this host is
// caught here rather than at the next restart — by which time the milter is
// down and, because milter_default_action is tempfail, so is mail.
func (p *Provider) validateRspamd(ctx context.Context) error {
	if !p.runner.Available(CommandRspamadm) {
		return nil
	}
	result, err := p.runner.Run(ctx, CommandRspamadm, "configtest")
	if err != nil {
		return wrap("check the Rspamd configuration", err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, firstLine(result.Stderr, result.Stdout))
	}
	return nil
}

// antivirusStatus reports whether the virus scanner is actually answering.
//
// Asked of Rspamd rather than of ClamAV directly, because Rspamd is what calls
// it: a clamd that is running but that Rspamd cannot reach — a socket path that
// moved, a permission — is a host where the panel would report scanning as
// working and no message would be scanned.
func (p *Provider) antivirusStatus(ctx context.Context, enabled bool) Daemon {
	status := Daemon{}
	if !enabled {
		status.Detail = "virus scanning is turned off"
		return status
	}
	if !p.SupportsFiltering() {
		status.Detail = "virus scanning needs Rspamd, which is not installed"
		return status
	}
	status.Installed = true

	if p.services == nil || !p.services.Running(ctx, DaemonClamAV) {
		status.Detail = "the virus scanner is not running, so no message is being scanned"
		return status
	}
	status.Running = true

	if p.runner.Available(CommandRspamc) {
		// `rspamc stat` reports the running instance's own view. It is asked
		// rather than assumed because the interesting failure — Rspamd running
		// with an antivirus module that cannot reach its scanner — looks
		// exactly like success from outside.
		result, err := p.runner.Run(ctx, CommandRspamc, "stat")
		if err == nil && result.Succeeded() {
			status.Version = clamVersion(result.Stdout)
		}
	}
	return status
}

// clamVersion picks a version string out of rspamc's statistics, if it is
// there. An empty answer is not a failure: the version is decoration, and the
// running/not-running answer above it is the one that matters.
func clamVersion(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "Version:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Version:"))
		}
	}
	return ""
}

// spamScore asks Rspamd what it thinks of a message.
//
// Used by the test-message path, which exists for the same reason Phase 20's
// test send does: a spam filter nobody has ever put a message through is a
// filter somebody believes is working. The reply is the score and the action,
// which is what an operator needs to know their threshold is where they think
// it is.
func (p *Provider) spamScore(ctx context.Context, message string) (score float64, action string, err error) {
	if !p.runner.Available(CommandRspamc) {
		return 0, "", fmt.Errorf("%w: this host has no rspamc", ErrUnavailable)
	}
	result, err := p.runner.RunWith(ctx, CommandRspamc, command.Options{Stdin: message}, "symbols")
	if err != nil {
		return 0, "", wrap("ask the spam filter to score a message", err)
	}
	if !result.Succeeded() {
		return 0, "", fmt.Errorf("ask the spam filter to score a message: %s",
			firstLine(result.Stderr, result.Stdout))
	}
	score, action = parseSymbols(result.Stdout)
	return score, action, nil
}

// parseSymbols reads the score and action out of rspamc's report.
func parseSymbols(output string) (float64, string) {
	var score float64
	var action string
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Action:"):
			action = strings.TrimSpace(strings.TrimPrefix(trimmed, "Action:"))
		case strings.HasPrefix(trimmed, "Score:"):
			// "Score: 2.10 / 15.00" — the first number.
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, "Score:"))
			if first, _, found := strings.Cut(value, "/"); found {
				value = strings.TrimSpace(first)
			}
			if parsed, err := strconv.ParseFloat(value, 64); err == nil {
				score = parsed
			}
		}
	}
	return score, action
}
