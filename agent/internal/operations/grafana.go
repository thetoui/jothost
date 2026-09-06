package operations

import (
	"context"
	"errors"

	"github.com/jothost/panel/agent/internal/grafana"
	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/shared/protocol"
)

// grafanaRequest is what the install and provision operations take.
type grafanaRequest struct {
	// The datasource. The panel supplies its own database's details, because
	// the Agent has no reason to know them and no way to find them out.
	Host     string `json:"db_host"`
	Port     int    `json:"db_port"`
	Database string `json:"db_name"`
	User     string `json:"db_user"`
	Password string `json:"db_password"`
	SSLMode  string `json:"db_sslmode"`
	// RootURL is where Grafana is reached, which it needs to build the links
	// inside an embedded panel correctly.
	RootURL string `json:"root_url"`
}

func (r grafanaRequest) config() grafana.DatasourceConfig {
	port := r.Port
	if port == 0 {
		port = 5432
	}
	return grafana.DatasourceConfig{
		Host: r.Host, Port: port, Database: r.Database,
		User: r.User, Password: r.Password, SSLMode: r.SSLMode,
		RootURL: r.RootURL,
	}
}

// handleGrafanaStatus reports what is on the host.
func (r *Registry) handleGrafanaStatus(ctx context.Context, _ protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	if r.deps.Grafana == nil {
		return map[string]any{
			"installed":   false,
			"running":     false,
			"provisioned": false,
			"can_install": false,
			"detail":      "Grafana management is not configured on this host",
		}, nil
	}
	return structToMap(r.deps.Grafana.Status(ctx))
}

// handleGrafanaInstall installs Grafana and provisions it in one job.
//
// One operation rather than two, because an installed and unprovisioned
// Grafana is not a useful state to leave anybody in: it starts, it has no
// datasource, and every dashboard in it is empty for a reason nothing explains.
func (r *Registry) handleGrafanaInstall(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	if r.deps.Grafana == nil {
		return nil, Fail(protocol.CodeUnsupported,
			"Grafana management is not configured on this host", nil)
	}

	var payload grafanaRequest
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	report := reporterFunc(reporter)
	if err := r.deps.Grafana.Install(ctx, report); err != nil {
		return nil, grafanaError(err)
	}
	if err := r.deps.Grafana.Provision(ctx, payload.config(), report); err != nil {
		return nil, grafanaError(err)
	}
	return structToMap(r.deps.Grafana.Status(ctx))
}

// handleGrafanaProvision rewrites the panel's files without installing.
//
// Separate from the install so a datasource password change, or a move to a
// different hostname, does not go through the package manager.
func (r *Registry) handleGrafanaProvision(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	if r.deps.Grafana == nil {
		return nil, Fail(protocol.CodeUnsupported,
			"Grafana management is not configured on this host", nil)
	}

	var payload grafanaRequest
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if err := r.deps.Grafana.Provision(ctx, payload.config(), reporterFunc(reporter)); err != nil {
		return nil, grafanaError(err)
	}
	return structToMap(r.deps.Grafana.Status(ctx))
}

func grafanaError(err error) error {
	switch {
	case errors.Is(err, grafana.ErrUnsupported):
		return Fail(protocol.CodeUnsupported,
			"this host has no package manager the panel can install Grafana with", err)
	case errors.Is(err, grafana.ErrNotInstalled):
		return Fail(protocol.CodeInvalidRequest, "Grafana is not installed on this host", err)
	default:
		return err
	}
}

// grafanaServices resolves a catalogue key to a unit and drives it.
//
// The same adapter shape the mail provider uses: exactly one thing on this host
// owns daemon lifecycles, and it is not the package that writes the
// configuration.
type grafanaServices struct{ registry *Registry }

// GrafanaServicesFor builds the daemon controller for the Grafana manager.
func GrafanaServicesFor(r *Registry) interface {
	Restart(ctx context.Context, name string) error
	Running(ctx context.Context, name string) bool
	Enable(ctx context.Context, name string) error
} {
	return grafanaServices{registry: r}
}

func (g grafanaServices) unit(ctx context.Context, name string) (string, error) {
	provider := g.registry.deps.Services
	if provider == nil || !provider.Available() {
		return "", errors.New("this host has no service manager")
	}
	_, unit, err := provider.UnitFor(ctx, name, nil)
	return unit, err
}

func (g grafanaServices) Restart(ctx context.Context, name string) error {
	unit, err := g.unit(ctx, name)
	if err != nil {
		return err
	}
	return g.registry.deps.Services.Restart(ctx, unit)
}

func (g grafanaServices) Enable(ctx context.Context, name string) error {
	unit, err := g.unit(ctx, name)
	if err != nil {
		return err
	}
	return g.registry.deps.Services.Enable(ctx, unit)
}

func (g grafanaServices) Running(ctx context.Context, name string) bool {
	provider := g.registry.deps.Services
	if provider == nil || !provider.Available() {
		return false
	}
	status, err := provider.Status(ctx, name)
	if err != nil {
		return false
	}
	return status.Running
}
