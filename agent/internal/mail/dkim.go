package mail

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jothost/panel/shared/validate"
)

// DKIMBits is the size of the signing keys this panel generates.
//
// 2048 rather than 1024, which some hosts still use: 1024-bit RSA is within
// reach and a forged signature is worse than no signature, because it is
// believed. Not 4096 either, and that is the less obvious choice — a 4096-bit
// public key does not fit in a single DNS character-string and several
// resolvers in the wild still mishandle the multi-string form, so the record
// would be published correctly and read as absent by a fraction of the
// internet. 2048 is what every large mail provider signs with.
const DKIMBits = 2048

// GenerateDKIM creates a signing key for a domain and returns the public half.
//
// The private key is written to this host and **is not returned**. That is the
// decision this phase makes most deliberately: the panel's control-plane
// database is backed up, replicated, and read by every part of the API, and a
// signing key stored there is a key that leaves with any one of those. It lives
// on the machine that signs with it and nowhere else.
//
// What that costs is honest and small: a host rebuilt from nothing has no keys,
// so the panel reports its domains as not signing and the operator generates
// new ones — a minute of work and a DNS change. What the alternative costs is
// every customer's domain reputation at once.
func (p *Provider) GenerateDKIM(ctx context.Context, domain, selector string) (DKIMKey, error) {
	if err := validate.MailDomain(domain); err != nil {
		return DKIMKey{}, err
	}
	if err := validate.DKIMSelector(selector); err != nil {
		return DKIMKey{}, err
	}
	if _, _, err := p.ensureDirs(ctx); err != nil {
		return DKIMKey{}, err
	}

	key, err := rsa.GenerateKey(rand.Reader, DKIMBits)
	if err != nil {
		return DKIMKey{}, fmt.Errorf("generate a %d-bit signing key for %s: %w",
			DKIMBits, domain, err)
	}

	// PKCS#8 rather than PKCS#1: it is what OpenSSL writes by default now and
	// what every signer reads, and the header says which algorithm the key is
	// rather than leaving it to be inferred.
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return DKIMKey{}, fmt.Errorf("encode the signing key for %s: %w", domain, err)
	}
	private := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	path := p.paths.DKIMKeyPath(domain, selector)
	uid, gid := p.signerOwnership()
	// 0640 and group-owned by the signer, not 0600 and root: Rspamd reads this
	// file as its own unprivileged user, and a key it cannot read is a domain
	// that quietly stops being signed. Nothing else on the host is in that
	// group.
	if err := writeFile(path, private, 0o640, uid, gid); err != nil {
		return DKIMKey{}, err
	}

	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return DKIMKey{}, fmt.Errorf("encode the public key for %s: %w", domain, err)
	}

	return DKIMKey{
		Domain:   domain,
		Selector: selector,
		// Base64 of the SubjectPublicKeyInfo, which is exactly what goes in the
		// record's p= tag. The record itself is not built here: a DNS record is
		// Phase 13's to write, and a second place that knows how to spell one
		// is a second place that can spell it differently.
		PublicKey: base64.StdEncoding.EncodeToString(publicDER),
		KeyPath:   path,
		Bits:      DKIMBits,
	}, nil
}

// RemoveDKIM deletes a domain's signing key from this host.
//
// Used when a domain is removed, and when a key is rotated: the old key is
// deleted only after the new one is in place, so there is no moment where the
// domain has none.
func (p *Provider) RemoveDKIM(domain, selector string) error {
	if err := validate.MailDomain(domain); err != nil {
		return err
	}
	if selector == "" {
		return nil
	}
	if err := validate.DKIMSelector(selector); err != nil {
		return err
	}
	if err := os.Remove(p.paths.DKIMKeyPath(domain, selector)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove the signing key for %s: %w", domain, err)
	}
	return nil
}

// signingDomains reports which domains have a usable private key on this host.
//
// "Usable" means the file is there and is readable — not that it is the key
// whose public half the panel published. The panel cannot know that from here,
// and the comparison that matters is made by the API against what DNS is
// actually serving. This is the host's half of that answer.
func (p *Provider) signingDomains(desired Desired) []string {
	var signing []string
	for _, domain := range desired.Domains {
		if !domain.Active || domain.DKIMSelector == "" {
			continue
		}
		path := p.paths.DKIMKeyPath(domain.Name, domain.DKIMSelector)
		info, err := os.Stat(path)
		if err != nil || info.Size() == 0 {
			continue
		}
		signing = append(signing, domain.Name)
	}
	sort.Strings(signing)
	return signing
}

// buildDKIMMap renders the domain-to-selector table Rspamd signs from.
//
// One line per domain. Rspamd resolves the key path from the domain and the
// selector, so this file is the whole of what tells it which key to use — a
// domain missing from it is a domain whose mail goes out unsigned, and Rspamd
// says nothing about it.
func buildDKIMMap(desired Desired) []byte {
	type pair struct{ domain, selector string }
	var pairs []pair
	for _, domain := range desired.Domains {
		if !domain.Active || domain.DKIMSelector == "" {
			continue
		}
		pairs = append(pairs, pair{domain: domain.Name, selector: domain.DKIMSelector})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].domain < pairs[j].domain })

	var out strings.Builder
	out.WriteString("# Which selector each domain signs with.\n")
	out.WriteString("# Generated by JotHost Panel; rewritten whole on every change.\n")
	out.WriteString("# A domain missing here sends unsigned mail, silently.\n")
	for _, entry := range pairs {
		out.WriteString(entry.domain + " " + entry.selector + "\n")
	}
	return []byte(out.String())
}

// signerOwnership resolves the account Rspamd runs as.
func (p *Provider) signerOwnership() (int, int) {
	return accountOwnership("rspamd", "_rspamd")
}

// authOwnership resolves the account Dovecot's authentication process runs as.
//
// It is not root, which is the thing worth knowing here and which was found by
// asking the daemon rather than by reading about it: Dovecot's auth process
// drops to its internal user, so a passwd-file that only root can read produces
// "auth failed" for a correct password — with no indication anywhere that the
// problem is a permission. The integration suite asks Dovecot to authenticate a
// real mailbox for exactly this reason.
func (p *Provider) authOwnership() (int, int) {
	return accountOwnership("dovecot", "_dovecot", "dovenull")
}

// accountOwnership resolves the first of these accounts that exists.
//
// Returns -1, -1 when none does, which writeFile and ensureDir read as "leave
// the ownership alone". That is the right answer on a host without the daemon:
// the file is written root-owned, nothing can read it, and nothing is trying
// to.
func accountOwnership(names ...string) (int, int) {
	for _, name := range names {
		account, err := user.Lookup(name)
		if err != nil {
			continue
		}
		uid, uidErr := strconv.Atoi(account.Uid)
		gid, gidErr := strconv.Atoi(account.Gid)
		if uidErr != nil || gidErr != nil {
			continue
		}
		return uid, gid
	}
	return -1, -1
}

// DKIMRecordName is the owner name a domain's key is published under.
//
// Exported because the API builds the DNS record and needs to agree with the
// Agent about where it goes; a second spelling of "_domainkey" would be a
// record published somewhere nothing looks.
func DKIMRecordName(selector string) string {
	return selector + "._domainkey"
}

// DefaultSelector is the selector a domain gets when the panel generates its
// first key.
//
// A date rather than "default" or "mail", because a selector's whole purpose is
// to let a domain have two keys at once during a rotation — and a fixed name
// makes that impossible, so the panel would have to break signing for a moment
// on every rotation.
func DefaultSelector(year int, month int) string {
	return fmt.Sprintf("jh%04d%02d", year, month)
}

// dkimKeyExists reports whether a key file is present and non-empty.
func (p *Provider) dkimKeyExists(domain, selector string) bool {
	if domain == "" || selector == "" {
		return false
	}
	info, err := os.Stat(filepath.Clean(p.paths.DKIMKeyPath(domain, selector)))
	return err == nil && info.Size() > 0
}
