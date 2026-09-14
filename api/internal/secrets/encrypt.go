package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jothost/panel/shared/seal"
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
	// panelBackupKey is derived once, at construction, so the raw key does not
	// have to be kept around to produce it later.
	panelBackupKey []byte
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
	backupKey, err := seal.PanelBackupKey(key)
	if err != nil {
		return nil, ErrInvalidKey
	}
	return &Encrypter{aead: aead, panelBackupKey: backupKey}, nil
}

// PanelBackupKey returns the key panel-database backups are sealed with.
//
// It is derived from ENCRYPTION_KEY for that one purpose (seal.PanelBackupKey)
// and is what the Agent is given, so the Agent can seal a backup of the
// database without holding the key that decrypts the credentials inside it.
// A copy is returned; callers must not log it or persist it.
func (e *Encrypter) PanelBackupKey() []byte {
	return bytes.Clone(e.panelBackupKey)
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
