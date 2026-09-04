package notifications

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jothost/panel/shared/validate"
)

// What the rest of the panel raises, and how each thing becomes an event.
//
// # Why these five and not everything
//
// A notification is an interruption. The test of whether something belongs here
// is not "is it interesting" but "would somebody want to be interrupted by it",
// and the answer for a successful backup, a resolved security finding or a
// certificate that renewed itself is no. A channel that reports those is a
// channel somebody mutes, and a muted channel does not deliver the one message
// that mattered.
//
// So: an alert opening, an alert resolving, a backup failing, a certificate
// running out of time, and a security finding serious enough to act on today.
//
// # Why the dedupe keys look like this
//
// Each is the identity of the *thing that happened*, not of the moment it was
// noticed. The monitor re-reads an open alert every minute; the backup
// scheduler sees the same failed backup on every page load. One event per
// thing, enforced by an index, is the difference between a notification and a
// stream.

// Sources, for the record of which phase raised what.
const (
	SourceMonitoring = "monitoring"
	SourceBackup     = "backup"
	SourceSecurity   = "security"
	SourceSSL        = "ssl"
	SourcePanel      = "panel"
)

// AlertOpened raises an alert that has started.
//
// The alert's id is the dedupe key, and Phase 19 already guarantees one open
// alert per thing being watched — so a disk that has been full for a week is one
// notification, not ten thousand. The two phases fit together exactly here, and
// that is not luck: Phase 19's unique partial index was built so that an alert
// could be a thing rather than a stream of readings.
func (s *Service) AlertOpened(ctx context.Context, alertID, severity, message, target string) {
	s.Emit(ctx, Event{
		Source:    SourceMonitoring,
		Kind:      validate.EventAlertOpened,
		Severity:  mapAlertSeverity(severity),
		Title:     message,
		Body:      alertBody(target, true),
		Link:      "/monitoring",
		DedupeKey: "alert.opened:" + safeKey(alertID),
		Metadata:  map[string]any{"alert_id": alertID, "target": target},
	})
}

// AlertResolved raises an alert that has cleared.
//
// Worth sending, and worth sending at the severity of the alert rather than as
// an afterthought: an alert that resolves itself at four in the morning is the
// difference between getting up and going back to sleep. A system that wakes
// people and never tells them it is over teaches them to get up every time.
func (s *Service) AlertResolved(ctx context.Context, alertID, severity, message,
	target string, openFor time.Duration,
) {
	body := alertBody(target, false)
	if openFor > 0 {
		body += fmt.Sprintf(" It was open for %s.", formatDuration(openFor))
	}

	s.Emit(ctx, Event{
		Source:    SourceMonitoring,
		Kind:      validate.EventAlertResolved,
		Severity:  validate.NotifyInfo,
		Title:     "Resolved: " + message,
		Body:      body,
		Link:      "/monitoring",
		DedupeKey: "alert.resolved:" + safeKey(alertID),
		Metadata:  map[string]any{"alert_id": alertID, "target": target},
	})
}

func alertBody(target string, opened bool) string {
	verb := "The condition has cleared on its own."
	if opened {
		verb = "The condition has held long enough to be worth telling you about."
	}
	if target != "" {
		return verb + " It concerns " + target + "."
	}
	return verb
}

// mapAlertSeverity translates Phase 19's two-level scale onto this one.
//
// Phase 19 grades an alert warning or critical, because a person woken at three
// in the morning can act on "bad" or "worse" and nothing finer. Anything it does
// not name maps to warning rather than info: a severity this panel cannot grade
// is one it should still deliver.
func mapAlertSeverity(severity string) string {
	switch strings.ToLower(severity) {
	case "critical":
		return validate.NotifyCritical
	case "warning", "warn":
		return validate.NotifyWarning
	case "info":
		return validate.NotifyInfo
	default:
		return validate.NotifyWarning
	}
}

// BackupFailed raises a backup that did not work.
//
// Only failures. A backup that succeeded is not news, and a channel that
// reported every nightly success is one whose messages nobody opens — including
// the night one of them says the opposite.
//
// It is critical rather than high, and deliberately: a failed backup is not
// dangerous today. It is dangerous on the day somebody needs it, by which time
// nothing can be done, and that asymmetry is what the severity is expressing.
func (s *Service) BackupFailed(ctx context.Context, backupID, subject, reason string) {
	body := "The backup did not complete."
	if reason != "" {
		body += " " + reason
	}
	body += " Nothing was lost by this on its own — what it means is that there " +
		"is one less copy than there should be."

	s.Emit(ctx, Event{
		Source:    SourceBackup,
		Kind:      validate.EventBackupFailed,
		Severity:  validate.NotifyCritical,
		Title:     "Backup failed: " + subject,
		Body:      body,
		Link:      "/backups",
		DedupeKey: "backup.failed:" + safeKey(backupID),
		Metadata:  map[string]any{"backup_id": backupID, "subject": subject},
	})
}

// SSLExpiring raises a certificate running out of time.
//
// The dedupe key carries the *bucket* rather than the day, so one certificate
// produces at most three notifications on its way to expiry — at thirty days,
// at seven, and once it has gone — instead of one a day for a month. A daily
// reminder about the same certificate is a daily reminder people filter.
func (s *Service) SSLExpiring(ctx context.Context, domain string, daysRemaining int) {
	bucket, severity, title := expiryBucket(domain, daysRemaining)
	if bucket == "" {
		return
	}

	body := "Renewal has not happened yet. If this certificate is set to renew " +
		"automatically, something is stopping it; if it is not, it needs doing by hand."
	if daysRemaining < 0 {
		body = "Every visitor to this site is now being shown a certificate warning."
	}

	s.Emit(ctx, Event{
		Source:    SourceSSL,
		Kind:      validate.EventSSLExpiring,
		Severity:  severity,
		Title:     title,
		Body:      body,
		Link:      "/ssl",
		DedupeKey: "ssl." + bucket + ":" + safeKey(domain),
		Metadata:  map[string]any{"domain": domain, "days": daysRemaining},
	})
}

// expiryBucket decides whether a certificate is worth a notification yet.
//
// Three thresholds, and nothing between them. Thirty days is when Let's
// Encrypt's own renewal window opens, so a certificate still unrenewed inside it
// is a renewal that is failing rather than one that has not started.
func expiryBucket(domain string, days int) (bucket, severity, title string) {
	switch {
	case days < 0:
		return "expired", validate.NotifyCritical,
			fmt.Sprintf("The certificate for %s has expired", domain)
	case days <= 7:
		return "urgent", validate.NotifyCritical,
			fmt.Sprintf("The certificate for %s expires in %d days", domain, days)
	case days <= 30:
		return "expiring", validate.NotifyWarning,
			fmt.Sprintf("The certificate for %s expires in %d days", domain, days)
	default:
		return "", "", ""
	}
}

// SecurityFinding raises a security finding worth interrupting somebody for.
//
// Only critical and high. A medium finding is a thing to fix this week and a low
// one is hygiene, and a channel that reported every tidy-up would be one nobody
// reads — including the day it says a database is exposed to the internet.
func (s *Service) SecurityFinding(ctx context.Context, findingID, severity, title,
	description string,
) {
	notify := mapFindingSeverity(severity)
	if notify == "" {
		return
	}

	body := description
	if body == "" {
		body = "A security scan found this on the host."
	}

	s.Emit(ctx, Event{
		Source:    SourceSecurity,
		Kind:      validate.EventSecurityFinding,
		Severity:  notify,
		Title:     title,
		Body:      body,
		Link:      "/security-center",
		DedupeKey: "security.finding:" + safeKey(findingID),
		Metadata:  map[string]any{"finding_id": findingID, "severity": severity},
	})
}

// mapFindingSeverity translates Phase 15's five-level scale, and returns empty
// for the levels that are not worth an interruption.
func mapFindingSeverity(severity string) string {
	switch severity {
	case validate.FindingCritical:
		return validate.NotifyCritical
	case validate.FindingHigh:
		return validate.NotifyHigh
	default:
		return ""
	}
}

// safeKey reduces a value to what validate.DedupeKey accepts.
//
// Fingerprints are built from ids and domains this panel already validated, so
// this is belt and braces — but a dedupe key is the key of a unique index, and
// one containing a character the index will not take is an event that never
// matches itself. That is not a cosmetic failure here: it is one notification
// per minute, forever.
func safeKey(value string) string {
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
	if len(trimmed) > 150 {
		trimmed = strings.Trim(trimmed[:150], "-")
	}
	return trimmed
}

// formatDuration renders a span the way somebody reads one.
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		minutes := int(d.Minutes())
		return fmt.Sprintf("%d minute%s", minutes, plural(minutes))
	case d < 24*time.Hour:
		hours := int(d.Hours())
		return fmt.Sprintf("%d hour%s", hours, plural(hours))
	default:
		days := int(d.Hours() / 24)
		return fmt.Sprintf("%d day%s", days, plural(days))
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// Emitter is the interface the other phases depend on.
//
// One method per thing that can happen, rather than a general Emit, so a phase
// raising a notification cannot invent a severity or a dedupe key. Those are
// decisions about how the panel behaves as a whole, and they belong here.
//
// It is an interface so that every phase can take a nil and carry on. A panel
// with no notifications configured must work exactly as it did before this
// phase existed.
type Emitter interface {
	AlertOpened(ctx context.Context, alertID, severity, message, target string)
	AlertResolved(ctx context.Context, alertID, severity, message, target string,
		openFor time.Duration)
	BackupFailed(ctx context.Context, backupID, subject, reason string)
	SSLExpiring(ctx context.Context, domain string, daysRemaining int)
	SecurityFinding(ctx context.Context, findingID, severity, title, description string)
}
