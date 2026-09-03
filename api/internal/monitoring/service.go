package monitoring

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/shared/validate"
)

// Audit actions.
//
// Changing a rule changes what the panel will and will not warn about, which is
// exactly the kind of change somebody needs to be able to find afterwards —
// "why did nobody get told the disk was full" has an answer here. Acknowledging
// is recorded too: it is a person taking responsibility for something.
const (
	ActionRuleCreate  = "monitor.rule.create"
	ActionRuleUpdate  = "monitor.rule.update"
	ActionRuleDelete  = "monitor.rule.delete"
	ActionAcknowledge = "monitor.alert.acknowledge"

	ResourceTypeRule  = "alert_rule"
	ResourceTypeAlert = "alert"
)

// Errors returned by the service.
var (
	// ErrMetricImmutable means somebody tried to point a rule at something
	// else.
	ErrMetricImmutable = errors.New(
		"a rule's metric and target cannot change: that would be a different rule, " +
			"and the alerts it has already opened would be attributed to a condition " +
			"it never observed")
)

// Actor is who asked, for the audit trail.
type Actor struct {
	UserID    string
	IPAddress string
	UserAgent string
}

// Service reads and changes what the panel watches.
type Service struct {
	repo     *Repository
	audit    *audit.Recorder
	log      *slog.Logger
	serverID string
	now      func() time.Time
}

// ServiceOptions configure a Service.
type ServiceOptions struct {
	Repo     *Repository
	Audit    *audit.Recorder
	Log      *slog.Logger
	ServerID string
	Now      func() time.Time
}

// NewService builds a Service.
func NewService(opts ServiceOptions) *Service {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{
		repo:     opts.Repo,
		audit:    opts.Audit,
		log:      log,
		serverID: opts.ServerID,
		now:      opts.Now,
	}
}

// Overview is everything the monitoring page shows.
type Overview struct {
	// Open are the alerts that have not cleared. They are the page's headline
	// and are ordered newest first.
	Open []Alert `json:"open"`
	// Recent includes resolved ones, so "it has been flapping all week" is
	// visible rather than inferred.
	Recent []Alert `json:"recent"`
	Rules  []Rule  `json:"rules"`
	// Services is what each watched service is doing and since when.
	Services []ServiceStatus `json:"services"`
	// Counts summarise the open alerts by severity.
	Counts Counts `json:"counts"`
}

// Counts summarise what is open.
type Counts struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	// Unacknowledged is what nobody has said they are dealing with, which is
	// the number that matters when deciding whether to worry.
	Unacknowledged int `json:"unacknowledged"`
}

// ServiceStatus is a service and how long it has been in its current state.
type ServiceStatus struct {
	Service string `json:"service"`
	Running bool   `json:"running"`
	Status  string `json:"status"`
	Since   string `json:"since"`
	// ForSeconds is how long it has been this way, which is the number an
	// operator reads: "down" and "down since Tuesday" are different problems.
	ForSeconds int `json:"for_seconds"`
}

// Overview reads the whole picture.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	overview := Overview{
		Open: []Alert{}, Recent: []Alert{}, Rules: []Rule{}, Services: []ServiceStatus{},
	}

	open, err := s.repo.ListAlerts(ctx, s.serverID, StatusOpen, 100)
	if err != nil {
		return overview, err
	}
	overview.Open = open

	for _, alert := range open {
		if alert.Severity == validate.SeverityCritical {
			overview.Counts.Critical++
		} else {
			overview.Counts.Warning++
		}
		if alert.AcknowledgedAt == nil {
			overview.Counts.Unacknowledged++
		}
	}

	recent, err := s.repo.ListAlerts(ctx, s.serverID, "", 50)
	if err != nil {
		return overview, err
	}
	overview.Recent = recent

	rules, err := s.repo.ListRules(ctx, s.serverID)
	if err != nil {
		return overview, err
	}
	overview.Rules = rules

	states, err := s.repo.CurrentServiceStates(ctx, s.serverID)
	if err != nil {
		return overview, err
	}
	now := s.now().UTC()
	for _, state := range states {
		overview.Services = append(overview.Services, ServiceStatus{
			Service:    state.Service,
			Running:    state.Running,
			Status:     state.Status,
			Since:      state.StartedAt.UTC().Format(time.RFC3339),
			ForSeconds: int(state.Duration(now).Seconds()),
		})
	}
	return overview, nil
}

// Alerts returns a host's alerts.
func (s *Service) Alerts(ctx context.Context, status string, limit int) ([]Alert, error) {
	if status != "" && status != StatusOpen && status != StatusResolved {
		return nil, errors.New("an alert is open or resolved")
	}
	return s.repo.ListAlerts(ctx, s.serverID, status, limit)
}

// ServiceHistory returns a service's state changes.
func (s *Service) ServiceHistory(ctx context.Context, service string, limit int) (
	[]ServiceState, error,
) {
	if strings.TrimSpace(service) == "" {
		return nil, errors.New("name the service to read the history of")
	}
	return s.repo.ServiceHistory(ctx, s.serverID, strings.TrimSpace(service), limit)
}

// RuleRequest is a rule to record or change.
type RuleRequest struct {
	Name       string
	Metric     string
	Target     string
	Comparison string
	Threshold  float64
	ForSeconds *int
	Severity   string
	Enabled    *bool
}

// CreateRule records a rule.
func (s *Service) CreateRule(ctx context.Context, actor Actor, requestID string,
	req RuleRequest,
) (Rule, error) {
	rule, err := s.build(req, Rule{ServerID: s.serverID})
	if err != nil {
		return Rule{}, err
	}

	created, err := s.repo.CreateRule(ctx, rule)
	if err != nil {
		return Rule{}, err
	}

	s.record(ctx, actor, requestID, ActionRuleCreate, ResourceTypeRule, created.ID,
		map[string]any{
			"metric": created.Metric, "target": created.Target,
			"threshold": created.Threshold, "severity": created.Severity,
			"for_seconds": created.ForSeconds,
		})
	return created, nil
}

// UpdateRule changes a rule.
func (s *Service) UpdateRule(ctx context.Context, actor Actor, requestID, id string,
	req RuleRequest,
) (Rule, error) {
	existing, err := s.repo.GetRule(ctx, id)
	if err != nil {
		return Rule{}, err
	}

	// The metric and target are fixed for the life of a rule. Changing them
	// would leave the alerts it has already opened describing a condition it
	// never watched.
	if req.Metric != "" && req.Metric != existing.Metric {
		return Rule{}, ErrMetricImmutable
	}
	if req.Target != "" && req.Target != existing.Target {
		return Rule{}, ErrMetricImmutable
	}

	req.Metric = existing.Metric
	req.Target = existing.Target
	rule, err := s.build(req, existing)
	if err != nil {
		return Rule{}, err
	}

	updated, err := s.repo.UpdateRule(ctx, id, rule)
	if err != nil {
		return Rule{}, err
	}

	s.record(ctx, actor, requestID, ActionRuleUpdate, ResourceTypeRule, updated.ID,
		map[string]any{
			"metric": updated.Metric, "target": updated.Target,
			"threshold": updated.Threshold, "severity": updated.Severity,
			"for_seconds": updated.ForSeconds, "enabled": updated.Enabled,
		})
	return updated, nil
}

// build validates a request into a rule, keeping what was not supplied.
func (s *Service) build(req RuleRequest, base Rule) (Rule, error) {
	rule := base
	rule.ServerID = s.serverID

	if req.Name != "" {
		rule.Name = strings.TrimSpace(req.Name)
	}
	if err := validate.AlertRuleName(rule.Name); err != nil {
		return Rule{}, err
	}

	if req.Metric != "" {
		rule.Metric = req.Metric
	}
	if err := validate.AlertMetric(rule.Metric); err != nil {
		return Rule{}, err
	}

	rule.Target = strings.TrimSpace(req.Target)
	if req.Target == "" && base.Target != "" {
		rule.Target = base.Target
	}
	if err := validate.AlertTarget(rule.Metric, rule.Target); err != nil {
		return Rule{}, err
	}

	if req.Comparison != "" {
		rule.Comparison = req.Comparison
	}
	if rule.Comparison == "" {
		rule.Comparison = validate.ComparisonAbove
	}
	if err := validate.AlertComparison(rule.Comparison); err != nil {
		return Rule{}, err
	}

	if req.Threshold != 0 || base.Threshold == 0 {
		rule.Threshold = req.Threshold
	}
	if err := validate.AlertThreshold(rule.Metric, rule.Threshold); err != nil {
		return Rule{}, err
	}

	if req.ForSeconds != nil {
		rule.ForSeconds = *req.ForSeconds
	}
	if err := validate.AlertDuration(rule.ForSeconds); err != nil {
		return Rule{}, err
	}

	if req.Severity != "" {
		rule.Severity = req.Severity
	}
	if rule.Severity == "" {
		rule.Severity = validate.SeverityWarning
	}
	if err := validate.AlertSeverity(rule.Severity); err != nil {
		return Rule{}, err
	}

	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}
	return rule, nil
}

// DeleteRule removes a rule.
func (s *Service) DeleteRule(ctx context.Context, actor Actor, requestID, id string) error {
	rule, err := s.repo.GetRule(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteRule(ctx, id); err != nil {
		return err
	}

	s.record(ctx, actor, requestID, ActionRuleDelete, ResourceTypeRule, id,
		map[string]any{"metric": rule.Metric, "target": rule.Target, "name": rule.Name})
	return nil
}

// Acknowledge records that somebody has seen an alert.
//
// It does not resolve it, and there is no endpoint that does. Whether a
// condition has cleared is a fact about the machine, and a panel where a person
// can mark a full disk as fine is a panel that will one day say a full disk is
// fine.
func (s *Service) Acknowledge(ctx context.Context, actor Actor, requestID, id string) (
	Alert, error,
) {
	alert, err := s.repo.AcknowledgeAlert(ctx, id, actor.UserID, s.now().UTC())
	if err != nil {
		return Alert{}, err
	}

	s.record(ctx, actor, requestID, ActionAcknowledge, ResourceTypeAlert, alert.ID,
		map[string]any{"metric": alert.Metric, "target": alert.Target,
			"severity": alert.Severity, "message": alert.Message})
	return alert, nil
}

// Thresholds returns the numbers the dashboard should judge its live readings
// by.
//
// This is what stops the panel holding two opinions. The dashboard computes its
// own alerts from the snapshot in front of it — which is right, because they
// cannot go stale — but the *numbers* come from here, so an operator who raises
// the disk threshold raises it everywhere rather than in one of two places.
//
// A metric with no rule keeps the value the caller passed in, which is what a
// host looks like before the defaults have been written.
func (s *Service) Thresholds(ctx context.Context, fallback map[string]float64) map[string]float64 {
	thresholds := make(map[string]float64, len(fallback))
	for key, value := range fallback {
		thresholds[key] = value
	}

	rules, err := s.repo.ListRules(ctx, s.serverID)
	if err != nil {
		s.log.Warn("could not read the alert rules for the dashboard", "error", err.Error())
		return thresholds
	}

	for _, rule := range rules {
		if !rule.Enabled || rule.Target != "" {
			// A rule about one filesystem is not the host's general threshold.
			continue
		}
		thresholds[rule.Metric+"."+rule.Severity] = rule.Threshold
	}
	return thresholds
}

// record writes an audit event.
func (s *Service) record(ctx context.Context, actor Actor, requestID, action,
	resourceType, resourceID string, details map[string]any,
) {
	if s.audit == nil {
		return
	}
	if details == nil {
		details = map[string]any{}
	}
	details["request_id"] = requestID

	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       actor.UserID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		IPAddress:    actor.IPAddress,
		UserAgent:    actor.UserAgent,
		Status:       "success",
		Metadata:     details,
	})
}
