//go:build !linux

package socket

import "syscall"

// readPeerCredentials is unavailable off Linux.
//
// The Agent only runs on Linux; this stub keeps the module building on a
// developer's machine (ARCHITECTURE.md section 16). It fails closed: without
// peer credentials the connection is refused rather than trusted.
func readPeerCredentials(syscall.RawConn) (PeerCredentials, error) {
	return PeerCredentials{}, ErrPeerUnavailable
}
