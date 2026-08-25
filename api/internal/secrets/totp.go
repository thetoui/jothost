package secrets

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 mandates HMAC-SHA1 for interoperable TOTP
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters. RFC 6238 defaults, which is what authenticator apps expect;
// changing them would break Google Authenticator, 1Password, and friends.
const (
	totpDigits     = 6
	totpPeriod     = 30 * time.Second
	totpSecretSize = 20 // 160 bits, the RFC 4226 recommendation
	// totpSkew allows one step either side, tolerating clock drift between the
	// server and the user's device without meaningfully widening the window.
	totpSkew = 1
)

// base32NoPad is the unpadded base32 encoding used by authenticator apps.
var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a new base32-encoded TOTP secret.
func NewTOTPSecret() (string, error) {
	buf := make([]byte, totpSecretSize)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate totp secret: %w", err)
	}
	return base32NoPad.EncodeToString(buf), nil
}

// TOTPProvisioningURI builds the otpauth:// URI an authenticator app scans.
//
// The secret appears in this URI by necessity, so callers must treat the
// result as a secret: it is returned once at setup and never logged.
func TOTPProvisioningURI(issuer, accountName, secret string) string {
	label := url.PathEscape(issuer + ":" + accountName)

	query := url.Values{}
	query.Set("secret", secret)
	query.Set("issuer", issuer)
	query.Set("algorithm", "SHA1")
	query.Set("digits", fmt.Sprint(totpDigits))
	query.Set("period", fmt.Sprint(int(totpPeriod.Seconds())))

	return "otpauth://totp/" + label + "?" + query.Encode()
}

// TOTPCode computes the code for a secret at a point in time.
func TOTPCode(secret string, at time.Time) (string, error) {
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return "", err
	}
	return totpAt(key, at.Unix()/int64(totpPeriod.Seconds())), nil
}

// VerifyTOTP reports whether code is valid for secret at time now, allowing
// one time step of drift in each direction.
//
// The comparison is constant time, and every candidate step is evaluated even
// after a match, so verification takes the same time whichever step matched.
func VerifyTOTP(secret, code string, now time.Time) (bool, error) {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false, nil
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false, nil
		}
	}

	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return false, err
	}

	counter := now.Unix() / int64(totpPeriod.Seconds())
	match := 0
	for offset := -totpSkew; offset <= totpSkew; offset++ {
		candidate := totpAt(key, counter+int64(offset))
		match |= subtle.ConstantTimeCompare([]byte(candidate), []byte(code))
	}
	return match == 1, nil
}

// decodeTOTPSecret parses the stored base32 secret.
func decodeTOTPSecret(secret string) ([]byte, error) {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	normalized = strings.TrimRight(normalized, "=")

	key, err := base32NoPad.DecodeString(normalized)
	if err != nil || len(key) == 0 {
		return nil, fmt.Errorf("invalid totp secret")
	}
	return key, nil
}

// totpAt implements the HOTP truncation of RFC 4226 for one counter value.
func totpAt(key []byte, counter int64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(counter))

	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	truncated := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff

	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, truncated%mod)
}
