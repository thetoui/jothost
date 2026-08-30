package validate

import (
	"errors"
	"fmt"
)

// Errors returned by web server validation.
var (
	// ErrInvalidWebserverMode covers an unknown front-end arrangement.
	ErrInvalidWebserverMode = errors.New("invalid web server mode")
	// ErrInvalidBackendPort covers a port Apache must not be given.
	ErrInvalidBackendPort = errors.New("invalid Apache backend port")
)

// Web server arrangements a host can run.
//
// Standalone is nginx serving everything itself: fastest, and the only thing
// this panel did before Phase 4.5. Hybrid puts Apache behind it, which buys
// .htaccess and the modules that only Apache has, and costs a process per
// request that nginx would have answered alone.
const (
	WebserverNginx  = "nginx"
	WebserverHybrid = "hybrid"
)

// WebserverMode checks a host's front-end arrangement.
func WebserverMode(mode string) error {
	switch mode {
	case WebserverNginx, WebserverHybrid:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidWebserverMode, mode)
	}
}

// The range Apache backends are assigned from.
//
// A private range of its own, rather than anywhere above 1024, so that reading
// a port is enough to know what it belongs to — in a netstat, in a firewall
// rule, in a log. It starts at 7080 because that is where Plesk puts its own
// Apache backend, and an operator who has seen one hosting panel should not
// have to learn a second set of numbers.
//
// It is well clear of the ephemeral range the kernel hands to outgoing
// connections (32768 and up), which is what stops a backend binding fine on
// most days and failing on the day something else got there first.
const (
	MinBackendPort = 7080
	MaxBackendPort = 7979
)

// BackendPort checks a port an Apache virtual host will listen on.
func BackendPort(port int) error {
	if port < MinBackendPort || port > MaxBackendPort {
		return fmt.Errorf("%w: %d is outside %d-%d",
			ErrInvalidBackendPort, port, MinBackendPort, MaxBackendPort)
	}
	return nil
}

// BackendPortCount is how many sites a host can run in hybrid mode.
//
// Exposed so the allocator can say "the host is full" rather than looping to
// the end of the range and returning a bare zero.
const BackendPortCount = MaxBackendPort - MinBackendPort + 1
