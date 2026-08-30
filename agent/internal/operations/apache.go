package operations

import (
	"context"
	"errors"

	"github.com/jothost/panel/agent/internal/apache"
	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/shared/protocol"
)

// handleApacheStatus reports what the hybrid backend can do on this host.
//
// The panel asks before it offers to switch mode, so an operator is told
// "Apache is not installed" by a page rather than by a mode change that fails
// halfway through every site on the host.
func (r *Registry) handleApacheStatus(ctx context.Context, _ protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	status := r.deps.Sites.ApacheStatus(ctx)

	result := map[string]any{
		"available": status.Available,
		"running":   status.Running,
		"sites":     status.Sites,
		// Whether the host could install it, which is a different question
		// from whether it is installed.
		"can_install": r.deps.PHPInstaller != nil && r.deps.PHPInstaller.Available(),
	}
	if status.Version != "" {
		result["version"] = status.Version
	}
	return result, nil
}

// apacheInstallPayload names the packages to install.
//
// The panel sends no package name at all: the Agent holds the only list, and a
// name from a request would be an argument to a package manager running as
// root. The field exists so a payload that carries one is refused rather than
// silently ignored.
type apacheInstallPayload struct{}

// apachePackages are what hybrid mode needs, in the order they are installed.
//
// The proxy package is not optional: PHP runs through mod_proxy_fcgi, and
// without it every .php request returns 500 with a message about an unknown
// handler — on a configuration that is otherwise correct.
var apachePackages = []string{"apache2", "apache2-proxy"}

// handleApacheInstall puts Apache on the host.
//
// It reuses the PHP installer because that is the package manager wrapper the
// Agent already has: it resolves apk, apt or dnf once at startup and never
// takes a package name from a request. Only the name is different here.
func (r *Registry) handleApacheInstall(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	var payload apacheInstallPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	if r.deps.PHPInstaller == nil || !r.deps.PHPInstaller.Available() {
		return nil, Fail(protocol.CodeUnsupported,
			"This host has no package manager the Agent can use", nil)
	}

	report := reporterFunc(reporter)
	for index, pkg := range apachePackages {
		if report != nil {
			report(index*80/len(apachePackages), "Installing "+pkg)
		}
		if err := r.deps.PHPInstaller.InstallPackage(ctx, pkg, nil); err != nil {
			return nil, apacheError(err)
		}
	}

	if report != nil {
		report(90, "Checking the installation")
	}

	// The provider resolved its binary at startup, so a freshly installed
	// Apache is not visible to it until the Agent restarts — except that the
	// path was allowlisted whether or not the binary existed, so the runner
	// finds it now. Re-resolving here is what makes the install usable
	// immediately.
	r.deps.Apache.Rebind()

	status := r.deps.Sites.ApacheStatus(ctx)
	if !status.Available {
		return nil, Fail(protocol.CodeInternal,
			"The packages installed but no Apache binary was found where the Agent expects one",
			nil)
	}

	return map[string]any{
		"available": true,
		"version":   status.Version,
		"packages":  apachePackages,
	}, nil
}

// apacheError maps an Apache failure to a structured response.
func apacheError(err error) error {
	switch {
	case errors.Is(err, apache.ErrUnavailable):
		return Fail(protocol.CodeUnsupported, "Apache is not installed on this host", err)
	case errors.Is(err, apache.ErrNoWebGroup):
		return Fail(protocol.CodeUnsupported,
			"The web server group could not be resolved, so Apache could not read any site", err)
	case errors.Is(err, apache.ErrInvalidConfig):
		return Fail(protocol.CodeInvalidRequest,
			"Apache rejected the generated configuration", err)
	default:
		return err
	}
}
