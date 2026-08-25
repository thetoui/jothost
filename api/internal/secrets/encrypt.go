package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

// KeyLength is the required ENCRYPTION_KEY size in bytes (AES-256).
const KeyLength = 32

// ErrInvalidKey and ErrDecryptFailed report encryption problems without
// disclosing anything about the key or the ciphertext.
var (
	ErrInvalidKey     = fmt.Errorf("encryption key must be %d bytes (64 hex characters)", KeyLength)
	ErrDecryptFailed  = errors.New("decryption failed")
	ErrCiphertextForm = errors.New("malformed ciphertext")
)

// Encrypter provides authenticated encryption for values stored in the
// database (DATABASE.md section 30): 2FA secrets today, provider API tokens
// and database passwords in later phases.
type Encrypter struct {
	aead cipher.AEAD
}

// NewEncrypter builds an AES-256-GCM encrypter from a hex-encoded key.
func NewEncrypter(hexKey string) (*Encrypter, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil || len(key) != KeyLength {
		return nil, ErrInvalidKey
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrInvalidKey
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrInvalidKey
	}
	return &Encrypter{aead: aead}, nil
}

// GenerateKey returns a new hex-encoded encryption key, for operators running
// first-time setup.
func GenerateKey() (string, error) {
	key := make([]byte, KeyLength)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	return hex.EncodeToString(key), nil
}

// Encrypt seals plaintext, returning base64(nonce||ciphertext||tag).
//
// context is bound as additional authenticated data, so a ciphertext copied
// from one row into another (a different user's 2FA secret, say) fails to
// decrypt rather than silently succeeding.
func (e *Encrypter) Encrypt(plaintext []byte, context string) (string, error) {
	nonce := make([]byte, e.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	sealed := e.aead.Seal(nonce, nonce, plaintext, []byte(context))
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Decrypt opens a ciphertext produced by Encrypt with the same context.
func (e *Encrypter) Decrypt(encoded, context string) ([]byte, error) {
	raw, err := base64.RawStdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, ErrCiphertextForm
	}

	nonceSize := e.aead.NonceSize()
	if len(raw) < nonceSize+e.aead.Overhead() {
		return nil, ErrCiphertextForm
	}

	plaintext, err := e.aead.Open(nil, raw[:nonceSize], raw[nonceSize:], []byte(context))
	if err != nil {
		// The underlying error distinguishes tampering from a wrong key, which
		// is not information a caller should act on or log.
		return nil, ErrDecryptFailed
	}
	return plaintext, nil
}
