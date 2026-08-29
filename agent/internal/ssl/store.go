// Package ssl issues, stores, and inspects TLS certificates.
//
// A private key is the one file on this host whose disclosure cannot be undone
// by any later action: anyone holding it can impersonate the site until the
// certificate expires. Every path here is therefore written by root, readable
// only by root, and never logged.
package ssl

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jothost/panel/shared/validate"
)

// Providers that can obtain a certificate.
const (
	// ProviderLetsEncrypt obtains a publicly trusted certificate over ACME.
	// It requires the domain to resolve publicly and the challenge path to be
	// reachable, so it cannot run on an isolated host.
	ProviderLetsEncrypt = "letsencrypt"
	// ProviderSelfSigned generates a certificate locally. Browsers do not
	// trust it, but it is the only option for a host with no public DNS, and
	// it exercises the same serving path as a real certificate.
	ProviderSelfSigned = "selfsigned"
)

// Root is where the panel keeps certificates it generates itself.
//
// Deliberately not under /var/www: nothing beneath a document root should ever
// hold a private key, however the web server is configured.
const Root = "/etc/jothost/ssl"

// File names within a certificate's directory. They match the names certbot
// uses so an operator reading either layout sees the same words.
const (
	CertFileName  = "fullchain.pem"
	KeyFileName   = "privkey.pem"
	ChainFileName = "chain.pem"
)

// Permissions for certificate material.
const (
	// certDirMode keeps the directory listing to root. The certificate itself
	// is public, but the directory holds the key beside it.
	certDirMode os.FileMode = 0o700
	// certMode is the public half; nginx reads it as root before dropping
	// privileges, so it needs no wider access than that.
	certMode os.FileMode = 0o644
	// keyMode is the private half. Anything wider than this hands the site's
	// identity to whoever else can read it.
	keyMode os.FileMode = 0o600
)

// Errors returned by the store.
var (
	ErrNotFound      = errors.New("no certificate for this domain")
	ErrInvalidDomain = errors.New("invalid certificate domain")
	// ErrKeyExposed means a private key on disk is readable beyond root.
	ErrKeyExposed = errors.New("private key permissions are too permissive")
)

// Certificate describes one certificate on disk.
type Certificate struct {
	Domain   string   `json:"domain"`
	Domains  []string `json:"domains"`
	Provider string   `json:"provider"`

	CertPath string `json:"certificate_path"`
	KeyPath  string `json:"private_key_path"`

	Issuer      string    `json:"issuer"`
	Fingerprint string    `json:"fingerprint"`
	IssuedAt    time.Time `json:"issued_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	// SelfSigned reports whether the certificate signed itself, which is what
	// a browser will refuse rather than anything about how it was obtained.
	SelfSigned bool `json:"self_signed"`
}

// DaysRemaining reports how long the certificate is still valid for.
//
// Negative once it has expired, which the caller shows rather than clamping:
// "expired 3 days ago" is more useful than "0 days left".
func (c Certificate) DaysRemaining(now time.Time) int {
	return int(c.ExpiresAt.Sub(now).Hours() / 24)
}

// Store reads and writes certificate material under a root.
type Store struct {
	// root prefixes every path; empty in production, a temporary directory in
	// tests so certificate handling can be exercised without touching /etc.
	root string
}

// NewStore builds a Store.
func NewStore(root string) *Store {
	return &Store{root: root}
}

// DirFor returns the directory holding a domain's certificate.
//
// The path is logical: it has no test root applied, because this is the value
// that ends up in the database and in an nginx ssl_certificate directive, where
// a test prefix would be meaningless. The root is applied only at the syscall
// boundary, by realPath.
func (s *Store) DirFor(domain string) (string, error) {
	normalized := validate.NormalizeDomain(domain)
	if err := validate.Domain(normalized); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidDomain, err)
	}
	// The domain is validated as a hostname, so it cannot contain a separator
	// or a traversal; joining it is safe only because of that check.
	return filepath.Join(Root, normalized), nil
}

// realPath maps a logical path to where it actually lives.
//
// Empty root means the two are the same, which is production; a test root
// redirects every filesystem operation into a temporary directory without
// changing any path the rest of the system sees.
func (s *Store) realPath(path string) string {
	if s.root == "" {
		return path
	}
	return filepath.Join(s.root, path)
}

// PathsFor returns where a domain's certificate and key live.
func (s *Store) PathsFor(domain string) (certPath, keyPath string, err error) {
	dir, err := s.DirFor(domain)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dir, CertFileName), filepath.Join(dir, KeyFileName), nil
}

// Write stores a certificate and its key.
//
// The key is written with its final permissions from the outset rather than
// being chmod-ed afterwards: between a wide-open create and a later chmod there
// is a window in which any process on the host can read it.
func (s *Store) Write(domain string, certPEM, keyPEM []byte) (certPath, keyPath string, err error) {
	dir, err := s.DirFor(domain)
	if err != nil {
		return "", "", err
	}

	realDir := s.realPath(dir)
	if err := os.MkdirAll(realDir, certDirMode); err != nil {
		return "", "", fmt.Errorf("create certificate directory: %w", err)
	}
	// MkdirAll leaves an existing directory's mode alone, and this one may
	// predate a tightening of the constant above.
	if err := os.Chmod(realDir, certDirMode); err != nil {
		return "", "", fmt.Errorf("chmod certificate directory: %w", err)
	}

	certPath = filepath.Join(dir, CertFileName)
	keyPath = filepath.Join(dir, KeyFileName)

	if err := writeExclusive(s.realPath(keyPath), keyPEM, keyMode); err != nil {
		return "", "", fmt.Errorf("write private key: %w", err)
	}
	if err := os.WriteFile(s.realPath(certPath), certPEM, certMode); err != nil {
		return "", "", fmt.Errorf("write certificate: %w", err)
	}
	return certPath, keyPath, nil
}

// writeExclusive replaces a file, creating it with the given mode.
//
// os.WriteFile applies the mode only when it creates the file, so replacing an
// existing key would silently keep whatever permissions it already had.
// Removing first makes the mode apply every time.
func writeExclusive(path string, content []byte, mode os.FileMode) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("replace %s: %w", path, err)
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	if _, err := file.Write(content); err != nil {
		return err
	}
	return file.Sync()
}

// Remove deletes a domain's certificate material.
func (s *Store) Remove(domain string) error {
	dir, err := s.DirFor(domain)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(s.realPath(dir)); err != nil {
		return fmt.Errorf("remove certificate directory: %w", err)
	}
	return nil
}

// Load reads a certificate from explicit paths and parses its metadata.
//
// The paths are supplied rather than derived because a certbot certificate
// lives under /etc/letsencrypt, not under this store's root.
func (s *Store) Load(domain, certPath, keyPath, provider string) (Certificate, error) {
	if certPath == "" {
		return Certificate{}, ErrNotFound
	}

	pemBytes, err := os.ReadFile(s.realPath(certPath))
	if err != nil {
		if os.IsNotExist(err) {
			return Certificate{}, ErrNotFound
		}
		return Certificate{}, fmt.Errorf("read certificate: %w", err)
	}

	parsed, err := ParseCertificate(pemBytes)
	if err != nil {
		return Certificate{}, err
	}

	parsed.Domain = validate.NormalizeDomain(domain)
	parsed.CertPath = certPath
	parsed.KeyPath = keyPath
	parsed.Provider = provider
	return parsed, nil
}

// ParseCertificate reads a PEM certificate and reports what it covers.
//
// Only the leaf is parsed: it carries the names, the validity window, and the
// issuer, which is everything the panel reports. The rest of the chain matters
// to a client verifying it, not to a panel describing it.
func ParseCertificate(pemBytes []byte) (Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return Certificate{}, errors.New("file does not contain a PEM certificate")
	}

	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return Certificate{}, fmt.Errorf("parse certificate: %w", err)
	}

	// SHA-256 over the DER, which is what every tool means by "the
	// certificate's fingerprint".
	sum := sha256.Sum256(leaf.Raw)

	names := append([]string(nil), leaf.DNSNames...)
	if len(names) == 0 && leaf.Subject.CommonName != "" {
		// A certificate with no SAN is unusual and modern clients reject it,
		// but reporting the common name is more useful than reporting nothing.
		names = []string{leaf.Subject.CommonName}
	}

	return Certificate{
		Domains:     names,
		Issuer:      leaf.Issuer.CommonName,
		Fingerprint: formatFingerprint(sum[:]),
		IssuedAt:    leaf.NotBefore,
		ExpiresAt:   leaf.NotAfter,
		// A self-signed certificate names itself as its own issuer.
		SelfSigned: leaf.Issuer.String() == leaf.Subject.String(),
	}, nil
}

// formatFingerprint renders a digest as colon-separated uppercase hex.
func formatFingerprint(sum []byte) string {
	encoded := strings.ToUpper(hex.EncodeToString(sum))

	var builder strings.Builder
	for i := 0; i < len(encoded); i += 2 {
		if i > 0 {
			builder.WriteByte(':')
		}
		builder.WriteString(encoded[i : i+2])
	}
	return builder.String()
}

// CheckKeyPermissions reports whether a private key is readable beyond root.
//
// This is checked rather than assumed because a key can arrive from certbot,
// from a restored backup, or from an operator's own hand, and a key that any
// site user can read is the whole security of the certificate gone.
func (s *Store) CheckKeyPermissions(keyPath string) error {
	if keyPath == "" {
		return nil
	}

	info, err := os.Stat(s.realPath(keyPath))
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("stat private key: %w", err)
	}

	// Any group or world bit set is too much for a private key.
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf("%w: %s is mode %04o", ErrKeyExposed, keyPath, mode)
	}
	return nil
}
