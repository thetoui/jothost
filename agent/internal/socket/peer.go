package socket

import (
	"errors"
	"fmt"
	"net"
)

// PeerCredentials identifies the process on the other end of a Unix socket.
//
// The kernel supplies these values; the caller cannot set or forge them. That
// is what makes them a stronger authentication signal than anything carried in
// the request body, which is why they are checked first.
type PeerCredentials struct {
	UID int
	GID int
	PID int
}

// ErrPeerUnavailable means the peer's identity could not be determined.
//
// This is fatal for a connection rather than something to shrug off: if the
// Agent cannot tell who is calling, it must not act on the request.
var ErrPeerUnavailable = errors.New("peer credentials are unavailable")

// peerCredentials reads the connecting process's identity.
func peerCredentials(conn net.Conn) (PeerCredentials, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return PeerCredentials{}, fmt.Errorf("%w: connection is not a unix socket", ErrPeerUnavailable)
	}

	raw, err := unixConn.SyscallConn()
	if err != nil {
		return PeerCredentials{}, fmt.Errorf("%w: %v", ErrPeerUnavailable, err)
	}

	return readPeerCredentials(raw)
}
