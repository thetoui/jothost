package security

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/ssl"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/validate"
)

// Audit actions.
//
// Accepting is audited and running a scan is not. A scan reads and changes
// nothing, and a nightly one would drown the log it is supposed to make
// readable. Accepting a risk is a decision a person made about this machine,
// and "who decided we could live with that, and when" is exactly what an audit
// trail is for.
const (
	ActionAccept = "security.finding.accept"
	ActionReopen = "security.finding.reopen"

	ResourceTypeFinding = "security_finding"
)

// Errors returned by the service.
var (
	// ErrScanRunning means a scan is already in flight. Two at once would race
	// on resolving each other's findings.
	ErrScanRunning = errors.New("a security scan is already running")
	// ErrNotOpen means a finding cannot be accepted because it is not open.
	ErrNotOpen = errors.New("only an open finding can be accepted")
	// ErrNotAccepted means a finding cannot be reopened because it is not
	// accepted.
	ErrNotAccepted = errors.New("only an accepted finding can be reopened")
)

// Actor is who asked, for the audit trail.
type Actor struct {
	UserID    string
	IPAddress string
	UserAgent string
}

// Certificates is what this package needs to know about SSL.
//
// An interface rather than the ssl package's repository directly, so the
// scanner can be tested without one — and so that the dependency is one method
// wide rather than the whole of certificate management.
type Certificates interface {
	List(ctx context.Context) ([]ssl.Certificate, error)
}

// Site is one website, as this package needs it.
type Site struct {
	ID     string
	Domain string
}

// Websites is what this package needs to know about sites.
type Websites interface {
	List(ctx context.Context) ([]Site, error)
}

// UpdateCheck is the last thing the update scanner learned, as this package
// needs it.
//
// Succeeded is the field that carries the weight, for the reason Phase 21 built
// the whole feature around: an empty package list from a check that failed is
// indistinguishable from a host with nothing to do.
type UpdateCheck struct {
	Succeeded      bool
	Reason         string
	SecurityKnown  bool
	SecurityCount  int
	PackageCount   int
	RebootRequired bool
}

// Updates is what this package needs to know about outstanding packages.
type Updates interface {
	LatestCheck(ctx context.Context) (UpdateCheck, error)
}

// Service runs the scanners and keeps the findings.
type Service struct {
	repo         *Repository
	agent        *agentclient.Client
	audit        *audit.Recorder
	certificates Certificates
	websites     Websites
	updates      Updates
	log          *slog.Logger
	serverID     string
	now          func() time.Time

	// scanning guards against two scans at once within this process. Two
	// concurrent scans would each resolve the findings the other had just
	// written, and the survivor would be whichever finished last.
	scanning sync.Mutex
}

// ServiceOptions configure a Service.
type ServiceOptions struct {
	Repository   *Repository
	Agent        *agentclient.Client
	Audit        *audit.Recorder
	Certificates Certificates
	Websites     Websites
	Updates      Updates
	Log          *slog.Logger
	ServerID     string
	Now          func() time.Time
}

// NewService builds a Service.
func NewService(opts ServiceOptions) *Service {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		repo:         opts.Repository,
		agent:        opts.Agent,
		audit:        opts.Audit,
		certificates: opts.Certificates,
		websites:     opts.Websites,
		updates:      opts.Updates,
		log:          log,
		serverID:     opts.ServerID,
		now:          now,
	}
}

// Overview is what the Security Center shows.
type Overview struct {
	Score    Score     `json:"score"`
	Counts   Counts    `json:"counts"`
	Findings []Finding `json:"findings"`
	// Accepted is listed separately and always. An accepted risk that scrolled
	// out of sight would be a risk nobody reviews again.
	Accepted []Finding `json:"accepted"`
	// LastScan is nil when this host has never been scanned — which is a
	// different fact from having nothing wrong with it, and the page says so.
	LastScan   *Scan            `json:"last_scan"`
	Scanners   []ScannerOutcome `json:"scanners"`
	Severities []string         `json:"severities"`
}

// Overview reads everything the page needs.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	overview := Overview{Severities: validate.FindingSeverities}

	counts, err := s.repo.Counts(ctx, s.serverID)
	if err != nil {
		return Overview{}, err
	}
	overview.Counts = counts

	if overview.Findings, err = s.repo.List(ctx, ListParams{
		ServerID: s.serverID, Status: StatusOpen,
	}); err != nil {
		return Overview{}, err
	}
	if overview.Accepted, err = s.repo.List(ctx, ListParams{
		ServerID: s.serverID, Status: StatusAccepted,
	}); err != nil {
		return Overview{}, err
	}

	scan, err := s.repo.LatestScan(ctx, s.serverID)
	switch {
	case errors.Is(err, ErrNeverScanned):
		// Deliberately not a zero score. A host nobody has looked at is not a
		// host with problems and it is not a host without them, and showing a
		// number here would make one of those up.
		overview.Score = Score{
			Grade:   "unknown",
			Summary: "This host has not been scanned yet.",
		}
		overview.Scanners = []ScannerOutcome{}
		return overview, nil
	case err != nil:
		return Overview{}, err
	}

	overview.LastScan = &scan
	overview.Scanners = scan.Scanners
	// Recomputed from the *current* counts rather than read from the stored
	// scan, so accepting a finding moves the score immediately instead of at
	// the next scan. The stored score is the history; this is the state.
	overview.Score = ComputeScore(counts, scan.Scanners)
	return overview, nil
}

// Scan runs every scanner and records what it found.
//
// The order is: run everything, write what was found, resolve what was not, and
// only then compute a score. Resolving before writing would briefly show a host
// with no findings, and a page refreshed at that moment would say the machine
// was clean.
func (s *Service) Scan(ctx context.Context, requestID string, actor Actor) (Scan, error) {
	if !s.scanning.TryLock() {
		return Scan{}, ErrScanRunning
	}
	defer s.scanning.Unlock()

	started := s.now()
	now := started.UTC()

	results := []Result{
		s.timed(func() Result { return s.scanSSH(ctx, requestID) }),
		s.timed(func() Result { return s.scanFirewall(ctx, requestID) }),
		s.timed(func() Result { return s.scanPorts(ctx, requestID) }),
		s.timed(func() Result { return s.scanPermissions(ctx, requestID) }),
		s.timed(func() Result { return s.scanSSL(ctx, now) }),
		s.timed(func() Result { return s.scanUpdates(ctx) }),
		s.timed(func() Result { return s.scanFail2Ban(ctx, requestID) }),
	}

	outcomes := make([]ScannerOutcome, 0, len(results))
	var resolved int64

	for _, result := range results {
		outcome := ScannerOutcome{
			Scanner:  result.Scanner,
			Ran:      result.Ran,
			Reason:   result.Reason,
			Findings: len(result.Findings),
			Duration: int(result.Duration.Milliseconds()),
		}

		if !result.Ran {
			// The findings this scanner produced last time are left exactly as
			// they were. An unreadable firewall must never resolve "the
			// firewall is disabled" — that would report a host as clean at the
			// moment the panel stopped being able to check it.
			outcomes = append(outcomes, outcome)
			s.log.Warn("a security check could not be run",
				"scanner", result.Scanner, "reason", result.Reason)
			continue
		}

		seen := make([]string, 0, len(result.Findings))
		for _, observation := range result.Findings {
			if err := validate.Fingerprint(observation.Fingerprint); err != nil {
				s.log.Error("a scanner produced an unusable fingerprint",
					"scanner", result.Scanner, logger.KeyError, err.Error())
				continue
			}
			if err := validate.FindingSeverity(observation.Severity); err != nil {
				s.log.Error("a scanner produced an unknown severity",
					"scanner", result.Scanner, logger.KeyError, err.Error())
				continue
			}
			if _, err := s.repo.Record(ctx, s.serverID, observation, now); err != nil {
				return Scan{}, err
			}
			seen = append(seen, observation.Fingerprint)
		}

		count, err := s.repo.ResolveMissing(ctx, s.serverID, result.Scanner, seen, now)
		if err != nil {
			return Scan{}, err
		}
		resolved += count
		outcomes = append(outcomes, outcome)
	}

	counts, err := s.repo.Counts(ctx, s.serverID)
	if err != nil {
		return Scan{}, err
	}
	score := ComputeScore(counts, outcomes)

	scan := Scan{
		ServerID:    s.serverID,
		Score:       score.Value,
		ChecksRun:   score.ChecksRun,
		ChecksTotal: score.ChecksTotal,
		Critical:    counts.Critical,
		High:        counts.High,
		Medium:      counts.Medium,
		Low:         counts.Low,
		Info:        counts.Info,
		Accepted:    counts.Accepted,
		Resolved:    int(resolved),
		Scanners:    outcomes,
		DurationMS:  int(s.now().Sub(started).Milliseconds()),
	}
	if actor.UserID != "" {
		scan.TriggeredBy = &actor.UserID
	}

	stored, err := s.repo.RecordScan(ctx, scan)
	if err != nil {
		return Scan{}, err
	}

	s.log.Info("security scan complete",
		"score", stored.Score, "checks_run", stored.ChecksRun,
		"checks_total", stored.ChecksTotal, "critical", stored.Critical,
		"high", stored.High, "resolved", stored.Resolved)
	return stored, nil
}

// timed runs one scanner and records how long it took.
//
// A scanner that has become slow is worth seeing before it becomes a scanner
// that times out, and the per-scanner duration is the only place that shows.
func (s *Service) timed(run func() Result) Result {
	started := s.now()
	result := run()
	result.Duration = s.now().Sub(started)
	return result
}

// Findings returns findings matching a filter.
func (s *Service) Findings(ctx context.Context, params ListParams) ([]Finding, error) {
	params.ServerID = s.serverID
	return s.repo.List(ctx, params)
}

// Accept records that a risk is known and deliberate.
func (s *Service) Accept(ctx context.Context, id, reason string, actor Actor) (Finding, error) {
	if err := validate.AcceptReason(reason); err != nil {
		return Finding{}, err
	}

	existing, err := s.repo.Get(ctx, id)
	if err != nil {
		return Finding{}, err
	}
	if existing.Status != StatusOpen {
		return Finding{}, fmt.Errorf("%w: it is %s", ErrNotOpen, existing.Status)
	}

	finding, err := s.repo.Accept(ctx, id, actor.UserID, reason, s.now().UTC())
	if err != nil {
		return Finding{}, err
	}

	s.record(ctx, actor, ActionAccept, finding.ID, map[string]any{
		"scanner":  finding.Scanner,
		"severity": finding.Severity,
		"title":    finding.Title,
		"reason":   reason,
	})
	return finding, nil
}

// Reopen withdraws an acceptance.
func (s *Service) Reopen(ctx context.Context, id string, actor Actor) (Finding, error) {
	existing, err := s.repo.Get(ctx, id)
	if err != nil {
		return Finding{}, err
	}
	if existing.Status != StatusAccepted {
		return Finding{}, fmt.Errorf("%w: it is %s", ErrNotAccepted, existing.Status)
	}

	finding, err := s.repo.Reopen(ctx, id)
	if err != nil {
		return Finding{}, err
	}

	s.record(ctx, actor, ActionReopen, finding.ID, map[string]any{
		"scanner": finding.Scanner, "title": finding.Title,
	})
	return finding, nil
}

// History returns recent scans.
func (s *Service) History(ctx context.Context, limit int) ([]Scan, error) {
	return s.repo.ScanHistory(ctx, s.serverID, limit)
}

// record writes an audit entry, logging rather than failing when it cannot.
func (s *Service) record(ctx context.Context, actor Actor, action, resourceID string,
	details map[string]any,
) {
	if s.audit == nil {
		return
	}
	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       actor.UserID,
		Action:       action,
		ResourceType: ResourceTypeFinding,
		ResourceID:   resourceID,
		IPAddress:    actor.IPAddress,
		UserAgent:    actor.UserAgent,
		Status:       "success",
		Metadata:     details,
	})
}
