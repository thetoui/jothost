package socket

import (
	"crypto/subtle"
	"errors"
	"fmt"
)

// Authentication failures. All of them produce the same response to the
// caller; the distinction exists for the audit log.
var (
	ErrPeerDenied    = errors.New("peer is not permitted to call the agent")
	ErrTokenMissing  = errors.New("authentication token is missing")
	ErrTokenMismatch = errors.New("authentication token is incorrect")
)

// Authenticator decides whether a caller may issue operations.
//
// Two independent checks are applied, because they fail in different ways:
//
//   - Peer credentials come from the kernel and cannot be forged, but they
//     only prove which UID connected. If the socket's permissions are ever
//     widened by mistake, they are the check that still holds.
//   - The shared token proves the caller knows a secret. It survives a
//     misconfigured UID mapping — a container rebuild that changes the API's
//     UID, say — and is what a future remote transport would rely on.
//
// Requiring both means a single misconfiguration is not a compromise.
type Authenticator struct {
	allowedUIDs map[int]struct{}
	token       string
}

// AuthOptions configures an Authenticator.
type AuthOptions struct {
	// AllowedUIDs lists the UIDs permitted to call the Agent. An empty list
	// disables the check, which is only appropriate when the socket's
	// permissions are the sole boundary.
	AllowedUIDs []int
	// Token is the shared secret. An empty token disables the check.
	Token string
}

// NewAuthenticator builds an Authenticator.
func NewAuthenticator(opts AuthOptions) *Authenticator {
	uids := make(map[int]struct{}, len(opts.AllowedUIDs))
	for _, uid := range opts.AllowedUIDs {
		uids[uid] = struct{}{}
	}
	return &Authenticator{allowedUIDs: uids, token: opts.Token}
}

// RequiresToken reports whether a token check is configured.
func (a *Authenticator) RequiresToken() bool { return a.token != "" }

// RestrictsPeers reports whether a UID allowlist is configured.
func (a *Authenticator) RestrictsPeers() bool { return len(a.allowedUIDs) > 0 }

// AuthorizePeer checks the connecting process's identity.
//
// root is always allowed: the Agent itself runs as root, and its own health
// check connects to its socket. Refusing root would mean the daemon could not
// probe itself.
func (a *Authenticator) AuthorizePeer(peer PeerCredentials) error {
	if !a.RestrictsPeers() {
		return nil
	}
	if peer.UID == 0 {
		return nil
	}
	if _, ok := a.allowedUIDs[peer.UID]; !ok {
		return fmt.Errorf("%w: uid %d", ErrPeerDenied, peer.UID)
	}
	return nil
}

// AuthorizeToken checks the shared secret.
//
// The comparison is constant time so a caller cannot recover the token one
// byte at a time by measuring how long a rejection takes.
func (a *Authenticator) AuthorizeToken(token string) error {
	if !a.RequiresToken() {
		return nil
	}
	if token == "" {
		return ErrTokenMissing
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(a.token)) != 1 {
		return ErrTokenMismatch
	}
	return nil
}
