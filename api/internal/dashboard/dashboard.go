// Package dashboard aggregates the live server view (PRD.md section 7).
//
// Every panel is fetched from the Agent independently and in parallel. A
// widget that cannot be filled reports why instead of failing the page: a
// dashboard is what an operator opens when something is already wrong, so it
// has to keep working when parts of the host do not.
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/servers"
	"github.com/jothost/panel/shared/logger"
)

// Widget wraps a panel with its availability.
//
// The three states are distinct and a caller must not conflate them: data
// present, data unavailable for a stated reason, or the host cannot provide
// this metric at all.
type Widget[T any] struct {
	Available bool `json:"available"`
	Data      *T   `json:"data,omitempty"`
	// Unsupported means the host cannot provide this, not that it failed.
	Unsupported bool `json:"unsupported,omitempty"`
	// Error is a short, non-sensitive reason. It never carries internal detail.
	Error string `json:"error,omitempty"`
}

// ok builds an available widget.
func ok[T any](data T) Widget[T] {
	return Widget[T]{Available: true, Data: &data}
}

// unavailable builds a widget that could not be filled.
func unavailable[T any](err error) Widget[T] {
	if agentclient.IsUnsupported(err) {
		return Widget[T]{Unsupported: true, Error: "not available on this host"}
	}
	if errors.Is(err, agentclient.ErrUnavailable) {
		return Widget[T]{Error: "agent unreachable"}
	}
	return Widget[T]{Error: "could not be collected"}
}

// ServiceState is one monitored service.
type ServiceState struct {
	Name string `json:"name"`
	// Kind distinguishes a unit the Agent queried from a dependency the API
	// checks itself, because the two fail for different reasons.
	Kind    string `json:"kind"`
	Running bool   `json:"running"`
	// Status is a short human-readable state: running, stopped, not installed.
	Status  string `json:"status"`
	Enabled *bool  `json:"enabled,omitempty"`
	// Essential means the websites on this host stop working while it is down.
	// It is what decides whether being stopped raises an alert.
	Essential bool `json:"essential"`
}

// Service kinds.
const (
	KindSystemd    = "systemd"
	KindDependency = "dependency"
)

// Alert is a condition worth an operator's attention.
type Alert struct {
	// Severity is "critical" or "warning".
	Severity string `json:"severity"`
	// Category groups alerts by subsystem: disk, memory, cpu, service, agent.
	Category string `json:"category"`
	Message  string `json:"message"`
}

// Alert severities.
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
)

// Thresholds decide when a resource reading becomes an alert.
type Thresholds struct {
	DiskWarning    float64
	DiskCritical   float64
	MemoryWarning  float64
	MemoryCritical float64
	// LoadWarning and LoadCritical are per core, so the same numbers mean the
	// same thing on a 2-core and a 64-core machine.
	LoadWarning  float64
	LoadCritical float64
}

// DefaultThresholds are conservative enough not to cry wolf.
func DefaultThresholds() Thresholds {
	return Thresholds{
		DiskWarning:    80,
		DiskCritical:   90,
		MemoryWarning:  85,
		MemoryCritical: 95,
		LoadWarning:    1.5,
		LoadCritical:   3.0,
	}
}

// Snapshot is the dashboard payload.
type Snapshot struct {
	Server      servers.Server                   `json:"server"`
	System      Widget[agentclient.SystemInfo]   `json:"system"`
	CPU         Widget[agentclient.CPUStats]     `json:"cpu"`
	Memory      Widget[agentclient.MemoryStats]  `json:"memory"`
	Disk        Widget[agentclient.DiskStats]    `json:"disk"`
	Network     Widget[agentclient.NetworkStats] `json:"network"`
	Load        Widget[agentclient.LoadStats]    `json:"load"`
	Services    Widget[[]ServiceState]           `json:"services"`
	Alerts      []Alert                          `json:"alerts"`
	GeneratedAt string                           `json:"generated_at"`
}

// Service builds dashboard snapshots.
type Service struct {
	agent      *agentclient.Client
	servers    *servers.Repository
	log        *slog.Logger
	thresholds Thresholds
	// checkDependency reports whether an API-side dependency is healthy. It is
	// injected so the dashboard does not reach into the server package.
	checkDependency func(ctx context.Context, name string) bool
	dependencies    []string
	now             func() time.Time
	// expiringCerts reports certificates near expiry, injected for the same
	// reason as checkDependency: to keep this package from depending on ssl.
	expiringCerts func(ctx context.Context) []ExpiringCertificate
}

// Options configures a Service.
type Options struct {
	Agent           *agentclient.Client
	Servers         *servers.Repository
	Log             *slog.Logger
	Thresholds      Thresholds
	Dependencies    []string
	CheckDependency func(ctx context.Context, name string) bool
	Now             func() time.Time
	// ExpiringCertificates reports certificates near or past expiry.
	//
	// A function rather than a repository so the dashboard does not depend on
	// the ssl package, which depends on websites, which would make this a
	// cycle. Nil simply produces no certificate alerts.
	ExpiringCertificates func(ctx context.Context) []ExpiringCertificate
}

// ExpiringCertificate is a certificate the dashboard should warn about.
type ExpiringCertificate struct {
	Domain string
	// DaysRemaining is negative once the certificate has expired.
	DaysRemaining int
}

// NewService builds a Service.
func NewService(opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Thresholds == (Thresholds{}) {
		opts.Thresholds = DefaultThresholds()
	}

	return &Service{
		expiringCerts:   opts.ExpiringCertificates,
		agent:           opts.Agent,
		servers:         opts.Servers,
		log:             opts.Log,
		thresholds:      opts.Thresholds,
		dependencies:    opts.Dependencies,
		checkDependency: opts.CheckDependency,
		now:             opts.Now,
	}
}

// Snapshot collects the dashboard view for a server.
//
// The Agent calls run concurrently: serialised, a dashboard with six panels
// would take six round trips, and one slow probe would delay all of them.
func (s *Service) Snapshot(ctx context.Context, serverID, requestID string) (Snapshot, error) {
	server, err := s.servers.Get(ctx, serverID)
	if err != nil {
		return Snapshot{}, err
	}

	snapshot := Snapshot{
		Server:      server,
		GeneratedAt: s.now().UTC().Format(time.RFC3339),
	}

	var wg sync.WaitGroup
	collect := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn()
		}()
	}

	collect(func() {
		info, err := s.agent.System(ctx, requestID)
		snapshot.System = widget(info, err)
	})
	collect(func() {
		stats, err := s.agent.CPU(ctx, requestID)
		snapshot.CPU = widget(stats, err)
	})
	collect(func() {
		stats, err := s.agent.Memory(ctx, requestID)
		snapshot.Memory = widget(stats, err)
	})
	collect(func() {
		stats, err := s.agent.Disk(ctx, requestID, "")
		snapshot.Disk = widget(stats, err)
	})
	collect(func() {
		stats, err := s.agent.Network(ctx, requestID)
		snapshot.Network = widget(stats, err)
	})
	collect(func() {
		stats, err := s.agent.Load(ctx, requestID)
		snapshot.Load = widget(stats, err)
	})
	collect(func() {
		snapshot.Services = s.collectServices(ctx, requestID)
	})

	wg.Wait()

	snapshot.Alerts = s.buildAlerts(ctx, snapshot)
	return snapshot, nil
}

// widget converts an Agent result into a widget.
func widget[T any](data T, err error) Widget[T] {
	if err != nil {
		return unavailable[T](err)
	}
	return ok(data)
}

// collectServices reports the services on the host and the panel's own
// dependencies.
//
// The units come from the Agent's detection rather than from a configured list
// of names. A list in configuration is a guess that has to be maintained per
// host: it names php-fpm on a host that runs php-fpm83, or mysql on one that
// runs mariadb, and the widget then reports "not installed" for something that
// is running. Detection asks the host.
//
// Dependencies and services are gathered into one list because an operator
// wants a single "what is up" panel, but each entry records which kind it is so
// a host without a service manager degrades only the services.
func (s *Service) collectServices(ctx context.Context, requestID string) Widget[[]ServiceState] {
	states := make([]ServiceState, 0, len(s.dependencies)+8)

	// API-side dependencies are checked directly: the API holds the pools, so
	// asking the Agent about Postgres would be a worse answer than its own.
	for _, name := range s.dependencies {
		running := s.checkDependency != nil && s.checkDependency(ctx, name)
		states = append(states, ServiceState{
			Name:    name,
			Kind:    KindDependency,
			Running: running,
			Status:  runningStatus(running),
		})
	}

	result, err := s.agent.ServiceDetect(ctx, requestID)
	if err != nil {
		if agentclient.IsUnsupported(err) {
			// No service manager: the dependencies are still worth showing, so
			// the widget reports what it has rather than nothing.
			return Widget[[]ServiceState]{
				Available:   true,
				Data:        &states,
				Unsupported: true,
				Error:       "no service manager on this host",
			}
		}
		s.log.Warn("failed to collect service status", logger.KeyError, err.Error())
		return unavailable[[]ServiceState](err)
	}

	for _, service := range result.Services {
		states = append(states, ServiceState{
			Name:      service.Label,
			Kind:      KindSystemd,
			Running:   service.Running,
			Status:    detectedStatus(service),
			Enabled:   service.Enabled,
			Essential: service.Essential,
		})
	}
	return ok(states)
}

// detectedStatus renders a service's state for display.
//
// "stopped" and "not installed" are different answers, and so is a unit that is
// still starting: a dashboard that rounds activating to running says a service
// is ready when it is not.
func detectedStatus(service agentclient.DetectedService) string {
	switch {
	case !service.Installed:
		return "not installed"
	case service.Running:
		return "running"
	case service.ActiveState == "failed":
		return "failed"
	case service.ActiveState == "activating":
		return "starting"
	default:
		return "stopped"
	}
}

func runningStatus(running bool) string {
	if running {
		return "running"
	}
	return "unreachable"
}

// buildAlerts derives the alert list from a snapshot.
//
// Alerts are computed from the reading that produced them rather than stored,
// so they cannot go stale or disagree with the panel beside them.
func (s *Service) buildAlerts(ctx context.Context, snapshot Snapshot) []Alert {
	alerts := []Alert{}

	if snapshot.Disk.Available && snapshot.Disk.Data != nil {
		// Per filesystem, not on the aggregate: a full /var matters even when
		// a large idle /home keeps the overall figure low.
		for _, fs := range snapshot.Disk.Data.Filesystems {
			if severity, breached := classify(fs.UsedPercent, s.thresholds.DiskWarning, s.thresholds.DiskCritical); breached {
				alerts = append(alerts, Alert{
					Severity: severity,
					Category: "disk",
					Message:  formatPercent("Disk "+fs.MountPoint+" is", fs.UsedPercent, "full"),
				})
			}
		}
	}

	if snapshot.Memory.Available && snapshot.Memory.Data != nil {
		memory := snapshot.Memory.Data
		if severity, breached := classify(memory.UsedPercent, s.thresholds.MemoryWarning, s.thresholds.MemoryCritical); breached {
			alerts = append(alerts, Alert{
				Severity: severity,
				Category: "memory",
				Message:  formatPercent("Memory is", memory.UsedPercent, "used"),
			})
		}
		// Swap in use on a server usually means memory pressure rather than a
		// healthy overcommit, so it is worth surfacing on its own.
		if memory.SwapTotalBytes > 0 && memory.SwapUsedPercent >= 50 {
			alerts = append(alerts, Alert{
				Severity: SeverityWarning,
				Category: "memory",
				Message:  formatPercent("Swap is", memory.SwapUsedPercent, "used"),
			})
		}
	}

	if snapshot.Load.Available && snapshot.Load.Data != nil {
		load := snapshot.Load.Data
		if severity, breached := classify(load.LoadPerCore, s.thresholds.LoadWarning, s.thresholds.LoadCritical); breached {
			alerts = append(alerts, Alert{
				Severity: severity,
				Category: "cpu",
				Message:  formatFloat("Load average is", load.LoadPerCore, "per core"),
			})
		}
	}

	if snapshot.Services.Available && snapshot.Services.Data != nil {
		for _, service := range *snapshot.Services.Data {
			// "Not installed" is a configuration, not a fault; alerting on it
			// would fire permanently on every host missing an optional unit.
			if service.Status == "not installed" {
				continue
			}
			// Nor does every stopped service mean something is wrong. Cron is
			// legitimately down on a host with no scheduled work, and Apache is
			// deliberately stopped whenever the host serves everything from
			// nginx. An alert that is always on is one nobody reads, so this
			// fires only for the services whose being down breaks websites —
			// and for the API's own dependencies, which are checked above.
			if service.Kind == KindSystemd && !service.Essential {
				continue
			}
			if !service.Running {
				alerts = append(alerts, Alert{
					Severity: SeverityCritical,
					Category: "service",
					Message:  service.Name + " is " + service.Status,
				})
			}
		}
	}

	// A certificate that lapses takes a site down for every visitor at once,
	// and unlike a full disk it gives no warning of its own — so the warning
	// has to come from here.
	if s.expiringCerts != nil {
		for _, certificate := range s.expiringCerts(ctx) {
			alerts = append(alerts, certificateAlert(certificate))
		}
	}

	// An unreachable Agent is the alert that explains every empty panel, so it
	// is stated plainly rather than left for the operator to infer.
	if !snapshot.System.Available && !snapshot.System.Unsupported {
		alerts = append(alerts, Alert{
			Severity: SeverityCritical,
			Category: "agent",
			Message:  "The host agent is unreachable, so live metrics are unavailable",
		})
	}

	return alerts
}

// certificateAlert renders one expiring or expired certificate.
//
// An expired certificate is critical rather than a warning: the site is already
// showing every visitor a security warning, which is indistinguishable from an
// attack and which most people will not click through.
func certificateAlert(certificate ExpiringCertificate) Alert {
	if certificate.DaysRemaining < 0 {
		return Alert{
			Severity: SeverityCritical,
			Category: "ssl",
			Message: fmt.Sprintf("The certificate for %s expired %s ago",
				certificate.Domain, plural(-certificate.DaysRemaining, "day")),
		}
	}

	severity := SeverityWarning
	if certificate.DaysRemaining <= 7 {
		severity = SeverityCritical
	}
	return Alert{
		Severity: severity,
		Category: "ssl",
		Message: fmt.Sprintf("The certificate for %s expires in %s",
			certificate.Domain, plural(certificate.DaysRemaining, "day")),
	}
}

func plural(count int, noun string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

// classify reports the severity a value breaches, if any.
func classify(value, warning, critical float64) (string, bool) {
	switch {
	case value >= critical:
		return SeverityCritical, true
	case value >= warning:
		return SeverityWarning, true
	default:
		return "", false
	}
}
