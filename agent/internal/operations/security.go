package operations

import (
	"context"
	"errors"

	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/security"
	"github.com/jothost/panel/shared/protocol"
)

// The security probes' request boundary.
//
// There is no boundary to speak of, and that is the design: neither operation
// takes a parameter. The directories the permission scan walks and the /proc it
// reads are the Agent's own configuration, fixed at startup.
//
// A scanner that accepted a path from a request would be a way to enumerate any
// directory on the host through an endpoint whose whole purpose is to be run
// often and read by everyone with a security role. So it accepts none.
//
// Neither operation changes anything. That is worth stating because it is what
// makes them safe to run on a schedule, and it is a property somebody extending
// this package has to preserve deliberately.

// securityScanner returns the scanner, or the error a caller should see when
// this host cannot be scanned.
func (r *Registry) securityScanner() (*security.Scanner, error) {
	if r.deps.Security == nil {
		return nil, Fail(protocol.CodeUnsupported,
			security.ErrUnsupported.Error(), nil)
	}
	return r.deps.Security, nil
}

// handleSecurityPorts reports what this host is listening on.
func (r *Registry) handleSecurityPorts(_ context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	scanner, err := r.securityScanner()
	if err != nil {
		return nil, err
	}
	var payload struct{}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	report, err := scanner.ListeningPorts()
	if err != nil {
		return nil, securityError(err)
	}
	return structToMap(report)
}

// handleSecurityPermissions reports what is writable or exposed under the
// directories this panel owns.
func (r *Registry) handleSecurityPermissions(_ context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	scanner, err := r.securityScanner()
	if err != nil {
		return nil, err
	}
	var payload struct{}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	report, err := scanner.Permissions()
	if err != nil {
		return nil, securityError(err)
	}
	return structToMap(report)
}

// securityError maps this package's errors onto protocol codes.
func securityError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, security.ErrUnsupported):
		return Fail(protocol.CodeUnsupported, err.Error(), err)
	default:
		return Fail(protocol.CodeInternal, err.Error(), err)
	}
}
