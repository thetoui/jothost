// Package auth implements login, session lifecycle, and two-factor
// authentication.
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/ratelimit"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/api/internal/secrets"
	"github.com/jothost/panel/api/internal/sessions"
	"github.com/jothost/panel/api/internal/twofactor"
	"github.com/jothost/panel/api/internal/users"
	"github.com/jothost/panel/shared/logger"
)

// Errors surfaced to handlers. They are deliberately coarse: the caller
// learns that authentication failed, not why.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrAccountInactive    = errors.New("account is not active")
	ErrRateLimited        = errors.New("too many attempts")
	ErrInvalidToken       = errors.New("invalid or expired token")
	ErrInvalidTOTP        = errors.New("invalid verification code")
	// ErrInvalidRecoveryCode is a recovery code that is not one of the
	// user's unused codes. It says nothing about which of those it was.
	ErrInvalidRecoveryCode = errors.New("invalid recovery code")
	ErrTwoFactorEnabled    = errors.New("two-factor authentication is already enabled")
)

// dummyHash is verified against when the username does not exist, so a
// login attempt costs the same Argon2 work whether or not the account is real.
// Without this, response time alone enumerates valid usernames.
var dummyHash string

func init() {
	// Any fixed password works: the hash is only ever compared against, never
	// matched. A failure here would silently remove the timing defence, so it
	// panics at startup instead.
	h, err := secrets.HashPassword("jothost-timing-equalizer-placeholder")
	if err != nil {
		panic("auth: failed to build timing-equalisation hash: " + err.Error())
	}
	dummyHash = h
}

// Config tunes the service.
type Config struct {
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	MFAChallengeTTL time.Duration
	// TOTPIssuer is the label shown in the user's authenticator app.
	TOTPIssuer string
}

// Service holds the authentication use cases.
type Service struct {
	cfg        Config
	users      *users.Repository
	sessions   *sessions.Repository
	rbac       *rbac.Repository
	twoFactor  *twofactor.Repository
	tokens     *TokenStore
	audit      *audit.Recorder
	userLimit  *ratelimit.Limiter
	ipLimit    *ratelimit.Limiter
	log        *slog.Logger
	timeSource func() time.Time
}

// Dependencies bundles the collaborators a Service needs.
type Dependencies struct {
	Users     *users.Repository
	Sessions  *sessions.Repository
	RBAC      *rbac.Repository
	TwoFactor *twofactor.Repository
	Tokens    *TokenStore
	Audit     *audit.Recorder
	// UserLimiter throttles attempts against one account; IPLimiter throttles
	// one source address across accounts. Both are required.
	UserLimiter *ratelimit.Limiter
	IPLimiter   *ratelimit.Limiter
	Log         *slog.Logger
}

// NewService builds a Service.
func NewService(cfg Config, deps Dependencies) *Service {
	return &Service{
		cfg:        cfg,
		users:      deps.Users,
		sessions:   deps.Sessions,
		rbac:       deps.RBAC,
		twoFactor:  deps.TwoFactor,
		tokens:     deps.Tokens,
		audit:      deps.Audit,
		userLimit:  deps.UserLimiter,
		ipLimit:    deps.IPLimiter,
		log:        deps.Log,
		timeSource: time.Now,
	}
}

// now returns the current time through the injectable clock.
func (s *Service) now() time.Time { return s.timeSource() }

// TokenPair is what a successful authentication returns.
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

// LoginResult is either a token pair or a two-factor challenge.
type LoginResult struct {
	Tokens *TokenPair
	// MFAToken is set when the password was correct but TOTP is required.
	MFAToken string
	// MFARequired distinguishes the two outcomes explicitly rather than
	// relying on a nil check at the call site.
	MFARequired bool
}

// RequestContext carries the client attributes recorded on every auth event.
type RequestContext struct {
	IPAddress string
	UserAgent string
}

// Login verifies a password and either issues tokens or starts a TOTP
// challenge.
func (s *Service) Login(ctx context.Context, username, password string, rc RequestContext) (LoginResult, error) {
	normalized := users.NormalizeUsername(username)

	if err := s.checkRateLimits(ctx, normalized, rc); err != nil {
		return LoginResult{}, err
	}

	user, err := s.users.GetByUsername(ctx, normalized)
	if err != nil {
		if errors.Is(err, users.ErrNotFound) {
			// Spend the same work as a real verification before failing.
			if _, verifyErr := secrets.VerifyPassword(password, dummyHash); verifyErr != nil {
				s.log.Error("timing equalisation failed", logger.KeyError, verifyErr.Error())
			}
			s.recordLoginFailure(ctx, "", normalized, rc, "unknown_user")
			return LoginResult{}, ErrInvalidCredentials
		}
		return LoginResult{}, err
	}

	matched, err := secrets.VerifyPassword(password, user.PasswordHash)
	if err != nil {
		return LoginResult{}, fmt.Errorf("verify password: %w", err)
	}
	if !matched {
		s.recordLoginFailure(ctx, user.ID, normalized, rc, "bad_password")
		return LoginResult{}, ErrInvalidCredentials
	}

	// A disabled or locked account is rejected only after the password check,
	// so account status cannot be probed without valid credentials.
	if !user.IsActive() {
		s.recordLoginFailure(ctx, user.ID, normalized, rc, "inactive_account")
		return LoginResult{}, ErrAccountInactive
	}

	// Upgrade a hash that predates the current Argon2 parameters. A failure
	// here must not block a valid login, so it is logged and ignored.
	if secrets.NeedsRehash(user.PasswordHash) {
		s.rehashPassword(ctx, user.ID, password)
	}

	twoFactorEnabled, err := s.twoFactor.IsEnabled(ctx, user.ID)
	if err != nil {
		return LoginResult{}, err
	}

	if twoFactorEnabled {
		mfaToken, err := s.tokens.IssueMFAChallenge(ctx, MFAChallenge{
			UserID:    user.ID,
			Username:  user.Username,
			IPAddress: rc.IPAddress,
		}, s.cfg.MFAChallengeTTL)
		if err != nil {
			return LoginResult{}, err
		}
		return LoginResult{MFARequired: true, MFAToken: mfaToken}, nil
	}

	pair, err := s.completeLogin(ctx, user, rc)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Tokens: pair}, nil
}

// VerifyTwoFactor completes a login that required TOTP.
func (s *Service) VerifyTwoFactor(ctx context.Context, mfaToken, code string, rc RequestContext) (*TokenPair, error) {
	return s.completeChallenge(ctx, mfaToken, rc, func(challenge MFAChallenge) error {
		valid, err := s.twoFactor.VerifyCode(ctx, challenge.UserID, code, s.now())
		if err != nil {
			if errors.Is(err, twofactor.ErrNotEnrolled) {
				return ErrInvalidToken
			}
			return err
		}
		if !valid {
			s.audit.RecordAsync(ctx, audit.Event{
				UserID:    challenge.UserID,
				Action:    audit.ActionTwoFactorFailed,
				IPAddress: rc.IPAddress,
				UserAgent: rc.UserAgent,
				Status:    audit.StatusFailure,
			})
			return ErrInvalidTOTP
		}
		return nil
	})
}

// VerifyRecoveryCode completes a login that required two-factor with one of
// the user's recovery codes instead of an authenticator code.
//
// It is the same step as VerifyTwoFactor, behind the same challenge and the
// same rate limits, so a recovery code is no easier to guess than a TOTP code
// is. The code is spent whether or not anything after it succeeds: a code
// that has been typed into a sign-in form once is not one to keep.
func (s *Service) VerifyRecoveryCode(ctx context.Context, mfaToken, recoveryCode string, rc RequestContext) (*TokenPair, error) {
	return s.completeChallenge(ctx, mfaToken, rc, func(challenge MFAChallenge) error {
		valid, remaining, err := s.twoFactor.UseRecoveryCode(ctx, challenge.UserID, recoveryCode)
		if err != nil {
			return err
		}
		if !valid {
			s.audit.RecordAsync(ctx, audit.Event{
				UserID:    challenge.UserID,
				Action:    audit.ActionTwoFactorFailed,
				IPAddress: rc.IPAddress,
				UserAgent: rc.UserAgent,
				Status:    audit.StatusFailure,
				Metadata:  map[string]any{"method": "recovery_code"},
			})
			return ErrInvalidRecoveryCode
		}
		// Recorded as a success of its own, not folded into the login: a
		// recovery code in use means an authenticator is lost or somebody else
		// has the codes, and either way it is the line an administrator reading
		// the audit trail needs to find.
		s.audit.RecordAsync(ctx, audit.Event{
			UserID:    challenge.UserID,
			Action:    audit.ActionTwoFactorRecoveryUsed,
			IPAddress: rc.IPAddress,
			UserAgent: rc.UserAgent,
			Status:    audit.StatusSuccess,
			Metadata:  map[string]any{"remaining": remaining},
		})
		return nil
	})
}

// completeChallenge runs one second-factor check against a pending login and,
// if it passes, signs the user in.
func (s *Service) completeChallenge(ctx context.Context, mfaToken string, rc RequestContext,
	check func(MFAChallenge) error,
) (*TokenPair, error) {
	challenge, err := s.tokens.PeekMFAChallenge(ctx, mfaToken)
	if err != nil {
		if errors.Is(err, ErrTokenNotFound) {
			return nil, ErrInvalidToken
		}
		return nil, err
	}

	// The TOTP step is rate limited too: a valid challenge token must not
	// become an unlimited oracle for guessing six-digit codes.
	if err := s.checkRateLimits(ctx, challenge.Username, rc); err != nil {
		return nil, err
	}

	if err := check(challenge); err != nil {
		return nil, err
	}

	// Consume the challenge only on success, so a mistyped code does not send
	// the user back to the password step.
	if err := s.tokens.ConsumeMFAChallenge(ctx, mfaToken); err != nil {
		return nil, err
	}

	user, err := s.users.GetByID(ctx, challenge.UserID)
	if err != nil {
		return nil, err
	}
	if !user.IsActive() {
		return nil, ErrAccountInactive
	}

	return s.completeLogin(ctx, user, rc)
}

// completeLogin issues a session and its first token pair.
func (s *Service) completeLogin(ctx context.Context, user users.User, rc RequestContext) (*TokenPair, error) {
	refreshToken, err := secrets.NewToken()
	if err != nil {
		return nil, err
	}

	session, err := s.sessions.Create(ctx, sessions.CreateParams{
		UserID:    user.ID,
		TokenHash: secrets.HashToken(refreshToken),
		IPAddress: rc.IPAddress,
		UserAgent: rc.UserAgent,
		ExpiresAt: s.now().Add(s.cfg.RefreshTokenTTL),
	})
	if err != nil {
		return nil, err
	}

	accessToken, err := s.issueAccessToken(ctx, user, session.ID)
	if err != nil {
		return nil, err
	}

	if err := s.users.TouchLastLogin(ctx, user.ID); err != nil {
		// Recording the login time is bookkeeping, not authentication.
		s.log.Warn("failed to update last_login_at",
			logger.KeyUserID, user.ID, logger.KeyError, err.Error())
	}

	// Clear the throttle so earlier mistyped attempts do not count against a
	// user who has now proven who they are.
	if err := s.userLimit.Reset(ctx, user.Username); err != nil {
		s.log.Warn("failed to reset login rate limit", logger.KeyError, err.Error())
	}

	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       user.ID,
		Action:       audit.ActionLoginSucceeded,
		ResourceType: "session",
		ResourceID:   session.ID,
		IPAddress:    rc.IPAddress,
		UserAgent:    rc.UserAgent,
		Status:       audit.StatusSuccess,
	})

	return s.tokenPair(accessToken, refreshToken), nil
}

// issueAccessToken snapshots the user's roles and permissions into a token.
func (s *Service) issueAccessToken(ctx context.Context, user users.User, sessionID string) (string, error) {
	permissions, err := s.rbac.PermissionsForUser(ctx, user.ID)
	if err != nil {
		return "", err
	}
	roles, err := s.rbac.RolesForUser(ctx, user.ID)
	if err != nil {
		return "", err
	}

	return s.tokens.Issue(ctx, AccessClaims{
		UserID:      user.ID,
		Username:    user.Username,
		SessionID:   sessionID,
		Permissions: permissions,
		Roles:       roles,
		IssuedAt:    s.now(),
	})
}

func (s *Service) tokenPair(accessToken, refreshToken string) *TokenPair {
	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		TokenType:    "Bearer",
		ExpiresIn:    int(s.cfg.AccessTokenTTL.Seconds()),
	}
}

// Refresh exchanges a refresh token for a new pair, rotating the refresh token.
//
// If the presented token is not the session's current one, the token has
// leaked: whoever holds the older copy is replaying it. The session is revoked
// entirely rather than merely rejecting the request.
func (s *Service) Refresh(ctx context.Context, refreshToken string, rc RequestContext) (*TokenPair, error) {
	if refreshToken == "" {
		return nil, ErrInvalidToken
	}

	oldHash := secrets.HashToken(refreshToken)
	newToken, err := secrets.NewToken()
	if err != nil {
		return nil, err
	}

	session, err := s.sessions.Rotate(ctx, oldHash, secrets.HashToken(newToken),
		s.now().Add(s.cfg.RefreshTokenTTL))
	if err != nil {
		if errors.Is(err, sessions.ErrNotFound) {
			s.handleRefreshFailure(ctx, oldHash, rc)
			return nil, ErrInvalidToken
		}
		return nil, err
	}

	user, err := s.users.GetByID(ctx, session.UserID)
	if err != nil {
		return nil, err
	}
	if !user.IsActive() {
		if revokeErr := s.revokeSession(ctx, session.ID); revokeErr != nil {
			s.log.Error("failed to revoke session for inactive user",
				logger.KeyError, revokeErr.Error())
		}
		return nil, ErrAccountInactive
	}

	// The previous access tokens for this session are dropped so a rotated
	// pair does not leave older access tokens usable.
	if err := s.tokens.RevokeSession(ctx, session.ID); err != nil {
		return nil, err
	}

	accessToken, err := s.issueAccessToken(ctx, user, session.ID)
	if err != nil {
		return nil, err
	}

	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       user.ID,
		Action:       audit.ActionTokenRefreshed,
		ResourceType: "session",
		ResourceID:   session.ID,
		IPAddress:    rc.IPAddress,
		UserAgent:    rc.UserAgent,
		Status:       audit.StatusSuccess,
	})

	return s.tokenPair(accessToken, newToken), nil
}

// handleRefreshFailure distinguishes an unknown token from a replayed one.
//
// Rotate matches only usable sessions, so a failure has three possible causes:
// the token was never issued (nothing to do), it belongs to a session that is
// now revoked or expired, or it is a retired token from an earlier rotation.
// The last two both mean a token that should no longer exist is in someone's
// hands, so the user's whole session set is torn down.
func (s *Service) handleRefreshFailure(ctx context.Context, tokenHash string, rc RequestContext) {
	sessionID, userID, found := s.locateLeakedToken(ctx, tokenHash)
	if !found {
		// Genuinely unknown token: nothing to escalate.
		return
	}

	s.log.Warn("refresh token reuse detected",
		logger.KeyUserID, userID,
		"session_id", sessionID,
	)

	revoked, err := s.sessions.RevokeAllForUser(ctx, userID)
	if err != nil {
		s.log.Error("failed to revoke sessions after refresh reuse", logger.KeyError, err.Error())
		return
	}
	if err := s.tokens.RevokeSessions(ctx, append(revoked, sessionID)); err != nil {
		s.log.Error("failed to revoke access tokens after refresh reuse", logger.KeyError, err.Error())
	}

	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       userID,
		Action:       audit.ActionRefreshReuse,
		ResourceType: "session",
		ResourceID:   sessionID,
		IPAddress:    rc.IPAddress,
		UserAgent:    rc.UserAgent,
		Status:       audit.StatusFailure,
		Metadata:     map[string]any{"sessions_revoked": len(revoked)},
	})
}

// locateLeakedToken resolves a rejected refresh token to its session, whether
// it is the session's current hash or one it has already retired.
func (s *Service) locateLeakedToken(ctx context.Context, tokenHash string) (sessionID, userID string, found bool) {
	if session, err := s.sessions.GetByTokenHash(ctx, tokenHash); err == nil {
		return session.ID, session.UserID, true
	}

	retiredSessionID, err := s.sessions.FindRetiredToken(ctx, tokenHash)
	if err != nil {
		return "", "", false
	}

	session, err := s.sessions.GetByID(ctx, retiredSessionID)
	if err != nil {
		// The session row is gone (expired and cleaned up); the retired hash
		// still identifies it, but there is no user left to act on.
		s.log.Warn("retired refresh token references a missing session",
			"session_id", retiredSessionID)
		return "", "", false
	}
	return session.ID, session.UserID, true
}

// Logout revokes the caller's session and its access tokens.
func (s *Service) Logout(ctx context.Context, claims AccessClaims, rc RequestContext) error {
	if err := s.revokeSession(ctx, claims.SessionID); err != nil {
		return err
	}

	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       claims.UserID,
		Action:       audit.ActionLogout,
		ResourceType: "session",
		ResourceID:   claims.SessionID,
		IPAddress:    rc.IPAddress,
		UserAgent:    rc.UserAgent,
		Status:       audit.StatusSuccess,
	})
	return nil
}

func (s *Service) revokeSession(ctx context.Context, sessionID string) error {
	if err := s.sessions.Revoke(ctx, sessionID); err != nil {
		return err
	}
	return s.tokens.RevokeSession(ctx, sessionID)
}

// Profile is the response body of GET /auth/me.
type Profile struct {
	ID          string   `json:"id"`
	Username    string   `json:"username"`
	Email       *string  `json:"email"`
	Status      string   `json:"status"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
	TwoFactor   bool     `json:"two_factor_enabled"`
	// RecoveryCodesRemaining is how many unused recovery codes the user has,
	// so the page can say so before the last one is gone.
	RecoveryCodesRemaining int        `json:"recovery_codes_remaining"`
	LastLoginAt            *time.Time `json:"last_login_at"`
	CreatedAt              time.Time  `json:"created_at"`
}

// Me returns the authenticated user's profile.
//
// Roles and permissions are read fresh from the database rather than taken
// from the token, so the UI reflects a role change before the token rotates.
func (s *Service) Me(ctx context.Context, userID string) (Profile, error) {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return Profile{}, err
	}

	roles, err := s.rbac.RolesForUser(ctx, user.ID)
	if err != nil {
		return Profile{}, err
	}
	permissions, err := s.rbac.PermissionsForUser(ctx, user.ID)
	if err != nil {
		return Profile{}, err
	}
	twoFactorEnabled, err := s.twoFactor.IsEnabled(ctx, user.ID)
	if err != nil {
		return Profile{}, err
	}
	remaining := 0
	if twoFactorEnabled {
		if remaining, err = s.twoFactor.RecoveryCodesRemaining(ctx, user.ID); err != nil {
			return Profile{}, err
		}
	}

	return Profile{
		ID:                     user.ID,
		Username:               user.Username,
		Email:                  user.Email,
		Status:                 user.Status,
		Roles:                  roles,
		Permissions:            permissions,
		TwoFactor:              twoFactorEnabled,
		RecoveryCodesRemaining: remaining,
		LastLoginAt:            user.LastLoginAt,
		CreatedAt:              user.CreatedAt,
	}, nil
}

// TwoFactorSetup is the one-time enrolment payload.
type TwoFactorSetup struct {
	// Secret and URI are secrets. They are returned exactly once, at setup,
	// and must never be logged.
	Secret string `json:"secret"`
	URI    string `json:"otpauth_uri"`
}

// SetupTwoFactor generates a pending TOTP secret for the user.
func (s *Service) SetupTwoFactor(ctx context.Context, userID string, rc RequestContext) (TwoFactorSetup, error) {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return TwoFactorSetup{}, err
	}

	enabled, err := s.twoFactor.IsEnabled(ctx, userID)
	if err != nil {
		return TwoFactorSetup{}, err
	}
	if enabled {
		return TwoFactorSetup{}, ErrTwoFactorEnabled
	}

	secret, err := s.twoFactor.StartEnrolment(ctx, userID)
	if err != nil {
		return TwoFactorSetup{}, err
	}

	s.audit.RecordAsync(ctx, audit.Event{
		UserID:    userID,
		Action:    audit.ActionTwoFactorSetup,
		IPAddress: rc.IPAddress,
		UserAgent: rc.UserAgent,
		Status:    audit.StatusSuccess,
	})

	return TwoFactorSetup{
		Secret: secret,
		URI:    secrets.TOTPProvisioningURI(s.cfg.TOTPIssuer, user.Username, secret),
	}, nil
}

// EnableTwoFactor activates a pending enrolment once the user proves they can
// generate a valid code, and returns the account's first recovery codes. They
// are returned this once and never again.
func (s *Service) EnableTwoFactor(ctx context.Context, userID, code string, rc RequestContext) ([]string, error) {
	valid, err := s.twoFactor.VerifyCode(ctx, userID, code, s.now())
	if err != nil {
		if errors.Is(err, twofactor.ErrNotEnrolled) {
			return nil, twofactor.ErrNotEnrolled
		}
		return nil, err
	}
	if !valid {
		s.audit.RecordAsync(ctx, audit.Event{
			UserID:    userID,
			Action:    audit.ActionTwoFactorFailed,
			IPAddress: rc.IPAddress,
			UserAgent: rc.UserAgent,
			Status:    audit.StatusFailure,
		})
		return nil, ErrInvalidTOTP
	}

	codes, err := s.twoFactor.EnableWithRecoveryCodes(ctx, userID)
	if err != nil {
		return nil, err
	}

	s.audit.RecordAsync(ctx, audit.Event{
		UserID:    userID,
		Action:    audit.ActionTwoFactorEnable,
		IPAddress: rc.IPAddress,
		UserAgent: rc.UserAgent,
		Status:    audit.StatusSuccess,
	})
	return codes, nil
}

// RegenerateRecoveryCodes replaces the user's recovery codes with a new set.
//
// The password is required for the reason DisableTwoFactor requires it: an
// unlocked browser left open must not be enough to mint a way past the second
// factor.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, userID, password string, rc RequestContext) ([]string, error) {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	matched, err := secrets.VerifyPassword(password, user.PasswordHash)
	if err != nil {
		return nil, fmt.Errorf("verify password: %w", err)
	}
	if !matched {
		s.audit.RecordAsync(ctx, audit.Event{
			UserID:    userID,
			Action:    audit.ActionTwoFactorRecoveryRegenerated,
			IPAddress: rc.IPAddress,
			UserAgent: rc.UserAgent,
			Status:    audit.StatusFailure,
		})
		return nil, ErrInvalidCredentials
	}

	codes, err := s.twoFactor.RegenerateRecoveryCodes(ctx, userID)
	if err != nil {
		return nil, err
	}

	s.audit.RecordAsync(ctx, audit.Event{
		UserID:    userID,
		Action:    audit.ActionTwoFactorRecoveryRegenerated,
		IPAddress: rc.IPAddress,
		UserAgent: rc.UserAgent,
		Status:    audit.StatusSuccess,
	})
	return codes, nil
}

// DisableTwoFactor removes a user's second factor.
//
// The current password is required: an unlocked browser session must not be
// enough to strip a second factor off the account.
func (s *Service) DisableTwoFactor(ctx context.Context, userID, password string, rc RequestContext) error {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return err
	}

	matched, err := secrets.VerifyPassword(password, user.PasswordHash)
	if err != nil {
		return fmt.Errorf("verify password: %w", err)
	}
	if !matched {
		s.audit.RecordAsync(ctx, audit.Event{
			UserID:    userID,
			Action:    audit.ActionTwoFactorDisabl,
			IPAddress: rc.IPAddress,
			UserAgent: rc.UserAgent,
			Status:    audit.StatusFailure,
		})
		return ErrInvalidCredentials
	}

	if err := s.twoFactor.Disable(ctx, userID); err != nil {
		return err
	}

	s.audit.RecordAsync(ctx, audit.Event{
		UserID:    userID,
		Action:    audit.ActionTwoFactorDisabl,
		IPAddress: rc.IPAddress,
		UserAgent: rc.UserAgent,
		Status:    audit.StatusSuccess,
	})
	return nil
}

// Authenticate resolves a bearer token to its claims and confirms the backing
// session is still usable.
func (s *Service) Authenticate(ctx context.Context, token string) (AccessClaims, error) {
	claims, err := s.tokens.Lookup(ctx, token)
	if err != nil {
		if errors.Is(err, ErrTokenNotFound) {
			return AccessClaims{}, ErrInvalidToken
		}
		return AccessClaims{}, err
	}

	// Redis holds the token, but Postgres is the source of truth for whether
	// the session still exists. This closes the window where a session is
	// revoked through another path without clearing Redis.
	session, err := s.sessions.GetByID(ctx, claims.SessionID)
	if err != nil {
		if errors.Is(err, sessions.ErrNotFound) {
			return AccessClaims{}, ErrInvalidToken
		}
		return AccessClaims{}, err
	}
	if !session.IsUsable(s.now()) {
		if revokeErr := s.tokens.RevokeSession(ctx, session.ID); revokeErr != nil {
			s.log.Warn("failed to clean up tokens for unusable session",
				logger.KeyError, revokeErr.Error())
		}
		return AccessClaims{}, ErrInvalidToken
	}

	return claims, nil
}

// checkRateLimits applies the per-account and per-IP throttles.
func (s *Service) checkRateLimits(ctx context.Context, username string, rc RequestContext) error {
	userResult, err := s.userLimit.Allow(ctx, username)
	if err != nil {
		// Fail closed: see ratelimit.Limiter.Allow.
		return fmt.Errorf("rate limit unavailable: %w", err)
	}
	if !userResult.Allowed {
		s.recordRateLimit(ctx, username, rc, "username")
		return ErrRateLimited
	}

	if rc.IPAddress != "" {
		ipResult, err := s.ipLimit.Allow(ctx, rc.IPAddress)
		if err != nil {
			return fmt.Errorf("rate limit unavailable: %w", err)
		}
		if !ipResult.Allowed {
			s.recordRateLimit(ctx, username, rc, "ip")
			return ErrRateLimited
		}
	}
	return nil
}

func (s *Service) recordRateLimit(ctx context.Context, username string, rc RequestContext, scope string) {
	s.log.Warn("login rate limit exceeded", "scope", scope, "remote_addr", rc.IPAddress)
	s.audit.RecordAsync(ctx, audit.Event{
		Action:    audit.ActionLoginRateLimit,
		IPAddress: rc.IPAddress,
		UserAgent: rc.UserAgent,
		Status:    audit.StatusFailure,
		// The attempted username is recorded because an operator investigating
		// a lockout needs it. It is not a secret; the password never is.
		Metadata: map[string]any{"username": username, "scope": scope},
	})
}

func (s *Service) recordLoginFailure(ctx context.Context, userID, username string, rc RequestContext, reason string) {
	s.audit.RecordAsync(ctx, audit.Event{
		UserID:    userID,
		Action:    audit.ActionLoginFailed,
		IPAddress: rc.IPAddress,
		UserAgent: rc.UserAgent,
		Status:    audit.StatusFailure,
		Metadata:  map[string]any{"username": username, "reason": reason},
	})
}

// rehashPassword upgrades a stored hash to the current parameters.
func (s *Service) rehashPassword(ctx context.Context, userID, password string) {
	newHash, err := secrets.HashPassword(password)
	if err != nil {
		s.log.Warn("failed to rehash password", logger.KeyUserID, userID, logger.KeyError, err.Error())
		return
	}
	if err := s.users.UpdatePasswordHash(ctx, userID, newHash); err != nil {
		s.log.Warn("failed to store rehashed password", logger.KeyUserID, userID, logger.KeyError, err.Error())
	}
}
