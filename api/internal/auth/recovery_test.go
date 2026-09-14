package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/secrets"
	"github.com/jothost/panel/api/internal/twofactor"
)

// Recovery codes, against a real database and the real challenge flow: the
// way back into an account whose authenticator is lost.

var recoveryClock = time.Unix(1700000000, 0).UTC()

// enableWithCodes enrols the fixture user and returns its TOTP secret and
// first recovery codes.
func (f *fixture) enableWithCodes(t *testing.T) (string, []string) {
	t.Helper()
	setup, err := f.svc.SetupTwoFactor(f.ctx, f.userID, rc())
	if err != nil {
		t.Fatalf("SetupTwoFactor: %v", err)
	}
	f.setClock(recoveryClock)
	code, err := secrets.TOTPCode(setup.Secret, recoveryClock)
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	codes, err := f.svc.EnableTwoFactor(f.ctx, f.userID, code, rc())
	if err != nil {
		t.Fatalf("EnableTwoFactor: %v", err)
	}
	return setup.Secret, codes
}

func (f *fixture) challenge(t *testing.T) string {
	t.Helper()
	result, err := f.svc.Login(f.ctx, testUsername, testPassword, rc())
	if err != nil || !result.MFARequired {
		t.Fatalf("Login: %v (mfa required: %v)", err, result.MFARequired)
	}
	return result.MFAToken
}

func (f *fixture) remaining(t *testing.T) int {
	t.Helper()
	profile, err := f.svc.Me(f.ctx, f.userID)
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	return profile.RecoveryCodesRemaining
}

func TestEnablingTwoFactorIssuesTenDistinctRecoveryCodes(t *testing.T) {
	f := newFixture(t)
	_, codes := f.enableWithCodes(t)

	if len(codes) != twofactor.RecoveryCodeCount {
		t.Fatalf("%d codes, want %d", len(codes), twofactor.RecoveryCodeCount)
	}
	seen := map[string]bool{}
	for _, code := range codes {
		if seen[code] {
			t.Fatalf("code %q issued twice", code)
		}
		seen[code] = true
	}
	if got := f.remaining(t); got != twofactor.RecoveryCodeCount {
		t.Fatalf("profile reports %d codes remaining, want %d", got, twofactor.RecoveryCodeCount)
	}
}

func TestRecoveryCodesAreNotStoredInPlaintext(t *testing.T) {
	f := newFixture(t)
	_, codes := f.enableWithCodes(t)

	rows, err := f.pool.Query(f.ctx,
		`SELECT code_hash FROM two_factor_recovery_codes WHERE user_id = $1::uuid`, f.userID)
	if err != nil {
		t.Fatalf("read codes: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var stored string
		if err := rows.Scan(&stored); err != nil {
			t.Fatal(err)
		}
		for _, code := range codes {
			normalized, _ := twofactor.NormalizeRecoveryCode(code)
			if strings.Contains(stored, normalized) || strings.Contains(stored, code) {
				t.Fatalf("a recovery code is readable in the database")
			}
		}
	}
}

func TestARecoveryCodeSignsInOnceAndOnlyOnce(t *testing.T) {
	f := newFixture(t)
	_, codes := f.enableWithCodes(t)

	pair, err := f.svc.VerifyRecoveryCode(f.ctx, f.challenge(t), codes[3], rc())
	if err != nil || pair == nil || pair.AccessToken == "" {
		t.Fatalf("a valid recovery code did not sign in: %v", err)
	}
	if !f.awaitAudit(t, audit.ActionTwoFactorRecoveryUsed) {
		t.Fatal("using a recovery code was not audited")
	}
	if got := f.remaining(t); got != twofactor.RecoveryCodeCount-1 {
		t.Fatalf("%d codes remaining after one was used, want %d", got, twofactor.RecoveryCodeCount-1)
	}

	// The same code, on a fresh challenge, is spent.
	if _, err := f.svc.VerifyRecoveryCode(f.ctx, f.challenge(t), codes[3], rc()); !errors.Is(err, ErrInvalidRecoveryCode) {
		t.Fatalf("a used recovery code gave %v, want ErrInvalidRecoveryCode", err)
	}
}

func TestARecoveryCodeIsForgivingOfHowItWasCopied(t *testing.T) {
	f := newFixture(t)
	_, codes := f.enableWithCodes(t)

	typed := " " + strings.ToUpper(strings.ReplaceAll(codes[0], "-", " ")) + " "
	if _, err := f.svc.VerifyRecoveryCode(f.ctx, f.challenge(t), typed, rc()); err != nil {
		t.Fatalf("%q (a copy of %q) was refused: %v", typed, codes[0], err)
	}
}

func TestAWrongRecoveryCodeKeepsTheChallengeAndTheAuthenticator(t *testing.T) {
	f := newFixture(t)
	secret, _ := f.enableWithCodes(t)
	token := f.challenge(t)

	for _, wrong := range []string{"2222-3333-4444-5555", "not a code", "0000-0000-0000-0000"} {
		if _, err := f.svc.VerifyRecoveryCode(f.ctx, token, wrong, rc()); !errors.Is(err, ErrInvalidRecoveryCode) {
			t.Fatalf("%q gave %v, want ErrInvalidRecoveryCode", wrong, err)
		}
	}
	code, _ := secrets.TOTPCode(secret, recoveryClock)
	if _, err := f.svc.VerifyTwoFactor(f.ctx, token, code, rc()); err != nil {
		t.Fatalf("the authenticator no longer works on the same challenge after a wrong recovery code: %v", err)
	}
	if got := f.remaining(t); got != twofactor.RecoveryCodeCount {
		t.Fatalf("wrong guesses spent codes: %d remaining", got)
	}
}

func TestRecoveryCodesAreRateLimitedLikeAuthenticatorCodes(t *testing.T) {
	f := newFixture(t)
	f.enableWithCodes(t)
	token := f.challenge(t)

	// The fixture's user limiter allows five attempts a minute, and the
	// password step above spent one.
	var err error
	for i := 0; i < 10; i++ {
		_, err = f.svc.VerifyRecoveryCode(f.ctx, token, "2222-3333-4444-5555", rc())
		if errors.Is(err, ErrRateLimited) {
			return
		}
	}
	t.Fatalf("ten wrong recovery codes were never rate limited; the last gave %v", err)
}

func TestAnotherUsersRecoveryCodeDoesNotWork(t *testing.T) {
	f := newFixture(t)
	_, codes := f.enableWithCodes(t)

	otherID := f.createUser(t, "other", testPassword, "")
	otherSetup, err := f.svc.SetupTwoFactor(f.ctx, otherID, rc())
	if err != nil {
		t.Fatal(err)
	}
	code, _ := secrets.TOTPCode(otherSetup.Secret, recoveryClock)
	if _, err := f.svc.EnableTwoFactor(f.ctx, otherID, code, rc()); err != nil {
		t.Fatal(err)
	}

	result, err := f.svc.Login(f.ctx, "other", testPassword, rc())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.VerifyRecoveryCode(f.ctx, result.MFAToken, codes[0], rc()); !errors.Is(err, ErrInvalidRecoveryCode) {
		t.Fatalf("the admin's recovery code signed in another account: %v", err)
	}
}

func TestRegeneratingRequiresThePasswordAndRetiresTheOldSet(t *testing.T) {
	f := newFixture(t)
	_, old := f.enableWithCodes(t)

	if _, err := f.svc.RegenerateRecoveryCodes(f.ctx, f.userID, "wrong-password-entirely", rc()); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("regenerating with the wrong password gave %v", err)
	}
	if _, err := f.svc.VerifyRecoveryCode(f.ctx, f.challenge(t), old[0], rc()); err != nil {
		t.Fatalf("a refused regeneration retired the old codes: %v", err)
	}

	fresh, err := f.svc.RegenerateRecoveryCodes(f.ctx, f.userID, testPassword, rc())
	if err != nil {
		t.Fatalf("RegenerateRecoveryCodes: %v", err)
	}
	if got := f.remaining(t); got != twofactor.RecoveryCodeCount {
		t.Fatalf("%d remaining after regenerating, want a full set", got)
	}
	if _, err := f.svc.VerifyRecoveryCode(f.ctx, f.challenge(t), old[1], rc()); !errors.Is(err, ErrInvalidRecoveryCode) {
		t.Fatalf("an unused code from the old set still works: %v", err)
	}
	if _, err := f.svc.VerifyRecoveryCode(f.ctx, f.challenge(t), fresh[1], rc()); err != nil {
		t.Fatalf("a code from the new set does not work: %v", err)
	}
}

func TestRegeneratingNeedsTwoFactorOn(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.RegenerateRecoveryCodes(f.ctx, f.userID, testPassword, rc()); !errors.Is(err, twofactor.ErrNotEnabled) {
		t.Fatalf("regenerating without two-factor gave %v, want ErrNotEnabled", err)
	}
}

func TestDisablingTwoFactorDestroysItsRecoveryCodes(t *testing.T) {
	f := newFixture(t)
	_, old := f.enableWithCodes(t)

	if err := f.svc.DisableTwoFactor(f.ctx, f.userID, testPassword, rc()); err != nil {
		t.Fatalf("DisableTwoFactor: %v", err)
	}
	var left int
	if err := f.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM two_factor_recovery_codes WHERE user_id = $1::uuid`, f.userID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d recovery codes survived turning two-factor off", left)
	}

	// Turned back on, the account has a new set, and the old one is not it.
	_, fresh := f.enableWithCodes(t)
	for _, code := range old {
		for _, now := range fresh {
			if code == now {
				t.Fatal("re-enabling reissued a code from before")
			}
		}
	}
	if _, err := f.svc.VerifyRecoveryCode(f.ctx, f.challenge(t), old[0], rc()); !errors.Is(err, ErrInvalidRecoveryCode) {
		t.Fatalf("a code from before two-factor was turned off still works: %v", err)
	}
}
