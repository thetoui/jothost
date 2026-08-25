//go:build linux

package socket

import (
	"fmt"
	"syscall"
)

// readPeerCredentials asks the kernel who is on the other end of the socket.
//
// SO_PEERCRED is resolved at connect time by the kernel from the connecting
// process's actual credentials, so a caller cannot claim to be someone else.
func readPeerCredentials(raw syscall.RawConn) (PeerCredentials, error) {
	var (
		ucred   *syscall.Ucred
		sockErr error
	)

	controlErr := raw.Control(func(fd uintptr) {
		ucred, sockErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if controlErr != nil {
		return PeerCredentials{}, fmt.Errorf("%w: %v", ErrPeerUnavailable, controlErr)
	}
	if sockErr != nil {
		return PeerCredentials{}, fmt.Errorf("%w: %v", ErrPeerUnavailable, sockErr)
	}
	if ucred == nil {
		return PeerCredentials{}, ErrPeerUnavailable
	}

	return PeerCredentials{
		UID: int(ucred.Uid),
		GID: int(ucred.Gid),
		PID: int(ucred.Pid),
	}, nil
}
