package secrets

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// rfc6238Secret is the ASCII secret "12345678901234567890" from RFC 6238's
// test vectors, base32-encoded.
const rfc6238Secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func TestTOTPMatchesRFC6238Vectors(t *testing.T) {
	// RFC 6238 Appendix B, SHA-1 rows, truncated to the 6 digits this
	// implementation emits.
	cases := []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
	}

	for _, tc := range cases {
		got, err := TOTPCode(rfc6238Secret, time.Unix(tc.unix, 0).UTC())
		if err != nil {
			t.Fatalf("TOTPCode(%d): %v", tc.unix, err)
		}
		if got != tc.want {
			t.Fatalf("TOTPCode at %d = %s, want %s", tc.unix, got, tc.want)
		}
	}
}

func TestVerifyAcceptsCurrentCode(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatalf("NewTOTPSecret: %v", err)
	}

	now := time.Now()
	code, err := TOTPCode(secret, now)
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}

	ok, err := VerifyTOTP(secret, code, now)
	if err != nil {
		t.Fatalf("VerifyTOTP: %v", err)
	}
	if !ok {
		t.Fatal("the current code must verify")
	}
}

func TestVerifyToleratesOneStepOfDrift(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatalf("NewTOTPSecret: %v", err)
	}
	now := time.Unix(1700000000, 0).UTC()

	for _, offset := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		code, err := TOTPCode(secret, now.Add(offset))
		if err != nil {
			t.Fatalf("TOTPCode: %v", err)
		}
		ok, err := VerifyTOTP(secret, code, now)
		if err != nil {
			t.Fatalf("VerifyTOTP: %v", err)
		}
		if !ok {
			t.Fatalf("a code %v from now must verify", offset)
		}
	}
}

func TestVerifyRejectsCodesOutsideTheWindow(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatalf("NewTOTPSecret: %v", err)
	}
	now := time.Unix(1700000000, 0).UTC()

	// Two steps away must fail: the window is deliberately narrow so a
	// captured code expires quickly.
	for _, offset := range []time.Duration{-90 * time.Second, -60 * time.Second, 60 * time.Second, 90 * time.Second} {
		code, err := TOTPCode(secret, now.Add(offset))
		if err != nil {
			t.Fatalf("TOTPCode: %v", err)
		}
		ok, err := VerifyTOTP(secret, code, now)
		if err != nil {
			t.Fatalf("VerifyTOTP: %v", err)
		}
		if ok {
			t.Fatalf("a code %v from now must not verify", offset)
		}
	}
}

func TestVerifyRejectsMalformedCodes(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatalf("NewTOTPSecret: %v", err)
	}
	now := time.Now()

	for _, code := range []string{
		"",
		"12345",   // too short
		"1234567", // too long
		"abcdef",  // not digits
		"12345a",
		"' OR 1=1--",
	} {
		ok, err := VerifyTOTP(secret, code, now)
		if err != nil {
			t.Fatalf("VerifyTOTP(%q): %v", code, err)
		}
		if ok {
			t.Fatalf("malformed code %q must not verify", code)
		}
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	secretA, err := NewTOTPSecret()
	if err != nil {
		t.Fatalf("NewTOTPSecret: %v", err)
	}
	secretB, err := NewTOTPSecret()
	if err != nil {
		t.Fatalf("NewTOTPSecret: %v", err)
	}

	now := time.Now()
	code, err := TOTPCode(secretA, now)
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}

	ok, err := VerifyTOTP(secretB, code, now)
	if err != nil {
		t.Fatalf("VerifyTOTP: %v", err)
	}
	if ok {
		t.Fatal("a code from another secret must not verify")
	}
}

func TestNewTOTPSecretIsRandomAndBase32(t *testing.T) {
	seen := make(map[string]struct{}, 100)

	for i := 0; i < 100; i++ {
		secret, err := NewTOTPSecret()
		if err != nil {
			t.Fatalf("NewTOTPSecret: %v", err)
		}
		if _, dup := seen[secret]; dup {
			t.Fatalf("duplicate secret generated: %s", secret)
		}
		seen[secret] = struct{}{}

		// 20 bytes in unpadded base32 is 32 characters.
		if len(secret) != 32 {
			t.Fatalf("secret %q has length %d, want 32", secret, len(secret))
		}
		if strings.ContainsAny(secret, "=0189") {
			t.Fatalf("secret %q is not valid unpadded base32", secret)
		}
	}
}

func TestSecretNormalizationAcceptsUserFormatting(t *testing.T) {
	// Users often paste a secret with spaces or in lowercase.
	now := time.Now()
	code, err := TOTPCode(rfc6238Secret, now)
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}

	for _, variant := range []string{
		strings.ToLower(rfc6238Secret),
		"GEZD GNBV GY3T QOJQ GEZD GNBV GY3T QOJQ",
		rfc6238Secret + "==",
	} {
		ok, err := VerifyTOTP(variant, code, now)
		if err != nil {
			t.Fatalf("VerifyTOTP(%q): %v", variant, err)
		}
		if !ok {
			t.Fatalf("secret variant %q must be accepted", variant)
		}
	}
}

func TestInvalidSecretIsAnError(t *testing.T) {
	if _, err := TOTPCode("not!base32", time.Now()); err == nil {
		t.Fatal("an invalid secret must report an error")
	}
	if _, err := TOTPCode("", time.Now()); err == nil {
		t.Fatal("an empty secret must report an error")
	}
}

func TestProvisioningURI(t *testing.T) {
	uri := TOTPProvisioningURI("JotHost Panel", "admin", rfc6238Secret)

	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("provisioning URI is not a valid URL: %v", err)
	}
	if parsed.Scheme != "otpauth" || parsed.Host != "totp" {
		t.Fatalf("unexpected scheme/host: %s", uri)
	}

	query := parsed.Query()
	if query.Get("secret") != rfc6238Secret {
		t.Fatalf("secret missing from URI: %s", uri)
	}
	if query.Get("issuer") != "JotHost Panel" {
		t.Fatalf("issuer missing from URI: %s", uri)
	}
	if query.Get("digits") != "6" || query.Get("period") != "30" || query.Get("algorithm") != "SHA1" {
		t.Fatalf("URI must declare the RFC 6238 defaults: %s", uri)
	}
	if !strings.Contains(parsed.Path, "admin") {
		t.Fatalf("account name missing from label: %s", uri)
	}
}
