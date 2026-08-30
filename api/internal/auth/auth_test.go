package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/ratelimit"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/api/internal/secrets"
	"github.com/jothost/panel/api/internal/sessions"
	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/api/internal/twofactor"
	"github.com/jothost/panel/api/internal/users"
	"github.com/jothost/panel/shared/logger"
)

const (
	testUsername = "admin"
	testPassword = "correct-horse-battery-staple"
)

// fixture bundles a live service with its dependencies for a test.
type fixture struct {
	svc       *Service
	pool      *pgxpool.Pool
	redis     *redis.Client
	users     *users.Repository
	sessions  *sessions.Repository
	rbac      *rbac.Repository
	twoFactor *twofactor.Repository
	tokens    *TokenStore
	logBuf    *bytes.Buffer
	userID    string
	ctx       context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	deps := testsupport.Require(t)
	ctx := context.Background()

	var buf bytes.Buffer
	log := logger.New(logger.Options{Service: "api", Level: "error", Output: &buf})

	encrypter, err := secrets.NewEncrypter(testsupport.TestEncryptionKey)
	if err != nil {
		t.Fatalf("NewEncrypter: %v", err)
	}

	f := &fixture{
		pool:      deps.Pool,
		redis:     deps.Redis,
		users:     users.NewRepository(deps.Pool),
		sessions:  sessions.NewRepository(deps.Pool),
		rbac:      rbac.NewRepository(deps.Pool),
		twoFactor: twofactor.NewRepository(deps.Pool, encrypter),
		tokens:    NewTokenStore(deps.Redis, 15*time.Minute),
		logBuf:    &buf,
		ctx:       ctx,
	}

	f.svc = NewService(Config{
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: time.Hour,
		MFAChallengeTTL: 5 * time.Minute,
		TOTPIssuer:      "JotHost Test",
	}, Dependencies{
		Users:       f.users,
		Sessions:    f.sessions,
		RBAC:        f.rbac,
		TwoFactor:   f.twoFactor,
		Tokens:      f.tokens,
		Audit:       audit.NewRecorder(deps.Pool, log),
		UserLimiter: ratelimit.New(deps.Redis, "test:rl:user:", 5, time.Minute),
		IPLimiter:   ratelimit.New(deps.Redis, "test:rl:ip:", 20, time.Minute),
		Log:         log,
	})

	f.userID = f.createUser(t, testUsername, testPassword, rbac.RoleAdmin)
	return f
}

func (f *fixture) createUser(t *testing.T, username, password, role string) string {
	t.Helper()

	hash, err := secrets.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	user, err := f.users.Create(f.ctx, users.CreateParams{
		Username:     username,
		PasswordHash: hash,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if role != "" {
		if err := f.rbac.AssignRole(f.ctx, user.ID, role); err != nil {
			t.Fatalf("assign role: %v", err)
		}
	}
	return user.ID
}

// setClock overrides the service clock, for TOTP and expiry tests.
func (f *fixture) setClock(at time.Time) {
	f.svc.timeSource = func() time.Time { return at }
}

func rc() RequestContext {
	return RequestContext{IPAddress: "192.0.2.10", UserAgent: "go-test"}
}

// awaitAudit waits for one audit action to be recorded for the fixture.
//
// Audit writes are asynchronous, so this polls rather than assuming the row has
// landed. It waits for the specific action the caller cares about, not merely
// for the table to be non-empty: every one of these tests logs in first, so a
// poll that stopped at the first row would return login.succeeded and report a
// missing logout that was simply still in flight.
func (f *fixture) awaitAudit(t *testing.T, want string) bool {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for {
		var found bool
		if err := f.pool.QueryRow(f.ctx,
			`SELECT EXISTS (SELECT 1 FROM audit_logs WHERE action = $1)`,
			want).Scan(&found); err != nil {
			t.Fatalf("query audit logs: %v", err)
		}
		if found {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ---------------------------------------------------------------- login

func TestLoginSucceedsWithCorrectPassword(t *testing.T) {
	f := newFixture(t)

	result, err := f.svc.Login(f.ctx, testUsername, testPassword, rc())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.MFARequired {
		t.Fatal("2FA is not enabled, so no challenge should be issued")
	}
	if result.Tokens.AccessToken == "" || result.Tokens.RefreshToken == "" {
		t.Fatal("login must return both tokens")
	}
	if result.Tokens.TokenType != "Bearer" {
		t.Fatalf("unexpected token type %q", result.Tokens.TokenType)
	}
	if result.Tokens.ExpiresIn != 900 {
		t.Fatalf("expected a 900 second access token, got %d", result.Tokens.ExpiresIn)
	}

	if !f.awaitAudit(t, audit.ActionLoginSucceeded) {
		t.Fatal("a successful login must be audited")
	}
}

func TestLoginIsCaseInsensitiveOnUsername(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.Login(f.ctx, "ADMIN", testPassword, rc()); err != nil {
		t.Fatalf("username lookup must be case-insensitive: %v", err)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	f := newFixture(t)

	_, err := f.svc.Login(f.ctx, testUsername, "wrong-password-entirely", rc())
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
	if !f.awaitAudit(t, audit.ActionLoginFailed) {
		t.Fatal("a failed login must be audited")
	}
}

func TestLoginRejectsUnknownUserWithTheSameError(t *testing.T) {
	f := newFixture(t)

	// The error must be identical to a wrong password, or the API becomes a
	// username enumeration oracle.
	_, err := f.svc.Login(f.ctx, "nosuchuser", testPassword, rc())
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestLoginRejectsInactiveAccount(t *testing.T) {
	f := newFixture(t)

	if _, err := f.pool.Exec(f.ctx,
		`UPDATE users SET status = 'disabled' WHERE id = $1::uuid`, f.userID); err != nil {
		t.Fatalf("disable user: %v", err)
	}

	_, err := f.svc.Login(f.ctx, testUsername, testPassword, rc())
	if !errors.Is(err, ErrAccountInactive) {
		t.Fatalf("expected ErrAccountInactive, got %v", err)
	}
}

func TestLoginRateLimitLocksAfterRepeatedFailures(t *testing.T) {
	f := newFixture(t)

	// The limiter allows 5 attempts per window.
	for i := 0; i < 5; i++ {
		if _, err := f.svc.Login(f.ctx, testUsername, "wrong-password-entirely", rc()); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: expected ErrInvalidCredentials, got %v", i, err)
		}
	}

	_, err := f.svc.Login(f.ctx, testUsername, "wrong-password-entirely", rc())
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited after exhausting attempts, got %v", err)
	}

	// Crucially, the correct password must also be refused while throttled,
	// otherwise the limit does not slow an online guessing attack.
	if _, err := f.svc.Login(f.ctx, testUsername, testPassword, rc()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("throttling must apply to correct credentials too, got %v", err)
	}
}

func TestSuccessfulLoginClearsTheThrottle(t *testing.T) {
	f := newFixture(t)

	for i := 0; i < 3; i++ {
		if _, err := f.svc.Login(f.ctx, testUsername, "wrong-password-entirely", rc()); err == nil {
			t.Fatal("expected a failure")
		}
	}
	if _, err := f.svc.Login(f.ctx, testUsername, testPassword, rc()); err != nil {
		t.Fatalf("Login: %v", err)
	}

	// The counter reset means a fresh budget of failures.
	for i := 0; i < 5; i++ {
		_, err := f.svc.Login(f.ctx, testUsername, "wrong-password-entirely", rc())
		if errors.Is(err, ErrRateLimited) {
			t.Fatalf("attempt %d was throttled; the counter should have reset", i)
		}
	}
}

// ------------------------------------------------------------ authenticate

func TestAuthenticateAcceptsAFreshToken(t *testing.T) {
	f := newFixture(t)

	result, err := f.svc.Login(f.ctx, testUsername, testPassword, rc())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	claims, err := f.svc.Authenticate(f.ctx, result.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if claims.UserID != f.userID || claims.Username != testUsername {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if !rbac.Has(claims.Permissions, rbac.PermUserManage) {
		t.Fatalf("admin claims must carry every permission, got %v", claims.Permissions)
	}
}

func TestAuthenticateRejectsUnknownTokens(t *testing.T) {
	f := newFixture(t)

	for _, token := range []string{"", "not-a-real-token", "../../etc/passwd"} {
		if _, err := f.svc.Authenticate(f.ctx, token); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("token %q must be rejected, got %v", token, err)
		}
	}
}

func TestAccessTokenIsNotStoredInPlaintext(t *testing.T) {
	f := newFixture(t)

	result, err := f.svc.Login(f.ctx, testUsername, testPassword, rc())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	// A dump of Redis keys must not reveal a usable token.
	keys, err := f.redis.Keys(f.ctx, "*").Result()
	if err != nil {
		t.Fatalf("list redis keys: %v", err)
	}
	for _, key := range keys {
		if bytes.Contains([]byte(key), []byte(result.Tokens.AccessToken)) {
			t.Fatalf("redis key %q contains the plaintext access token", key)
		}
	}

	// Likewise, the refresh token must be stored only as a hash.
	var stored string
	if err := f.pool.QueryRow(f.ctx, `SELECT token_hash FROM sessions LIMIT 1`).Scan(&stored); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if stored == result.Tokens.RefreshToken {
		t.Fatal("the refresh token must be hashed at rest")
	}
	if stored != secrets.HashToken(result.Tokens.RefreshToken) {
		t.Fatal("stored hash does not match the issued refresh token")
	}
}

// ---------------------------------------------------------------- refresh

func TestRefreshRotatesBothTokens(t *testing.T) {
	f := newFixture(t)

	first, err := f.svc.Login(f.ctx, testUsername, testPassword, rc())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	second, err := f.svc.Refresh(f.ctx, first.Tokens.RefreshToken, rc())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if second.RefreshToken == first.Tokens.RefreshToken {
		t.Fatal("the refresh token must be rotated")
	}
	if second.AccessToken == first.Tokens.AccessToken {
		t.Fatal("a new access token must be issued")
	}
	if _, err := f.svc.Authenticate(f.ctx, second.AccessToken); err != nil {
		t.Fatalf("the new access token must work: %v", err)
	}
	// The superseded access token must stop working immediately.
	if _, err := f.svc.Authenticate(f.ctx, first.Tokens.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("the previous access token must be revoked, got %v", err)
	}
}

func TestRefreshReuseRevokesTheWholeSession(t *testing.T) {
	f := newFixture(t)

	first, err := f.svc.Login(f.ctx, testUsername, testPassword, rc())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	second, err := f.svc.Refresh(f.ctx, first.Tokens.RefreshToken, rc())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// Replaying the original refresh token models a stolen token being used
	// after the legitimate client has already rotated.
	if _, err := f.svc.Refresh(f.ctx, first.Tokens.RefreshToken, rc()); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("a replayed refresh token must be rejected, got %v", err)
	}

	// The legitimate session must now be dead as well: the token leaked, so
	// continuing to honour it would keep the attacker in.
	if _, err := f.svc.Refresh(f.ctx, second.RefreshToken, rc()); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("reuse detection must revoke the session family, got %v", err)
	}
	if _, err := f.svc.Authenticate(f.ctx, second.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("access tokens must be revoked after reuse detection, got %v", err)
	}

	if !f.awaitAudit(t, audit.ActionRefreshReuse) {
		t.Fatal("refresh reuse must be audited")
	}
}

func TestRefreshRejectsUnknownToken(t *testing.T) {
	f := newFixture(t)

	for _, token := range []string{"", "never-issued"} {
		if _, err := f.svc.Refresh(f.ctx, token, rc()); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("token %q must be rejected, got %v", token, err)
		}
	}
}

func TestRefreshRejectsInactiveAccount(t *testing.T) {
	f := newFixture(t)

	result, err := f.svc.Login(f.ctx, testUsername, testPassword, rc())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	if _, err := f.pool.Exec(f.ctx,
		`UPDATE users SET status = 'disabled' WHERE id = $1::uuid`, f.userID); err != nil {
		t.Fatalf("disable user: %v", err)
	}

	if _, err := f.svc.Refresh(f.ctx, result.Tokens.RefreshToken, rc()); !errors.Is(err, ErrAccountInactive) {
		t.Fatalf("expected ErrAccountInactive, got %v", err)
	}
	// Disabling an account must also end its live sessions.
	if _, err := f.svc.Authenticate(f.ctx, result.Tokens.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("a disabled user's access token must stop working, got %v", err)
	}
}

// ----------------------------------------------------------------- logout

func TestLogoutRevokesImmediately(t *testing.T) {
	f := newFixture(t)

	result, err := f.svc.Login(f.ctx, testUsername, testPassword, rc())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	claims, err := f.svc.Authenticate(f.ctx, result.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	if err := f.svc.Logout(f.ctx, claims, rc()); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	// The whole point of opaque tokens: revocation is not deferred to expiry.
	if _, err := f.svc.Authenticate(f.ctx, result.Tokens.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("the access token must stop working immediately, got %v", err)
	}
	if _, err := f.svc.Refresh(f.ctx, result.Tokens.RefreshToken, rc()); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("the refresh token must stop working, got %v", err)
	}
	if !f.awaitAudit(t, audit.ActionLogout) {
		t.Fatal("logout must be audited")
	}
}

func TestLogoutDoesNotAffectOtherSessions(t *testing.T) {
	f := newFixture(t)

	first, err := f.svc.Login(f.ctx, testUsername, testPassword, rc())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	second, err := f.svc.Login(f.ctx, testUsername, testPassword, rc())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	claims, err := f.svc.Authenticate(f.ctx, first.Tokens.AccessToken)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if err := f.svc.Logout(f.ctx, claims, rc()); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	// Signing out of one browser must not sign the user out everywhere.
	if _, err := f.svc.Authenticate(f.ctx, second.Tokens.AccessToken); err != nil {
		t.Fatalf("the other session must remain valid: %v", err)
	}
}

// -------------------------------------------------------------------- me

func TestMeReturnsRolesAndPermissions(t *testing.T) {
	f := newFixture(t)

	profile, err := f.svc.Me(f.ctx, f.userID)
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if profile.Username != testUsername {
		t.Fatalf("unexpected username %q", profile.Username)
	}
	if len(profile.Roles) != 1 || profile.Roles[0] != rbac.RoleAdmin {
		t.Fatalf("unexpected roles %v", profile.Roles)
	}
	if !rbac.Has(profile.Permissions, rbac.PermUserManage) {
		t.Fatalf("admin permissions missing: %v", profile.Permissions)
	}
	if profile.TwoFactor {
		t.Fatal("2FA must report disabled by default")
	}
}

// ------------------------------------------------------------- two-factor

// enableTOTP puts the fixture user through the full enrolment flow.
func (f *fixture) enableTOTP(t *testing.T, at time.Time) string {
	t.Helper()

	setup, err := f.svc.SetupTwoFactor(f.ctx, f.userID, rc())
	if err != nil {
		t.Fatalf("SetupTwoFactor: %v", err)
	}

	f.setClock(at)
	code, err := secrets.TOTPCode(setup.Secret, at)
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	if err := f.svc.EnableTwoFactor(f.ctx, f.userID, code, rc()); err != nil {
		t.Fatalf("EnableTwoFactor: %v", err)
	}
	return setup.Secret
}

func TestTwoFactorSetupReturnsAProvisioningURI(t *testing.T) {
	f := newFixture(t)

	setup, err := f.svc.SetupTwoFactor(f.ctx, f.userID, rc())
	if err != nil {
		t.Fatalf("SetupTwoFactor: %v", err)
	}
	if setup.Secret == "" || setup.URI == "" {
		t.Fatal("setup must return a secret and a provisioning URI")
	}

	// Setup alone must not enable 2FA: the user has not proven they can
	// generate a code yet.
	profile, err := f.svc.Me(f.ctx, f.userID)
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if profile.TwoFactor {
		t.Fatal("2FA must stay disabled until a code is verified")
	}
}

func TestTwoFactorSecretIsEncryptedAtRest(t *testing.T) {
	f := newFixture(t)

	setup, err := f.svc.SetupTwoFactor(f.ctx, f.userID, rc())
	if err != nil {
		t.Fatalf("SetupTwoFactor: %v", err)
	}

	var stored string
	if err := f.pool.QueryRow(f.ctx,
		`SELECT secret_encrypted FROM two_factor_auth WHERE user_id = $1::uuid`, f.userID).Scan(&stored); err != nil {
		t.Fatalf("read secret: %v", err)
	}
	if stored == setup.Secret {
		t.Fatal("the TOTP secret must not be stored in plaintext")
	}
	if bytes.Contains([]byte(stored), []byte(setup.Secret)) {
		t.Fatal("the stored ciphertext must not contain the secret")
	}
}

func TestEnableTwoFactorRequiresAValidCode(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.SetupTwoFactor(f.ctx, f.userID, rc()); err != nil {
		t.Fatalf("SetupTwoFactor: %v", err)
	}

	if err := f.svc.EnableTwoFactor(f.ctx, f.userID, "000000", rc()); !errors.Is(err, ErrInvalidTOTP) {
		t.Fatalf("expected ErrInvalidTOTP, got %v", err)
	}
}

func TestLoginWithTwoFactorRequiresACode(t *testing.T) {
	f := newFixture(t)
	at := time.Unix(1700000000, 0).UTC()
	secret := f.enableTOTP(t, at)

	result, err := f.svc.Login(f.ctx, testUsername, testPassword, rc())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !result.MFARequired {
		t.Fatal("login must require a second factor")
	}
	if result.Tokens != nil {
		t.Fatal("no tokens may be issued before the second factor is verified")
	}
	if result.MFAToken == "" {
		t.Fatal("a challenge token must be issued")
	}

	code, err := secrets.TOTPCode(secret, at)
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}

	pair, err := f.svc.VerifyTwoFactor(f.ctx, result.MFAToken, code, rc())
	if err != nil {
		t.Fatalf("VerifyTwoFactor: %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatal("verification must issue both tokens")
	}
	if _, err := f.svc.Authenticate(f.ctx, pair.AccessToken); err != nil {
		t.Fatalf("the issued access token must work: %v", err)
	}
}

func TestChallengeTokenIsNotAnAccessToken(t *testing.T) {
	f := newFixture(t)
	f.enableTOTP(t, time.Unix(1700000000, 0).UTC())

	result, err := f.svc.Login(f.ctx, testUsername, testPassword, rc())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	// A correct password alone must not grant API access.
	if _, err := f.svc.Authenticate(f.ctx, result.MFAToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("the MFA challenge token must not authenticate, got %v", err)
	}
}

func TestVerifyTwoFactorRejectsBadCode(t *testing.T) {
	f := newFixture(t)
	f.enableTOTP(t, time.Unix(1700000000, 0).UTC())

	result, err := f.svc.Login(f.ctx, testUsername, testPassword, rc())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	if _, err := f.svc.VerifyTwoFactor(f.ctx, result.MFAToken, "000000", rc()); !errors.Is(err, ErrInvalidTOTP) {
		t.Fatalf("expected ErrInvalidTOTP, got %v", err)
	}
	// A wrong code must not consume the challenge: users mistype.
	if _, err := f.svc.VerifyTwoFactor(f.ctx, result.MFAToken, "111111", rc()); !errors.Is(err, ErrInvalidTOTP) {
		t.Fatalf("the challenge should survive a wrong code, got %v", err)
	}
}

func TestChallengeIsConsumedOnSuccess(t *testing.T) {
	f := newFixture(t)
	at := time.Unix(1700000000, 0).UTC()
	secret := f.enableTOTP(t, at)

	result, err := f.svc.Login(f.ctx, testUsername, testPassword, rc())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	code, err := secrets.TOTPCode(secret, at)
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}

	if _, err := f.svc.VerifyTwoFactor(f.ctx, result.MFAToken, code, rc()); err != nil {
		t.Fatalf("VerifyTwoFactor: %v", err)
	}
	// Replaying the same challenge and code must not mint a second session.
	if _, err := f.svc.VerifyTwoFactor(f.ctx, result.MFAToken, code, rc()); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("a consumed challenge must be rejected, got %v", err)
	}
}

func TestVerifyTwoFactorRejectsUnknownChallenge(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.VerifyTwoFactor(f.ctx, "not-a-challenge", "123456", rc()); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestSetupTwoFactorRefusesWhenAlreadyEnabled(t *testing.T) {
	f := newFixture(t)
	f.enableTOTP(t, time.Unix(1700000000, 0).UTC())

	// Re-running setup must not silently replace a working second factor.
	if _, err := f.svc.SetupTwoFactor(f.ctx, f.userID, rc()); !errors.Is(err, ErrTwoFactorEnabled) {
		t.Fatalf("expected ErrTwoFactorEnabled, got %v", err)
	}
}

func TestDisableTwoFactorRequiresThePassword(t *testing.T) {
	f := newFixture(t)
	f.enableTOTP(t, time.Unix(1700000000, 0).UTC())

	// A hijacked session must not be able to strip the second factor.
	if err := f.svc.DisableTwoFactor(f.ctx, f.userID, "wrong-password-entirely", rc()); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}

	profile, err := f.svc.Me(f.ctx, f.userID)
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if !profile.TwoFactor {
		t.Fatal("2FA must remain enabled after a failed disable attempt")
	}

	if err := f.svc.DisableTwoFactor(f.ctx, f.userID, testPassword, rc()); err != nil {
		t.Fatalf("DisableTwoFactor: %v", err)
	}

	profile, err = f.svc.Me(f.ctx, f.userID)
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if profile.TwoFactor {
		t.Fatal("2FA must be disabled after a correct password")
	}
}

// --------------------------------------------------------------- logging

func TestSecretsNeverReachTheLog(t *testing.T) {
	f := newFixture(t)

	result, err := f.svc.Login(f.ctx, testUsername, testPassword, rc())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := f.svc.Refresh(f.ctx, result.Tokens.RefreshToken, rc()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, err := f.svc.Login(f.ctx, testUsername, "wrong-password-entirely", rc()); err == nil {
		t.Fatal("expected a failure")
	}

	logs := f.logBuf.String()
	for _, secret := range []string{
		testPassword,
		"wrong-password-entirely",
		result.Tokens.AccessToken,
		result.Tokens.RefreshToken,
	} {
		if bytes.Contains([]byte(logs), []byte(secret)) {
			t.Fatalf("a secret leaked into the logs: %s", logs)
		}
	}
}

func TestAuditMetadataCarriesNoPassword(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.Login(f.ctx, testUsername, "wrong-password-entirely", rc()); err == nil {
		t.Fatal("expected a failure")
	}
	if !f.awaitAudit(t, audit.ActionLoginFailed) {
		t.Fatal("a failed login must be audited")
	}

	var metadata *string
	if err := f.pool.QueryRow(f.ctx,
		`SELECT metadata::text FROM audit_logs WHERE action = $1 LIMIT 1`,
		audit.ActionLoginFailed).Scan(&metadata); err != nil {
		t.Fatalf("read audit metadata: %v", err)
	}
	if metadata == nil {
		t.Fatal("a failed login must record metadata")
	}
	if bytes.Contains([]byte(*metadata), []byte("wrong-password-entirely")) {
		t.Fatalf("the attempted password must never be audited: %s", *metadata)
	}
}

func TestAuditLogIsAppendOnly(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.Login(f.ctx, testUsername, testPassword, rc()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !f.awaitAudit(t, audit.ActionLoginSucceeded) {
		t.Fatal("a successful login must be audited")
	}

	// CLAUDE.md section 15 and DATABASE.md section 25: history must not be
	// rewritable, even by something holding a database connection.
	if _, err := f.pool.Exec(f.ctx, `UPDATE audit_logs SET action = 'tampered'`); err == nil {
		t.Fatal("UPDATE on audit_logs must be rejected")
	}
	if _, err := f.pool.Exec(f.ctx, `DELETE FROM audit_logs`); err == nil {
		t.Fatal("DELETE on audit_logs must be rejected")
	}
}

// ---------------------------------------------------------- misc helpers

func TestNowUsesTheInjectedClock(t *testing.T) {
	f := newFixture(t)

	fixed := time.Unix(1700000000, 0).UTC()
	f.setClock(fixed)

	if got := f.svc.now(); !got.Equal(fixed) {
		t.Fatalf("now() = %v, want %v", got, fixed)
	}
}

func TestServiceLoggerIsUsable(t *testing.T) {
	// Guards against a nil logger sneaking into the dependency struct.
	f := newFixture(t)
	if f.svc.log == nil {
		t.Fatal("service logger must not be nil")
	}
	var _ *slog.Logger = f.svc.log
}
