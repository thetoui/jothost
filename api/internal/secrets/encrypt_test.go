package secrets

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

const testKey = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"

func newTestEncrypter(t *testing.T) *Encrypter {
	t.Helper()
	e, err := NewEncrypter(testKey)
	if err != nil {
		t.Fatalf("NewEncrypter: %v", err)
	}
	return e
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	e := newTestEncrypter(t)
	plaintext := []byte("JBSWY3DPEHPK3PXP")

	ciphertext, err := e.Encrypt(plaintext, "two_factor_auth:user-1")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	got, err := e.Decrypt(ciphertext, "two_factor_auth:user-1")
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round trip changed the value: %q", got)
	}
}

func TestCiphertextHidesPlaintext(t *testing.T) {
	e := newTestEncrypter(t)
	plaintext := []byte("JBSWY3DPEHPK3PXP")

	ciphertext, err := e.Encrypt(plaintext, "ctx")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if strings.Contains(ciphertext, string(plaintext)) {
		t.Fatal("ciphertext must not contain the plaintext")
	}

	raw, err := base64.RawStdEncoding.DecodeString(ciphertext)
	if err != nil {
		t.Fatalf("ciphertext is not valid base64: %v", err)
	}
	if bytes.Contains(raw, plaintext) {
		t.Fatal("decoded ciphertext must not contain the plaintext")
	}
}

func TestEncryptUsesAFreshNonce(t *testing.T) {
	e := newTestEncrypter(t)

	first, err := e.Encrypt([]byte("same"), "ctx")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	second, err := e.Encrypt([]byte("same"), "ctx")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Nonce reuse with GCM is catastrophic, so identical plaintexts must never
	// produce identical ciphertexts.
	if first == second {
		t.Fatal("encrypting the same value twice must produce different ciphertexts")
	}
}

func TestDecryptRejectsWrongContext(t *testing.T) {
	e := newTestEncrypter(t)

	ciphertext, err := e.Encrypt([]byte("secret"), "two_factor_auth:user-1")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// A ciphertext copied into a different user's row must not decrypt.
	_, err = e.Decrypt(ciphertext, "two_factor_auth:user-2")
	if !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("expected ErrDecryptFailed for a mismatched context, got %v", err)
	}
}

func TestDecryptRejectsTampering(t *testing.T) {
	e := newTestEncrypter(t)

	ciphertext, err := e.Encrypt([]byte("secret value"), "ctx")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	raw, err := base64.RawStdEncoding.DecodeString(ciphertext)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Flip a bit in the ciphertext body.
	raw[len(raw)-1] ^= 0x01
	tampered := base64.RawStdEncoding.EncodeToString(raw)

	if _, err := e.Decrypt(tampered, "ctx"); !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("expected ErrDecryptFailed for tampered ciphertext, got %v", err)
	}
}

func TestDecryptRejectsWrongKey(t *testing.T) {
	e := newTestEncrypter(t)
	ciphertext, err := e.Encrypt([]byte("secret"), "ctx")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	other, err := NewEncrypter("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("NewEncrypter: %v", err)
	}

	if _, err := other.Decrypt(ciphertext, "ctx"); !errors.Is(err, ErrDecryptFailed) {
		t.Fatalf("expected ErrDecryptFailed with a different key, got %v", err)
	}
}

func TestDecryptRejectsMalformedInput(t *testing.T) {
	e := newTestEncrypter(t)

	for _, input := range []string{"", "!!!not base64!!!", "c2hvcnQ"} {
		if _, err := e.Decrypt(input, "ctx"); err == nil {
			t.Fatalf("malformed ciphertext %q must be rejected", input)
		}
	}
}

func TestNewEncrypterRejectsBadKeys(t *testing.T) {
	for _, key := range []string{
		"",
		"tooshort",
		"zz1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0", // not hex
		"0f1e2d3c4b5a69788796a5b4c3d2e1f0",                                 // 16 bytes
	} {
		if _, err := NewEncrypter(key); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("key %q must be rejected with ErrInvalidKey, got %v", key, err)
		}
	}
}

func TestGenerateKeyProducesUsableKeys(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if len(key) != KeyLength*2 {
		t.Fatalf("expected %d hex characters, got %d", KeyLength*2, len(key))
	}

	e, err := NewEncrypter(key)
	if err != nil {
		t.Fatalf("a generated key must be accepted: %v", err)
	}

	ciphertext, err := e.Encrypt([]byte("value"), "ctx")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := e.Decrypt(ciphertext, "ctx"); err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
}
