package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/api/internal/secrets"
	"github.com/jothost/panel/api/internal/sessions"
	"github.com/jothost/panel/api/internal/users"
)

// Impersonation lives in the auth package because it mints a session, and
// minting sessions is this package's job. Who *may* impersonate whom is the
// tenancy package's business and is decided there before this is called.
//
// An impersonated session is an ordinary session with two differences, and
// both of them are refusals.

// forbiddenWhileImpersonating are the permissions an impersonated session
// never carries, whatever the subject actually holds.
//
// Two families, for two different reasons.
//
// The tenancy permissions are removed so an impersonation cannot be used to
// escalate: an admin impersonating a reseller who then impersonates a customer
// produces an audit trail in which the first hop is invisible from the second,
// and — worse — a chain that could outlive the operator's own session. One hop,
// recorded, ending where it started.
//
// user.manage is removed because it is the ability to change a password. An
// operator who needs to see a customer's panel does not need to be able to
// take the account over permanently, and the difference between those two
// things is the whole reason impersonation is a feature rather than a
// password reset.
var forbiddenWhileImpersonating = map[string]struct{}{
	rbac.PermTenantView:        {},
	rbac.PermTenantManage:      {},
	rbac.PermTenantImpersonate: {},
	rbac.PermUserManage:        {},
}

// ImpersonationResult is a session opened as somebody else.
type ImpersonationResult struct {
	Tokens    *TokenPair
	SessionID string
}

// ErrCannotImpersonate covers a subject whose account cannot be used.
var ErrCannotImpersonate = errors.New("that account cannot be signed in to")

// Impersonate issues a session for subject, marked as opened by actorID.
//
// The caller has already decided that this is allowed. What this does is make
// the session say so: every access token minted for it carries the actor's id,
// so an audit row written by the session can name who was actually at the
// keyboard, and the panel can show a banner rather than letting somebody
// forget whose account they are in.
func (s *Service) Impersonate(ctx context.Context, actorID string, subject users.User,
	rc RequestContext,
) (ImpersonationResult, error) {
	if !subject.IsActive() {
		return ImpersonationResult{}, ErrAccountInactive
	}
	if actorID == subject.ID {
		return ImpersonationResult{}, ErrCannotImpersonate
	}

	refreshToken, err := secrets.NewToken()
	if err != nil {
		return ImpersonationResult{}, err
	}

	// A shorter life than an ordinary session, and deliberately so: an
	// impersonation is a thing somebody does for a few minutes to look at a
	// problem, and a refresh token good for a fortnight would turn it into a
	// standing key to a customer's account. Whichever is shorter wins, so a
	// panel configured with very short sessions is not lengthened by this.
	ttl := s.cfg.RefreshTokenTTL
	if impersonationTTL < ttl {
		ttl = impersonationTTL
	}

	session, err := s.sessions.Create(ctx, sessions.CreateParams{
		UserID:    subject.ID,
		TokenHash: secrets.HashToken(refreshToken),
		IPAddress: rc.IPAddress,
		UserAgent: rc.UserAgent,
		ExpiresAt: s.now().Add(ttl),
	})
	if err != nil {
		return ImpersonationResult{}, err
	}

	accessToken, err := s.issueImpersonatedAccessToken(ctx, subject, session.ID, actorID)
	if err != nil {
		return ImpersonationResult{}, err
	}

	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       actorID,
		Action:       "tenant.impersonate.session",
		ResourceType: "session",
		ResourceID:   session.ID,
		IPAddress:    rc.IPAddress,
		UserAgent:    rc.UserAgent,
		Status:       audit.StatusSuccess,
		Metadata:     map[string]any{"subject_user_id": subject.ID, "subject": subject.Username},
	})

	pair := s.tokenPair(accessToken, refreshToken)
	// The pair's advertised lifetime must not outlive the session it belongs
	// to, or a client would keep using a token the server has already dropped.
	if int(ttl.Seconds()) < pair.ExpiresIn {
		pair.ExpiresIn = int(ttl.Seconds())
	}
	return ImpersonationResult{Tokens: pair, SessionID: session.ID}, nil
}

// impersonationTTL caps how long an impersonated session lasts.
const impersonationTTL = 60 * time.Minute

// issueImpersonatedAccessToken mints a token that says whose session it is and
// who opened it.
func (s *Service) issueImpersonatedAccessToken(ctx context.Context, subject users.User,
	sessionID, actorID string,
) (string, error) {
	permissions, err := s.rbac.PermissionsForUser(ctx, subject.ID)
	if err != nil {
		return "", err
	}
	roles, err := s.rbac.RolesForUser(ctx, subject.ID)
	if err != nil {
		return "", err
	}

	return s.tokens.Issue(ctx, AccessClaims{
		UserID:             subject.ID,
		Username:           subject.Username,
		SessionID:          sessionID,
		Permissions:        stripForbidden(permissions),
		Roles:              roles,
		IssuedAt:           s.now(),
		ImpersonatorUserID: actorID,
	})
}

// stripForbidden removes the permissions an impersonated session never holds.
func stripForbidden(permissions []string) []string {
	kept := make([]string, 0, len(permissions))
	for _, permission := range permissions {
		if _, forbidden := forbiddenWhileImpersonating[permission]; forbidden {
			continue
		}
		kept = append(kept, permission)
	}
	return kept
}

// EndImpersonation revokes an impersonated session and its access tokens.
//
// Both halves matter. Revoking the session stops the refresh token; dropping
// the access tokens stops the one already in a browser tab, which would
// otherwise keep working for the rest of its TTL after somebody pressed "stop
// impersonating" and believed it had stopped.
func (s *Service) EndImpersonation(ctx context.Context, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return ErrInvalidToken
	}
	if err := s.sessions.Revoke(ctx, sessionID); err != nil && !errors.Is(err, sessions.ErrNotFound) {
		return fmt.Errorf("revoke the impersonated session: %w", err)
	}
	if err := s.tokens.RevokeSession(ctx, sessionID); err != nil {
		return fmt.Errorf("revoke the impersonated session's tokens: %w", err)
	}
	return nil
}
