package ssl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/agent/internal/fsperm"
	"github.com/jothost/panel/shared/validate"
)

// CommandCertbot is certbot's name in the Agent's command allowlist.
const CommandCertbot = "certbot"

// CertbotPath is where certbot lives when installed.
const CertbotPath = "/usr/bin/certbot"

// LetsEncryptRoot is where certbot keeps the certificates it manages.
//
// Its own paths are used rather than copying the files here: certbot renews in
// place through a symlink under live/, and a copy would go stale the first time
// it renewed without the panel noticing.
const LetsEncryptRoot = "/etc/letsencrypt/live"

// ACMEChallengeDir is the webroot certbot writes HTTP-01 challenges into.
//
// One shared directory rather than each site's own document root: the files are
// transient, they are not the site's content, and a single path means the
// nginx rule that exposes them is written once and reviewed once.
const ACMEChallengeDir = "/var/www/.acme-challenge"

// Errors returned by the certbot provider.
var (
	ErrCertbotUnavailable = errors.New("certbot is not installed on this host")
	ErrIssueFailed        = errors.New("the certificate could not be issued")
)

// Certbot obtains publicly trusted certificates over ACME.
type Certbot struct {
	runner *command.Runner
	root   string
}

// NewCertbot builds a Certbot provider.
func NewCertbot(runner *command.Runner, root string) *Certbot {
	return &Certbot{runner: runner, root: root}
}

// CertbotSpec builds the allowlist entry for certbot when it is present.
func CertbotSpec() []command.Spec {
	info, err := os.Stat(CertbotPath)
	if err != nil || info.IsDir() {
		return nil
	}
	return []command.Spec{{
		Name: CommandCertbot,
		Path: CertbotPath,
		// An ACME exchange involves several round trips to a remote server and
		// a wait for the challenge to be validated.
		Timeout: 5 * 60_000_000_000,
	}}
}

// Available reports whether this host can obtain a public certificate.
func (c *Certbot) Available() bool {
	return c.runner != nil && c.runner.Available(CommandCertbot)
}

// IssueRequest describes a certificate to obtain.
type IssueRequest struct {
	// Domains are the names to cover, primary first.
	Domains []string
	// Email receives expiry warnings from the CA. Optional; without it
	// certbot registers without a contact address.
	Email string
	// Staging uses Let's Encrypt's staging environment, which issues an
	// untrusted certificate but does not consume the strict rate limits of the
	// production one. Worth using while a deployment is being set up.
	Staging bool
}

// Issue obtains a certificate through the ACME HTTP-01 challenge.
//
// The webroot plugin is used rather than certbot's nginx plugin: the nginx
// plugin rewrites the server's configuration itself, and this panel owns those
// files. Two writers editing one vhost is how a configuration ends up in a
// state neither of them expects.
func (c *Certbot) Issue(ctx context.Context, req IssueRequest, report func(int, string)) (certPath, keyPath string, err error) {
	if !c.Available() {
		return "", "", ErrCertbotUnavailable
	}

	names, err := normalizeDomains(req.Domains)
	if err != nil {
		return "", "", err
	}

	if err := c.prepareChallengeDir(); err != nil {
		return "", "", err
	}

	progress(report, 30, "Requesting a certificate from Let's Encrypt")

	args, err := c.issueArgs(names, req)
	if err != nil {
		return "", "", err
	}

	result, err := c.runner.Run(ctx, CommandCertbot, args...)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrIssueFailed, err)
	}
	if !result.Succeeded() {
		// certbot's diagnostics name the actual obstacle — DNS not resolving,
		// the challenge not reachable, a rate limit — which is exactly what an
		// operator needs. It carries no key material.
		return "", "", fmt.Errorf("%w: %s", ErrIssueFailed, summarize(result.Stderr, result.Stdout))
	}

	progress(report, 80, "Certificate issued")

	certPath = filepath.Join(LetsEncryptRoot, names[0], CertFileName)
	keyPath = filepath.Join(LetsEncryptRoot, names[0], KeyFileName)

	if _, err := os.Stat(filepath.Join(c.root, certPath)); err != nil {
		return "", "", fmt.Errorf("%w: certbot reported success but wrote no certificate", ErrIssueFailed)
	}
	return certPath, keyPath, nil
}

// issueArgs builds certbot's argument vector.
//
// Every value is either a constant or a validated domain. Nothing is
// concatenated into a string and nothing reaches a shell.
func (c *Certbot) issueArgs(names []string, req IssueRequest) ([]string, error) {
	args := []string{
		"certonly",
		// webroot, not the nginx plugin: this panel owns the vhost files.
		"--webroot",
		"--webroot-path", ACMEChallengeDir,
		// There is no terminal to answer prompts on.
		"--non-interactive",
		"--agree-tos",
		// Replace an existing certificate rather than creating a second
		// lineage named "example.com-0001", which nothing would point at.
		"--keep-until-expiring",
		"--expand",
	}

	if req.Email != "" {
		if err := validateEmail(req.Email); err != nil {
			return nil, err
		}
		args = append(args, "--email", req.Email)
	} else {
		args = append(args, "--register-unsafely-without-email")
	}

	if req.Staging {
		args = append(args, "--staging")
	}

	for _, name := range names {
		args = append(args, "-d", name)
	}
	return args, nil
}

// Renew renews an existing certificate.
func (c *Certbot) Renew(ctx context.Context, domain string, report func(int, string)) error {
	if !c.Available() {
		return ErrCertbotUnavailable
	}

	normalized := validate.NormalizeDomain(domain)
	if err := validate.Domain(normalized); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDomain, err)
	}

	if err := c.prepareChallengeDir(); err != nil {
		return err
	}

	progress(report, 40, "Renewing the certificate")

	result, err := c.runner.Run(ctx, CommandCertbot,
		"renew",
		"--cert-name", normalized,
		"--webroot",
		"--webroot-path", ACMEChallengeDir,
		"--non-interactive",
	)
	if err != nil {
		return fmt.Errorf("renew %s: %w", normalized, err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("could not renew %s: %s", normalized,
			summarize(result.Stderr, result.Stdout))
	}
	return nil
}

// Revoke revokes a certificate and deletes its lineage.
//
// Revocation is irreversible: the certificate is dead for every client that
// checks, and the only way back is to issue a new one. The caller confirms
// before reaching here.
func (c *Certbot) Revoke(ctx context.Context, domain, certPath string, report func(int, string)) error {
	if !c.Available() {
		return ErrCertbotUnavailable
	}

	normalized := validate.NormalizeDomain(domain)
	if err := validate.Domain(normalized); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDomain, err)
	}
	if certPath == "" {
		return errors.New("a certificate path is required to revoke")
	}
	if err := validatePEMPath(certPath); err != nil {
		return err
	}

	progress(report, 40, "Revoking the certificate")

	result, err := c.runner.Run(ctx, CommandCertbot,
		"revoke",
		"--cert-path", certPath,
		"--non-interactive",
		// Remove the lineage too, so a later issue starts clean rather than
		// renewing something that is already revoked.
		"--delete-after-revoke",
	)
	if err != nil {
		return fmt.Errorf("revoke %s: %w", normalized, err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("could not revoke %s: %s", normalized,
			summarize(result.Stderr, result.Stdout))
	}
	return nil
}

// prepareChallengeDir creates the webroot certbot writes challenges into.
//
// It is world-readable because nginx serves from it as an unprivileged user,
// and it holds nothing but short-lived random tokens.
func (c *Certbot) prepareChallengeDir() error {
	base := filepath.Join(c.root, ACMEChallengeDir)
	dir := filepath.Join(base, ".well-known", "acme-challenge")
	if err := fsperm.MkdirAll(dir, challengeDirMode); err != nil {
		return fmt.Errorf("create ACME challenge directory: %w", err)
	}
	// Every level is set, including ones that already exist. A plain
	// MkdirAll under the Agent's 0077 umask left .well-known and
	// acme-challenge at 0700, so nginx could not serve a single token and
	// every HTTP-01 validation failed - and those directories persist on disk
	// on hosts that ran the earlier code. These are the panel's own
	// directories, world-readable by design, so repairing them is safe.
	for _, level := range []string{base, filepath.Join(base, ".well-known"), dir} {
		if err := os.Chmod(level, challengeDirMode); err != nil {
			return fmt.Errorf("open %s to the web server: %w", level, err)
		}
	}
	return nil
}

// challengeDirMode lets nginx, running unprivileged, reach the tokens.
const challengeDirMode os.FileMode = 0o755

// normalizeDomains validates and normalises a certificate's names.
func normalizeDomains(domains []string) ([]string, error) {
	if len(domains) == 0 {
		return nil, fmt.Errorf("%w: at least one domain is required", ErrInvalidDomain)
	}

	seen := make(map[string]struct{}, len(domains))
	names := make([]string, 0, len(domains))

	for _, domain := range domains {
		normalized := validate.NormalizeDomain(domain)
		if err := validate.Domain(normalized); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidDomain, err)
		}
		// A duplicate name would make certbot refuse the whole request.
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		names = append(names, normalized)
	}
	return names, nil
}

// validateEmail checks a contact address before it becomes an argument.
func validateEmail(email string) error {
	if len(email) > 254 {
		return errors.New("email address is too long")
	}
	at := strings.IndexByte(email, '@')
	if at <= 0 || at == len(email)-1 {
		return errors.New("email address is not valid")
	}
	if strings.ContainsAny(email, " \t\n\r\x00\"'\\;&|$`") {
		return errors.New("email address contains an invalid character")
	}
	return nil
}

// validatePEMPath rejects a path that could not be a certificate file.
func validatePEMPath(path string) error {
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("certificate path %q is not absolute", path)
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("certificate path %q traverses upward", path)
	}
	if strings.ContainsAny(path, "\x00\n\r;\"'`$&|") {
		return fmt.Errorf("certificate path %q contains an invalid character", path)
	}
	return nil
}

// summarize reduces command output to something worth showing a user.
//
// certbot is verbose and its useful line is usually near the end, so the tail
// is kept rather than the head.
func summarize(streams ...string) string {
	for _, stream := range streams {
		trimmed := strings.TrimSpace(stream)
		if trimmed == "" {
			continue
		}

		lines := strings.Split(trimmed, "\n")
		if len(lines) > 6 {
			lines = lines[len(lines)-6:]
		}
		return strings.TrimSpace(strings.Join(lines, " "))
	}
	return "no diagnostic output"
}

// progress reports a step if the caller supplied a reporter.
func progress(report func(int, string), percent int, message string) {
	if report != nil {
		report(percent, message)
	}
}
