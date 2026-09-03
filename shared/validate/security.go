package validate

import (
	"errors"
	"fmt"
	"strings"
)

// Errors returned by security validation.
var (
	// ErrInvalidFindingSeverity covers a severity outside the scale.
	ErrInvalidFindingSeverity = errors.New("invalid severity")
	// ErrInvalidScanner covers a scanner this panel does not run.
	ErrInvalidScanner = errors.New("invalid scanner")
	// ErrInvalidAcceptance covers accepting a finding without saying why.
	ErrInvalidAcceptance = errors.New("invalid acceptance")
)

// The severity scale for security findings.
//
// Deliberately not the alert severities above. An alert is a thing happening
// now and has two levels, because a person woken at three in the morning can
// act on "bad" or "worse" and nothing finer. A finding is a standing weakness
// and needs five, because the whole job of the page is to say what to fix
// first.
//
// Five levels, and the useful distinction is between the top two and the rest.
// Critical means somebody can get in now; high means somebody can get in given
// one more thing. Everything below is hygiene. A scale with more levels than
// that invites arguing about whether something is a 6 or a 7 instead of fixing
// it.
const (
	// FindingCritical is exploitable as it stands.
	FindingCritical = "critical"
	// FindingHigh needs one more condition to be exploitable.
	FindingHigh = "high"
	// FindingMedium weakens the host without opening it.
	FindingMedium = "medium"
	// FindingLow is hygiene.
	FindingLow = "low"
	// FindingInfo is worth knowing and costs no score. It exists so the panel
	// can say things it would otherwise have to either inflate or omit.
	FindingInfo = "info"
)

// FindingSeverities is the scale, worst first.
var FindingSeverities = []string{
	FindingCritical, FindingHigh, FindingMedium, FindingLow, FindingInfo,
}

// FindingSeverity checks a severity.
func FindingSeverity(value string) error {
	for _, known := range FindingSeverities {
		if value == known {
			return nil
		}
	}
	return fmt.Errorf("%w: %q is not one of %s",
		ErrInvalidFindingSeverity, value, strings.Join(FindingSeverities, ", "))
}

// FindingRank returns a sortable rank, lower being worse.
//
// An unknown severity ranks last rather than first. A finding whose severity
// this panel does not understand must not sort above a critical one and push it
// off the top of the page.
func FindingRank(value string) int {
	for rank, known := range FindingSeverities {
		if value == known {
			return rank
		}
	}
	return len(FindingSeverities)
}

// FindingAtLeast reports whether value is at least as severe as floor.
func FindingAtLeast(value, floor string) bool {
	return FindingRank(value) <= FindingRank(floor)
}

// The scanners the Security Center runs.
//
// Named constants rather than free strings because a finding records which
// scanner produced it, and a scan resolves the findings of the scanners that
// *ran*. A typo in a scanner name would mean a scanner's old findings were
// never resolved and its new ones never matched — the table would fill with
// pairs of identical findings and nothing would say why.
const (
	ScannerSSH         = "ssh"
	ScannerFirewall    = "firewall"
	ScannerPorts       = "ports"
	ScannerPermissions = "permissions"
	ScannerSSL         = "ssl"
	ScannerUpdates     = "updates"
	ScannerFail2Ban    = "fail2ban"
)

// Scanners is the full set, in the order a report should show them.
var Scanners = []string{
	ScannerSSH, ScannerFirewall, ScannerPorts, ScannerPermissions,
	ScannerSSL, ScannerUpdates, ScannerFail2Ban,
}

// Scanner checks a scanner name.
func Scanner(value string) error {
	for _, known := range Scanners {
		if value == known {
			return nil
		}
	}
	return fmt.Errorf("%w: %q is not one of %s",
		ErrInvalidScanner, value, strings.Join(Scanners, ", "))
}

// MaxAcceptReasonLength bounds the reason given for accepting a finding.
const MaxAcceptReasonLength = 1000

// MinAcceptReasonLength is deliberately more than zero.
//
// Accepting a finding with no reason is a mute button, and a mute button on a
// security page is how a real problem becomes permanent. Somebody has to write
// down why — for the person who reads it in a year, who will otherwise have to
// decide whether the risk was ever assessed at all.
const MinAcceptReasonLength = 8

// AcceptReason checks the justification for accepting a finding.
func AcceptReason(reason string) error {
	trimmed := strings.TrimSpace(reason)
	if len(trimmed) < MinAcceptReasonLength {
		return fmt.Errorf(
			"%w: say why this risk is accepted, in at least %d characters — "+
				"somebody will read it in a year and need to know it was a decision",
			ErrInvalidAcceptance, MinAcceptReasonLength)
	}
	if len(trimmed) > MaxAcceptReasonLength {
		return fmt.Errorf("%w: a reason may be at most %d characters",
			ErrInvalidAcceptance, MaxAcceptReasonLength)
	}
	return nil
}

// MaxFingerprintLength bounds a finding's stable identity.
const MaxFingerprintLength = 200

// Fingerprint checks the stable identity of the thing a finding reports.
//
// It is built by this panel, never supplied by a caller, and it is checked
// because it is the key of a unique index: a fingerprint containing whitespace
// or of unbounded length would produce a row that never matches itself on the
// next scan, and the table would grow one duplicate per scan forever.
func Fingerprint(value string) error {
	if value == "" {
		return fmt.Errorf("%w: a finding needs a fingerprint", ErrInvalidScanner)
	}
	if len(value) > MaxFingerprintLength {
		return fmt.Errorf("%w: a fingerprint may be at most %d characters",
			ErrInvalidScanner, MaxFingerprintLength)
	}
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' ||
			r == ':' || r == '/'
		if !ok {
			return fmt.Errorf(
				"%w: a fingerprint may contain only letters, digits and . - _ : /",
				ErrInvalidScanner)
		}
	}
	return nil
}
