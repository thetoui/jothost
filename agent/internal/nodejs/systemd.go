package nodejs

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"github.com/jothost/panel/agent/internal/services"
)

// UnitDir is where the panel writes its units.
const UnitDir = "/etc/systemd/system"

// unitTemplate renders a systemd service for a Node.js application.
//
// The hardening directives are the point of using systemd at all. An
// application is somebody else's code running on a shared host, and these are
// what stop it reaching the rest of the machine even if it is compromised:
//
//   - NoNewPrivileges stops it gaining any through a setuid binary.
//   - ProtectSystem=strict makes the whole filesystem read-only, and
//     ReadWritePaths then names the two directories it may write to.
//   - PrivateTmp gives it its own /tmp, so it cannot read what another
//     application left there.
//   - ProtectHome hides every other account's home directory.
//   - RestrictAddressFamilies keeps it to the socket families a web
//     application uses; a raw socket is not one of them.
//
// Every value substituted here has been through App.Validate, so none of them
// can carry a newline and close a directive to start another.
var unitTemplate = template.Must(template.New("unit").Parse(
	`[Unit]
Description=JotHost Node.js application {{ .App.Name }}
Documentation=https://github.com/jothost/panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User={{ .App.User }}
Group={{ .Group }}
WorkingDirectory={{ .App.Root }}
EnvironmentFile={{ .App.EnvFile }}
ExecStart={{ .Binary }} {{ .Startup }}

# An application that exits is restarted, but not in a tight loop: five
# failures inside a minute is a broken deployment, not a transient fault, and
# restarting it forever would hide that while burning the host's CPU.
Restart=always
RestartSec=3
StartLimitIntervalSec=60
StartLimitBurst=5

# Confinement. See the comment above this template for why each one is here.
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictRealtime=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
LockPersonality=true
ReadWritePaths={{ .App.Root }} {{ .LogDir }}

# Output goes to the journal, which is what "logs" reads on a systemd host.
StandardOutput=journal
StandardError=journal
SyslogIdentifier=jothost-node-{{ .App.Name }}

[Install]
WantedBy=multi-user.target
`))

// unitView is what the template renders.
type unitView struct {
	App     App
	Group   string
	Binary  string
	Startup string
	LogDir  string
}

// RenderUnit produces the systemd unit for an application.
func RenderUnit(app App, binary string) (string, error) {
	if err := app.Validate(); err != nil {
		return "", err
	}
	if !filepath.IsAbs(binary) {
		return "", fmt.Errorf("%w: the Node.js binary path must be absolute", ErrUnsupported)
	}

	group := app.Group
	if group == "" {
		group = app.User
	}

	var out bytes.Buffer
	err := unitTemplate.Execute(&out, unitView{
		App:     app,
		Group:   group,
		Binary:  binary,
		Startup: app.StartupPath(),
		LogDir:  app.LogDir(),
	})
	if err != nil {
		return "", fmt.Errorf("render the unit for %s: %w", app.Name, err)
	}
	return out.String(), nil
}

// Systemd runs applications as systemd units.
type Systemd struct {
	services *services.Provider
}

// NewSystemd builds a Systemd runner.
func NewSystemd(provider *services.Provider) *Systemd {
	return &Systemd{services: provider}
}

// Available reports whether this host has systemd.
func (s *Systemd) Available() bool {
	return s.services != nil && s.services.Available()
}

// Install writes an application's unit and asks systemd to read it.
func (s *Systemd) Install(ctx context.Context, app App, binary string) error {
	rendered, err := RenderUnit(app, binary)
	if err != nil {
		return err
	}

	path := filepath.Join(UnitDir, app.UnitName())
	if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
		return fmt.Errorf("write the unit for %s: %w", app.Name, err)
	}

	return s.services.DaemonReload(ctx)
}

// Remove takes an application's unit away.
func (s *Systemd) Remove(ctx context.Context, app App) error {
	path := filepath.Join(UnitDir, app.UnitName())
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove the unit for %s: %w", app.Name, err)
	}
	return s.services.DaemonReload(ctx)
}

// Start, Stop, and Restart drive the unit.
func (s *Systemd) Start(ctx context.Context, app App) error {
	return s.services.Start(ctx, app.UnitName())
}

func (s *Systemd) Stop(ctx context.Context, app App) error {
	return s.services.Stop(ctx, app.UnitName())
}

func (s *Systemd) Restart(ctx context.Context, app App) error {
	return s.services.Restart(ctx, app.UnitName())
}

// Status reports what systemd says about an application.
func (s *Systemd) Status(ctx context.Context, app App) Status {
	status := Status{Name: app.Name, Port: app.Port, Managed: ManagedBySystemd}

	unit, err := s.services.Status(ctx, app.UnitName())
	if err != nil {
		status.State = StateStopped
		status.Detail = "systemd does not know this unit"
		return status
	}

	switch {
	case unit.ActiveState == "active":
		status.State = StateRunning
		status.PID = unit.MainPID
		status.Listening = inUse(app.Port)
		if !status.Listening {
			status.Detail = "the unit is active but nothing is listening on its port yet"
		}
	case unit.ActiveState == "failed":
		status.State = StateFailed
		status.Detail = unit.SubState
	default:
		status.State = StateStopped
	}

	if status.PID > 0 {
		// systemd does not report a start time in the properties this panel
		// asks for, so uptime comes from the process itself — the same source
		// the supervisor uses, which keeps the two answers comparable.
		status.Uptime = processUptime(status.PID)
	}
	return status
}
