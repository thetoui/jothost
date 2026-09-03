package security

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/shared/validate"
)

// The seven scanners.
//
// Five of them ask the phase that manages the thing rather than probing it
// again. That is the same rule Phase 19 applied to thresholds: one definition,
// several views. A security page that said the firewall was off while the
// firewall page said it was on would be worse than no security page, and two
// probes of one thing is exactly how that happens.
//
// Every scanner returns a Result rather than an error, because "this could not
// be checked" is an answer the score has to carry rather than a failure to
// swallow. A scanner that returns Ran=false leaves its existing findings alone
// and contributes nothing to the score in either direction.

// Result is what one scanner produced.
type Result struct {
	Scanner string
	// Ran is false when the scanner could not answer. Its findings are then
	// left exactly as they were: an unreadable firewall must never resolve
	// "the firewall is disabled".
	Ran      bool
	Reason   string
	Findings []Observation
	Duration time.Duration
}

// unavailable builds the result of a scanner that could not answer.
func unavailable(scanner, reason string) Result {
	return Result{Scanner: scanner, Ran: false, Reason: reason, Findings: nil}
}

// ran builds the result of a scanner that answered.
func ran(scanner string, findings ...Observation) Result {
	if findings == nil {
		findings = []Observation{}
	}
	return Result{Scanner: scanner, Ran: true, Findings: findings}
}

// ------------------------------------------------------------------- SSH

// scanSSH reports on the SSH server's configuration.
//
// Phase 17 already derives findings from the effective configuration — what
// `sshd -T` says, not what the file says — and this maps them rather than
// deriving its own. A second interpretation of the same settings would be a
// second answer, and the SSH page and this page would eventually disagree about
// whether root login is permitted.
func (s *Service) scanSSH(ctx context.Context, requestID string) Result {
	status, err := s.agent.SSHStatusOf(ctx, requestID)
	if err != nil {
		return unavailable(validate.ScannerSSH,
			"the SSH server could not be read: "+agentclient.Message(err))
	}
	if !status.Config.Available {
		reason := status.Config.Reason
		if reason == "" {
			reason = "this host has no SSH server"
		}
		// Not a finding. A host with no sshd is not insecure for lacking one,
		// and scoring it as a failure would push every container down.
		return Result{Scanner: validate.ScannerSSH, Ran: true, Reason: reason,
			Findings: []Observation{}}
	}

	findings := []Observation{}
	for _, finding := range status.Findings {
		severity := mapSSHSeverity(finding.Severity)
		findings = append(findings, Observation{
			Scanner:     validate.ScannerSSH,
			Severity:    severity,
			Category:    "ssh",
			Title:       finding.Title,
			Description: finding.Detail,
			Remediation: finding.Action,
			Fingerprint: "ssh:" + sanitiseFingerprint(finding.ID),
			Metadata: map[string]any{
				"ports":      status.Config.Ports,
				"root_login": status.Config.RootLogin,
			},
		})
	}

	// One thing Phase 17 does not report, because it is not a setting: a server
	// that is configured correctly and is not running.
	if !status.Config.Running {
		findings = append(findings, Observation{
			Scanner:     validate.ScannerSSH,
			Severity:    validate.FindingInfo,
			Category:    "ssh",
			Title:       "The SSH server is not running",
			Description: "The configuration is in place and the daemon is stopped.",
			Remediation: "Start it from the Services page if remote access is wanted.",
			Fingerprint: "ssh:not-running",
		})
	}

	return ran(validate.ScannerSSH, findings...)
}

// mapSSHSeverity translates Phase 17's scale onto this one.
//
// Phase 17 speaks in critical/warning/info because that is what an SSH page
// needs. Anything it does not name maps to medium rather than to info: a
// finding this panel cannot grade must not be graded as harmless.
func mapSSHSeverity(severity string) string {
	switch strings.ToLower(severity) {
	case "critical":
		return validate.FindingCritical
	case "high":
		return validate.FindingHigh
	case "warning", "warn", "medium":
		return validate.FindingMedium
	case "low":
		return validate.FindingLow
	case "info", "notice":
		return validate.FindingInfo
	default:
		return validate.FindingMedium
	}
}

// -------------------------------------------------------------- firewall

// scanFirewall reports on the host's packet filter.
func (s *Service) scanFirewall(ctx context.Context, requestID string) Result {
	status, err := s.agent.FirewallStatus(ctx, requestID)
	if err != nil {
		return unavailable(validate.ScannerFirewall,
			"the firewall could not be read: "+agentclient.Message(err))
	}
	if !status.Available {
		reason := status.Reason
		if reason == "" {
			reason = "no firewall this panel can drive is installed"
		}
		// A host with no firewall *is* a finding, unlike a host with no sshd:
		// the absence is the exposure.
		return Result{
			Scanner: validate.ScannerFirewall, Ran: true, Reason: reason,
			Findings: []Observation{{
				Scanner:  validate.ScannerFirewall,
				Severity: validate.FindingHigh,
				Category: "firewall",
				Title:    "No firewall is installed",
				Description: "Nothing is filtering what reaches this host. Every port a " +
					"service listens on is reachable from wherever the network allows.",
				Remediation: "Install ufw and enable it from the Firewall page.",
				Fingerprint: "firewall:absent",
			}},
		}
	}

	findings := []Observation{}

	if !status.Enabled {
		findings = append(findings, Observation{
			Scanner:  validate.ScannerFirewall,
			Severity: validate.FindingHigh,
			Category: "firewall",
			Title:    "The firewall is installed but not enabled",
			Description: "Rules are configured and none of them are in force. " +
				"Every listening port is reachable.",
			Remediation: "Enable the firewall from the Firewall page.",
			Fingerprint: "firewall:disabled",
		})
	} else if strings.EqualFold(status.DefaultIncoming, "allow") {
		// Only worth saying when the firewall is on. On a disabled firewall the
		// default policy is a detail of something already reported.
		findings = append(findings, Observation{
			Scanner:  validate.ScannerFirewall,
			Severity: validate.FindingHigh,
			Category: "firewall",
			Title:    "The firewall allows incoming traffic by default",
			Description: "A default of allow means the rules are a list of what is " +
				"blocked. Anything nobody thought to block is open, including a " +
				"service installed tomorrow.",
			Remediation: "Set the incoming default to deny and add rules for what " +
				"should be reachable.",
			Fingerprint: "firewall:default-allow",
		})
	}

	// A pending change with a deadline is the most urgent thing this panel can
	// report about a firewall: if nobody confirms it, connectivity may already
	// be broken and about to be rolled back.
	if status.Pending != nil {
		findings = append(findings, Observation{
			Scanner:  validate.ScannerFirewall,
			Severity: validate.FindingMedium,
			Category: "firewall",
			Title:    "A firewall change is waiting to be confirmed",
			Description: "It will be rolled back automatically if nobody confirms it. " +
				"That is the safety net working, and it means the change is not settled.",
			Remediation: "Confirm or roll back the change on the Firewall page.",
			Fingerprint: "firewall:pending-change",
		})
	}

	return ran(validate.ScannerFirewall, findings...)
}

// ----------------------------------------------------------------- ports

// portExpectations names the ports a hosting box is supposed to expose.
//
// A closed list, and short. Everything else listening on a public address gets
// reported — which is the point: the finding worth having is "MySQL is on
// 0.0.0.0", and a list of exceptions long enough to cover every service would
// cover that one too.
var portExpectations = map[int]string{
	20:  "FTP data",
	21:  "FTP",
	22:  "SSH",
	53:  "DNS",
	80:  "HTTP",
	443: "HTTPS",
}

// sensitivePorts are the ones whose exposure is not merely unexpected but
// dangerous, with the reason a person needs to hear.
var sensitivePorts = map[int]string{
	3306:  "MySQL or MariaDB",
	5432:  "PostgreSQL",
	6379:  "Redis, which by default requires no password at all",
	27017: "MongoDB",
	11211: "memcached, which requires no password and can be used to amplify attacks",
	9200:  "Elasticsearch",
	2375:  "the Docker daemon, which is root access to this host",
	2376:  "the Docker daemon",
	5672:  "RabbitMQ",
	25:    "SMTP",
}

// scanPorts reports what this host is listening on where the world can reach it.
func (s *Service) scanPorts(ctx context.Context, requestID string) Result {
	report, err := s.agent.SecurityPorts(ctx, requestID)
	if err != nil {
		return unavailable(validate.ScannerPorts,
			"the listening sockets could not be read: "+agentclient.Message(err))
	}

	findings := []Observation{}
	// One finding per port rather than per socket: a service bound to both
	// 0.0.0.0 and :: is one exposure, and reporting it twice would teach people
	// that this page double-counts.
	seen := map[int]bool{}

	for _, socket := range report.Sockets {
		if !socket.Public || seen[socket.Port] {
			continue
		}
		if _, expected := portExpectations[socket.Port]; expected {
			continue
		}
		seen[socket.Port] = true

		process := socket.Process
		if process == "" {
			process = "an unidentified process"
		}

		if what, sensitive := sensitivePorts[socket.Port]; sensitive {
			findings = append(findings, Observation{
				Scanner:  validate.ScannerPorts,
				Severity: validate.FindingCritical,
				Category: "exposed-service",
				Title: fmt.Sprintf("Port %d is reachable from the network (%s)",
					socket.Port, what),
				Description: fmt.Sprintf(
					"%s is listening on %s, so %s is reachable from wherever the "+
						"network allows rather than only from this machine.",
					process, socket.Address, what),
				Remediation: "Bind the service to 127.0.0.1 if only this host uses it, " +
					"or block the port on the Firewall page if it must stay bound.",
				Fingerprint: fmt.Sprintf("ports:public:%d", socket.Port),
				Metadata: map[string]any{
					"port": socket.Port, "address": socket.Address,
					"process": socket.Process, "protocol": socket.Protocol,
				},
			})
			continue
		}

		findings = append(findings, Observation{
			Scanner:  validate.ScannerPorts,
			Severity: validate.FindingLow,
			Category: "exposed-service",
			Title:    fmt.Sprintf("Port %d is reachable from the network", socket.Port),
			Description: fmt.Sprintf(
				"%s is listening on %s. This panel does not know what it is for.",
				process, socket.Address),
			Remediation: "If it is deliberate, accept this finding and say why. " +
				"If not, stop the service or block the port.",
			Fingerprint: fmt.Sprintf("ports:public:%d", socket.Port),
			Metadata: map[string]any{
				"port": socket.Port, "address": socket.Address,
				"process": socket.Process, "protocol": socket.Protocol,
			},
		})
	}

	result := ran(validate.ScannerPorts, findings...)
	if !report.ProcessesResolved {
		// The sockets are still accurate; only their owners are unknown. That
		// is worth saying, and it is not a reason to discard the answer.
		result.Reason = report.Reason
	}
	return result
}

// ----------------------------------------------------------- permissions

// scanPermissions reports what is writable or exposed under this panel's
// directories.
func (s *Service) scanPermissions(ctx context.Context, requestID string) Result {
	report, err := s.agent.SecurityPermissions(ctx, requestID)
	if err != nil {
		return unavailable(validate.ScannerPermissions,
			"the file permissions could not be read: "+agentclient.Message(err))
	}

	findings := []Observation{}

	// Grouped by kind rather than one finding per file. A site with four
	// hundred world-writable files is one problem with one fix, and four
	// hundred rows would bury every other finding on the page.
	kinds := []struct {
		kind        string
		severity    string
		title       string
		description string
		remediation string
	}{
		{
			kind:     agentIssueWorldWritable,
			severity: validate.FindingHigh,
			title:    "Files that any account on this host can change",
			description: "Anything world-writable under a site can be replaced by any " +
				"account on this machine, including one belonging to a different " +
				"customer's site. It is the usual way one compromised site becomes all " +
				"of them.",
			remediation: "Remove write permission for others: the files should be " +
				"writable by the site's own account and nobody else.",
		},
		{
			kind:     agentIssueSetuid,
			severity: validate.FindingCritical,
			title:    "Executables under a site that run as their owner",
			description: "A setuid or setgid program under a site root has no legitimate " +
				"use. One appearing there is either a mistake or the second stage of an " +
				"intrusion.",
			remediation: "Inspect each one and remove the setuid bit, or the file.",
		},
		{
			kind:     agentIssueExposedSecret,
			severity: validate.FindingCritical,
			title:    "Credentials inside a directory the web server serves",
			description: "A .env or similar file inside a document root is served to " +
				"anyone who asks for it by name. These files hold database passwords by " +
				"convention.",
			remediation: "Move the file above the document root, or block it in the " +
				"site's server configuration.",
		},
		{
			kind:     agentIssueExposedVCS,
			severity: validate.FindingHigh,
			title:    "A version control directory inside a served directory",
			description: "A .git directory holds every version of the source, including " +
				"the credentials somebody committed and then removed. It can be " +
				"downloaded a file at a time by anyone.",
			remediation: "Remove it from the document root, or block it in the site's " +
				"server configuration.",
		},
		{
			kind:        agentIssueReadableKey,
			severity:    validate.FindingHigh,
			title:       "Private keys readable by more than their owner",
			description: "A private key any account can read is not private.",
			remediation: "Set the key's mode to 0600 and check who has been able to read it.",
		},
	}

	for _, kind := range kinds {
		count := report.Counts[kind.kind]
		if count == 0 {
			continue
		}

		examples := []string{}
		for _, issue := range report.Issues {
			if issue.Kind == kind.kind && len(examples) < 10 {
				examples = append(examples, issue.Path)
			}
		}
		sort.Strings(examples)

		description := fmt.Sprintf("%d found. %s", count, kind.description)
		if !report.Complete {
			description += " The scan stopped before finishing, so this is a lower bound."
		}

		findings = append(findings, Observation{
			Scanner:     validate.ScannerPermissions,
			Severity:    kind.severity,
			Category:    "file-permissions",
			Title:       kind.title,
			Description: description,
			Remediation: kind.remediation,
			Fingerprint: "permissions:" + kind.kind,
			Metadata: map[string]any{
				"count": count, "examples": examples, "complete": report.Complete,
			},
		})
	}

	result := ran(validate.ScannerPermissions, findings...)
	if !report.Complete {
		result.Reason = report.Reason
	}
	return result
}

// The issue kinds the Agent reports. Duplicated as constants here rather than
// imported, because the API module does not depend on the Agent module — the
// two talk over a socket, and a shared type would make them one program.
const (
	agentIssueWorldWritable = "world_writable"
	agentIssueSetuid        = "setuid"
	agentIssueExposedVCS    = "exposed_vcs"
	agentIssueExposedSecret = "exposed_secret"
	agentIssueReadableKey   = "readable_key"
)

// ------------------------------------------------------------------- SSL

// Certificate expiry thresholds.
//
// Thirty days is when Let's Encrypt's own renewal window opens, so a
// certificate inside it that has not renewed is a renewal that is failing
// rather than one that has not started.
const (
	certExpiringDays = 30
	certUrgentDays   = 7
)

// scanSSL reports on the certificates the panel knows about.
func (s *Service) scanSSL(ctx context.Context, now time.Time) Result {
	certificates, err := s.certificates.List(ctx)
	if err != nil {
		return unavailable(validate.ScannerSSL,
			"the certificates could not be read: "+err.Error())
	}

	findings := []Observation{}
	for _, certificate := range certificates {
		name := certificate.PrimaryDomain
		if name == "" && len(certificate.Domains) > 0 {
			name = certificate.Domains[0]
		}
		if name == "" {
			name = "a site"
		}

		days := certificate.DaysRemaining(now)
		if days == nil {
			continue
		}

		switch {
		case *days < 0:
			findings = append(findings, Observation{
				Scanner:  validate.ScannerSSL,
				Severity: validate.FindingCritical,
				Category: "tls",
				Title:    fmt.Sprintf("The certificate for %s has expired", name),
				Description: fmt.Sprintf(
					"It expired %d days ago. Every visitor is being shown a warning.",
					-*days),
				Remediation: "Renew it from the SSL page, or check why automatic " +
					"renewal is failing.",
				Fingerprint: "ssl:expired:" + sanitiseFingerprint(name),
				Metadata:    map[string]any{"days": *days, "domain": name},
			})
		case *days <= certUrgentDays:
			findings = append(findings, Observation{
				Scanner:  validate.ScannerSSL,
				Severity: validate.FindingHigh,
				Category: "tls",
				Title: fmt.Sprintf("The certificate for %s expires in %d days",
					name, *days),
				Description: "Renewal has not happened and there is very little time left.",
				Remediation: "Renew it from the SSL page and check the renewal log.",
				Fingerprint: "ssl:expiring:" + sanitiseFingerprint(name),
				Metadata:    map[string]any{"days": *days, "domain": name},
			})
		case *days <= certExpiringDays && !certificate.AutoRenew:
			// Inside the renewal window with automatic renewal off is the case
			// worth reporting. Inside it with renewal on is the system working.
			findings = append(findings, Observation{
				Scanner:  validate.ScannerSSL,
				Severity: validate.FindingMedium,
				Category: "tls",
				Title: fmt.Sprintf("%s renews by hand and expires in %d days",
					name, *days),
				Description: "Automatic renewal is off for this certificate, so somebody " +
					"has to remember.",
				Remediation: "Turn automatic renewal on from the SSL page.",
				Fingerprint: "ssl:manual:" + sanitiseFingerprint(name),
				Metadata:    map[string]any{"days": *days, "domain": name},
			})
		}
	}

	// Sites with no certificate at all, as **one** finding rather than one per
	// site. Read from the websites the panel manages rather than from the
	// certificates, because the interesting case is the row that is *not* there.
	//
	// Grouped for the same reason the permission findings are: a host with fifty
	// sites and no TLS is one decision with one fix, and fifty medium rows would
	// push every critical finding off the top of the page. The one thing a
	// grouped finding must not lose is *which* sites, so they are named.
	sites, err := s.websites.List(ctx)
	if err == nil {
		covered := map[string]bool{}
		for _, certificate := range certificates {
			if certificate.WebsiteID != "" {
				covered[certificate.WebsiteID] = true
			}
		}

		uncovered := []string{}
		for _, site := range sites {
			if !covered[site.ID] {
				uncovered = append(uncovered, site.Domain)
			}
		}
		sort.Strings(uncovered)

		if len(uncovered) > 0 {
			findings = append(findings, Observation{
				Scanner:  validate.ScannerSSL,
				Severity: validate.FindingMedium,
				Category: "tls",
				Title:    fmt.Sprintf("%d site(s) have no certificate", len(uncovered)),
				Description: fmt.Sprintf(
					"%s served over plain HTTP, so everything a visitor sends — "+
						"including anything they type into a form — travels where it can "+
						"be read.",
					describeSites(uncovered)),
				Remediation: "Issue certificates from the SSL page.",
				Fingerprint: "ssl:missing",
				Metadata:    map[string]any{"count": len(uncovered), "domains": uncovered},
			})
		}
	}

	return ran(validate.ScannerSSL, findings...)
}

// --------------------------------------------------------------- updates

// scanUpdates reports what this host has waiting.
//
// Phase 21's central rule is carried through unchanged: a check that could not
// reach the repositories produces an empty package list that is
// indistinguishable from a host with nothing to do, and "up to date" is exactly
// the sentence somebody reads to decide they are safe. So a check that never
// succeeded makes this scanner *unavailable* rather than clean.
func (s *Service) scanUpdates(ctx context.Context) Result {
	check, err := s.updates.LatestCheck(ctx)
	if err != nil {
		return unavailable(validate.ScannerUpdates,
			"this host has not been checked for updates yet, so what it is missing is not known")
	}
	if !check.Succeeded {
		reason := check.Reason
		if reason == "" {
			reason = "the last update check failed"
		}
		return unavailable(validate.ScannerUpdates,
			"what this host is missing is not known: "+reason)
	}

	findings := []Observation{}

	if check.SecurityKnown && check.SecurityCount > 0 {
		findings = append(findings, Observation{
			Scanner:  validate.ScannerUpdates,
			Severity: validate.FindingHigh,
			Category: "updates",
			Title: fmt.Sprintf("%d security update(s) are waiting to be installed",
				check.SecurityCount),
			Description: "These are the fixes the distribution has marked as security " +
				"fixes, which means somebody has published what they fix.",
			Remediation: "Apply them from the System updates page.",
			Fingerprint: "updates:security-outstanding",
			Metadata:    map[string]any{"count": check.SecurityCount},
		})
	}

	if check.PackageCount > 0 {
		severity := validate.FindingLow
		if !check.SecurityKnown {
			// A host whose package manager cannot mark security fixes has no
			// way to tell an urgent update from a cosmetic one, so every
			// outstanding update carries the weight of the worst.
			severity = validate.FindingMedium
		}
		findings = append(findings, Observation{
			Scanner:     validate.ScannerUpdates,
			Severity:    severity,
			Category:    "updates",
			Title:       fmt.Sprintf("%d package update(s) are outstanding", check.PackageCount),
			Description: describeUpdateBacklog(check.SecurityKnown),
			Remediation: "Apply them from the System updates page.",
			Fingerprint: "updates:outstanding",
			Metadata:    map[string]any{"count": check.PackageCount},
		})
	}

	if check.RebootRequired {
		findings = append(findings, Observation{
			Scanner:  validate.ScannerUpdates,
			Severity: validate.FindingMedium,
			Category: "updates",
			Title:    "This host needs a reboot to finish applying updates",
			Description: "Something already installed is not in force yet, which usually " +
				"means a kernel or a library that running processes still hold open.",
			Remediation: "Reboot at a time that suits.",
			Fingerprint: "updates:reboot-required",
		})
	}

	return ran(validate.ScannerUpdates, findings...)
}

func describeUpdateBacklog(securityKnown bool) string {
	if securityKnown {
		return "None of them are marked as security fixes, but a host nobody updates " +
			"is the one that eventually gets broken into."
	}
	return "This host's package manager does not mark security fixes, so there is no " +
		"way to tell which of these matter. All of them have to be treated as if they do."
}

// -------------------------------------------------------------- fail2ban

// scanFail2Ban reports on intrusion prevention.
func (s *Service) scanFail2Ban(ctx context.Context, requestID string) Result {
	status, err := s.agent.Fail2BanStatusOf(ctx, requestID)
	if err != nil {
		return unavailable(validate.ScannerFail2Ban,
			"intrusion prevention could not be read: "+agentclient.Message(err))
	}

	if !status.Available {
		return Result{
			Scanner: validate.ScannerFail2Ban, Ran: true, Reason: status.Reason,
			Findings: []Observation{{
				Scanner:  validate.ScannerFail2Ban,
				Severity: validate.FindingMedium,
				Category: "intrusion-prevention",
				Title:    "No intrusion prevention is installed",
				Description: "Nothing is blocking an address that has failed to log in a " +
					"hundred times. A password that can be guessed will eventually be " +
					"guessed by somebody with time.",
				Remediation: "Install fail2ban from the Intrusion prevention page.",
				Fingerprint: "fail2ban:absent",
			}},
		}
	}

	findings := []Observation{}

	if !status.Running {
		findings = append(findings, Observation{
			Scanner:     validate.ScannerFail2Ban,
			Severity:    validate.FindingMedium,
			Category:    "intrusion-prevention",
			Title:       "Intrusion prevention is installed but not running",
			Description: "The jails are configured and nothing is watching the logs.",
			Remediation: "Start it from the Services page.",
			Fingerprint: "fail2ban:stopped",
		})
	} else {
		enabled := 0
		for _, jail := range status.Jails {
			if jail.Enabled {
				enabled++
			}
		}
		if enabled == 0 {
			findings = append(findings, Observation{
				Scanner:  validate.ScannerFail2Ban,
				Severity: validate.FindingMedium,
				Category: "intrusion-prevention",
				Title:    "Intrusion prevention is running with no jails enabled",
				Description: "The daemon is up and it is not watching anything, which " +
					"looks like protection on every page that reports it as running.",
				Remediation: "Enable at least the SSH jail from the Intrusion prevention page.",
				Fingerprint: "fail2ban:no-jails",
			})
		}
	}

	return ran(validate.ScannerFail2Ban, findings...)
}

// sanitiseFingerprint reduces a value to what validate.Fingerprint accepts.
//
// Fingerprints are built from domains and identifiers this panel already
// validated, so this is belt and braces — but a fingerprint is the key of a
// unique index, and one containing a character the index will not take would
// mean a finding that never matches itself and a table that grows by one
// duplicate per scan forever.
func sanitiseFingerprint(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_', r == '/':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	trimmed := strings.Trim(builder.String(), "-")
	if trimmed == "" {
		return "unnamed"
	}
	if len(trimmed) > 120 {
		trimmed = strings.Trim(trimmed[:120], "-")
	}
	return trimmed
}

// describeSites names the sites in a grouped finding, bounded.
//
// A grouped finding that did not say which sites would be a finding nobody can
// act on, and one that listed four hundred would be a description nothing can
// display. The count is always exact; the names are the first few.
func describeSites(domains []string) string {
	const shown = 5
	if len(domains) == 1 {
		return domains[0] + " is"
	}
	if len(domains) <= shown {
		return strings.Join(domains, ", ") + " are"
	}
	return fmt.Sprintf("%s and %d more are",
		strings.Join(domains[:shown], ", "), len(domains)-shown)
}
