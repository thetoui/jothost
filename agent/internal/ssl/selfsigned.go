package ssl

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	"github.com/jothost/panel/shared/validate"
)

// SelfSignedValidity is how long a generated certificate lasts.
//
// A year rather than a decade: a self-signed certificate is a stand-in for a
// real one, and a short life keeps the renewal path exercised instead of
// leaving a certificate nobody thinks about until it fails.
const SelfSignedValidity = 365 * 24 * time.Hour

// GenerateSelfSigned produces a certificate and key for the given names.
//
// It is generated in-process with crypto/x509 rather than by invoking openssl.
// That removes an external dependency, and more importantly removes the last
// place a domain name would have been passed to another program as an
// argument — there is no command line here for a name to escape into.
func GenerateSelfSigned(domains []string, now time.Time) (certPEM, keyPEM []byte, err error) {
	if len(domains) == 0 {
		return nil, nil, fmt.Errorf("%w: at least one domain is required", ErrInvalidDomain)
	}

	names := make([]string, 0, len(domains))
	for _, domain := range domains {
		normalized := validate.NormalizeDomain(domain)
		if err := validate.Domain(normalized); err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrInvalidDomain, err)
		}
		names = append(names, normalized)
	}

	// P-256 rather than RSA: every current client supports it, the key is a
	// fraction of the size, and generation is fast enough that issuing a
	// certificate does not visibly block a request.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}

	// A 128-bit random serial, as required for certificates that might ever be
	// compared for uniqueness.
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   names[0],
			Organization: []string{"JotHost Panel"},
		},
		// Backdated by an hour so a small clock difference between this host
		// and a client does not make a just-issued certificate "not yet valid".
		NotBefore: now.Add(-time.Hour),
		NotAfter:  now.Add(SelfSignedValidity),

		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		// Self-signed, so it is its own issuer and must be able to sign itself.
		IsCA:     true,
		DNSNames: names,
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}
