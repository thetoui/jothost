package mail

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// The webmail release this panel installs.
//
// Pinned, with its checksum, and both are compiled in. That is the whole
// security posture of this file: the Agent is fetching a few megabytes of PHP
// over the network and about to serve it from a customer's own domain, as a
// page that will be handed every mailbox password on this host. A version taken
// from a request would be a way to ask the panel to install anything; a
// download that was not checked would be a way for anybody between this host
// and the release to replace it.
//
// Upgrading is a code change with a new checksum, deliberately. It is the one
// operation here that should require somebody to look.
const (
	// WebmailVersion is the Roundcube release.
	WebmailVersion = "1.6.9"
	// webmailURL is where it comes from. HTTPS, and a fixed host.
	webmailURL = "https://github.com/roundcube/roundcubemail/releases/download/" +
		WebmailVersion + "/roundcubemail-" + WebmailVersion + "-complete.tar.gz"
	// webmailSHA256 is the release's checksum, verified before anything is
	// unpacked.
	webmailSHA256 = "b61a5f5c22f890c299e935aacfcf0870676990d8aebff0d6cdff075bf17cef4f"
	// webmailArchiveRoot is the directory the archive unpacks into.
	webmailArchiveRoot = "roundcubemail-" + WebmailVersion
	// webmailMaxBytes bounds the download. The release is about six
	// megabytes; this is generous and finite, which is the property that
	// matters — an unbounded read from the network is a way to fill the disk.
	webmailMaxBytes = 64 << 20
	// webmailTimeout bounds the fetch.
	webmailTimeout = 10 * time.Minute
)

// WebmailRequest is what the API asks for when installing webmail.
type WebmailRequest struct {
	// DocumentRoot is where the application is unpacked. It is the document
	// root of a website the panel already created, so it has been through the
	// path checks that created it — the Agent does not accept an arbitrary
	// directory here, it accepts the one a website operation produced.
	DocumentRoot string `json:"document_root"`
	// Owner is the system account that owns the website. The files are written
	// as that account, so PHP-FPM can read them and nothing else on the host
	// can.
	Owner string `json:"owner"`
	// Domain is the name webmail is served on, which goes into the generated
	// configuration.
	Domain string `json:"domain"`
	// IMAPHost and SMTPHost are where webmail connects to read and send. They
	// are this host, always: webmail exists to serve the mailboxes on this
	// machine.
	IMAPHost string `json:"imap_host"`
	SMTPHost string `json:"smtp_host"`
}

// WebmailStatus reports what is installed.
func (p *Provider) WebmailStatus(root string) Webmail {
	if root == "" {
		return Webmail{Detail: "webmail is not installed"}
	}
	// The version file Roundcube ships. Its presence is what distinguishes an
	// installed application from a directory somebody made.
	data, err := os.ReadFile(filepath.Join(root, "index.php"))
	if err != nil || len(data) == 0 {
		return Webmail{Detail: "webmail is not installed"}
	}
	status := Webmail{Installed: true, Path: root, Version: WebmailVersion}
	if _, err := os.Stat(filepath.Join(root, "config", "config.inc.php")); err != nil {
		status.Detail = "webmail is unpacked but not configured, so it will not start"
	}
	return status
}

// InstallWebmail downloads, verifies, unpacks and configures webmail.
//
// The order is deliberate and it is the same order the backup phase uses for
// the same reason: the download is verified *before* a single file is written
// where it will be served from. An archive unpacked and then checked is one
// that was executable for the length of the check.
func (p *Provider) InstallWebmail(ctx context.Context, req WebmailRequest,
	progress func(percent int, message string),
) (Webmail, error) {
	report := func(percent int, message string) {
		if progress != nil {
			progress(percent, message)
		}
	}

	if req.DocumentRoot == "" || !filepath.IsAbs(req.DocumentRoot) {
		return Webmail{}, fmt.Errorf("%w: webmail needs the document root of a website",
			ErrWebmailUnavailable)
	}
	if req.Domain == "" {
		return Webmail{}, fmt.Errorf("%w: webmail needs the name it is served on",
			ErrWebmailUnavailable)
	}

	staging, err := os.MkdirTemp(filepath.Dir(req.DocumentRoot), ".webmail-*")
	if err != nil {
		return Webmail{}, fmt.Errorf("make room to unpack webmail: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	report(5, "Downloading webmail")
	archive := filepath.Join(staging, "webmail.tar.gz")
	if err := p.fetchWebmail(ctx, archive); err != nil {
		return Webmail{}, err
	}

	report(40, "Checking the download against its expected checksum")
	if err := verifyChecksum(archive, webmailSHA256); err != nil {
		return Webmail{}, err
	}

	report(50, "Unpacking webmail")
	unpacked := filepath.Join(staging, "tree")
	if err := extractTarGz(archive, unpacked); err != nil {
		return Webmail{}, err
	}

	source := filepath.Join(unpacked, webmailArchiveRoot)
	if _, err := os.Stat(source); err != nil {
		return Webmail{}, fmt.Errorf(
			"the webmail archive did not contain the directory it was expected to: %w", err)
	}

	report(70, "Writing the webmail configuration")
	config, err := buildWebmailConfig(req)
	if err != nil {
		return Webmail{}, err
	}
	configPath := filepath.Join(source, "config", "config.inc.php")
	// 0640: it holds the DES key that encrypts session data and the IMAP
	// password held for the length of a session. The web server reads it;
	// nothing else needs to.
	if err := writeFile(configPath, config, 0o640, -1, -1); err != nil {
		return Webmail{}, err
	}

	report(85, "Installing webmail into the document root")
	if err := replaceTree(source, req.DocumentRoot); err != nil {
		return Webmail{}, err
	}

	report(95, "Setting ownership")
	if err := p.ownWebmail(ctx, req); err != nil {
		return Webmail{}, err
	}

	report(100, "Webmail is installed")
	return p.WebmailStatus(req.DocumentRoot), nil
}

// fetchWebmail downloads the release.
//
// net/http from the standard library rather than a command, because this is the
// one place in the Agent that talks to the internet and it should be doing so
// with a client whose timeout, redirect policy and body limit are visible here
// rather than in a program's flags.
func (p *Provider) fetchWebmail(ctx context.Context, destination string) error {
	ctx, cancel := context.WithTimeout(ctx, webmailTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, webmailURL, nil)
	if err != nil {
		return fmt.Errorf("prepare the webmail download: %w", err)
	}
	client := &http.Client{
		// Redirects are followed — the release host redirects to its own
		// storage — but not indefinitely, and never away from HTTPS.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("the webmail download tried to redirect to %s, which is not HTTPS",
					req.URL.Scheme)
			}
			return nil
		},
	}

	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download webmail: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download webmail: the server answered %s", response.Status)
	}

	file, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("write the webmail download: %w", err)
	}
	defer func() { _ = file.Close() }()

	written, err := io.Copy(file, io.LimitReader(response.Body, webmailMaxBytes))
	if err != nil {
		return fmt.Errorf("write the webmail download: %w", err)
	}
	if written >= webmailMaxBytes {
		return fmt.Errorf("the webmail download was larger than %d bytes", webmailMaxBytes)
	}
	return file.Close()
}

// verifyChecksum refuses an archive that is not the one this panel pinned.
//
// Its own error, deliberately: everything else in this package that goes wrong
// is a misconfiguration, and this one is a download that was tampered with —
// which is a different thing for an operator to be told.
func verifyChecksum(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read the webmail download: %w", err)
	}
	defer func() { _ = file.Close() }()

	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return fmt.Errorf("check the webmail download: %w", err)
	}
	actual := hex.EncodeToString(digest.Sum(nil))
	if actual != expected {
		return fmt.Errorf("%w: expected %s and got %s", ErrWebmailChecksum, expected, actual)
	}
	return nil
}

// extractTarGz unpacks an archive under root.
//
// The member checks are the same three the restore path makes, for the same
// reason: an archive is data from somewhere else, and a member named
// "../../etc/cron.d/anything" is how unpacking becomes a way to write anywhere
// as root. This archive's checksum has already been verified, which makes the
// checks redundant — and they are here anyway, because "we verified it" is a
// property of today's code and the extractor will outlive it.
func extractTarGz(archive, root string) error {
	file, err := os.Open(archive)
	if err != nil {
		return fmt.Errorf("open the webmail archive: %w", err)
	}
	defer func() { _ = file.Close() }()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("read the webmail archive: %w", err)
	}
	defer func() { _ = gz.Close() }()

	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("make room for the webmail archive: %w", err)
	}

	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read the webmail archive: %w", err)
		}

		target, err := memberPath(root, header.Name)
		if err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create %s: %w", header.Name, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create the directory for %s: %w", header.Name, err)
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return fmt.Errorf("create %s: %w", header.Name, err)
			}
			// Bounded per member as well as overall: a decompression bomb is a
			// small archive containing one enormous file.
			if _, err := io.Copy(out, io.LimitReader(reader, webmailMaxBytes)); err != nil {
				_ = out.Close()
				return fmt.Errorf("write %s: %w", header.Name, err)
			}
			if err := out.Close(); err != nil {
				return fmt.Errorf("write %s: %w", header.Name, err)
			}
		default:
			// Symlinks, devices, hard links: none of them belong in a PHP
			// application, and each is a way to make extraction write
			// somewhere it should not. Skipped rather than refused, because
			// refusing would make a future release that happens to contain a
			// symlink fail to install for no security benefit.
			continue
		}
	}
}

// memberPath is the tar-slip check, in the form archive.go documents.
func memberPath(root, name string) (string, error) {
	if name == "" {
		return "", errors.New("the webmail archive has an entry with no name")
	}
	if strings.ContainsAny(name, "\\\x00") {
		return "", fmt.Errorf("the webmail archive entry %q contains a backslash or a null byte", name)
	}
	if path.IsAbs(name) {
		return "", fmt.Errorf("the webmail archive entry %q is an absolute path", name)
	}
	for _, segment := range strings.Split(strings.Trim(name, "/"), "/") {
		if segment == ".." {
			return "", fmt.Errorf("the webmail archive entry %q escapes the destination", name)
		}
	}
	cleanedRoot := filepath.Clean(root)
	target := filepath.Join(cleanedRoot, filepath.FromSlash(path.Clean(name)))
	if target != cleanedRoot && !strings.HasPrefix(target, cleanedRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("the webmail archive entry %q escapes the destination", name)
	}
	return target, nil
}

// buildWebmailConfig renders Roundcube's configuration.
func buildWebmailConfig(req WebmailRequest) ([]byte, error) {
	secret, err := randomSecret()
	if err != nil {
		return nil, err
	}

	imap := req.IMAPHost
	if imap == "" {
		imap = "localhost"
	}
	smtp := req.SMTPHost
	if smtp == "" {
		smtp = "localhost"
	}
	for _, value := range []string{imap, smtp, req.Domain} {
		if strings.ContainsAny(value, "'\\\r\n") {
			return nil, fmt.Errorf(
				"%w: %q cannot be written into the webmail configuration", ErrWebmailUnavailable, value)
		}
	}

	var out strings.Builder
	out.WriteString("<?php\n")
	out.WriteString("// Roundcube configuration, generated by JotHost Panel.\n")
	out.WriteString("// Rewritten on reinstall. Edits made here are lost.\n\n")

	// SQLite rather than MySQL. Webmail's own database holds contacts,
	// preferences and a session table — it is small, single-host, and has no
	// reason to be a second thing to back up, grant, and keep a password for.
	// A large installation would want MySQL, and that is a documented
	// limitation rather than a hidden one.
	out.WriteString("$config['db_dsnw'] = 'sqlite:///' . __DIR__ . '/../roundcube.db?mode=0640';\n\n")

	// Localhost, always. Webmail exists to serve the mailboxes on this
	// machine, and a configurable IMAP host would make this page a credential
	// collector pointed wherever somebody typed.
	fmt.Fprintf(&out, "$config['imap_host'] = 'ssl://%s:993';\n", imap)
	fmt.Fprintf(&out, "$config['smtp_host'] = 'ssl://%s:465';\n", smtp)
	out.WriteString("$config['smtp_user'] = '%u';\n")
	out.WriteString("$config['smtp_pass'] = '%p';\n\n")

	out.WriteString("// The customer types a mailbox name; the domain is added here, so\n")
	out.WriteString("// nobody has to know that their login is a full address.\n")
	fmt.Fprintf(&out, "$config['username_domain'] = '%s';\n\n", req.Domain)

	fmt.Fprintf(&out, "$config['des_key'] = '%s';\n", secret)
	out.WriteString("$config['product_name'] = 'Webmail';\n")
	out.WriteString("$config['plugins'] = ['archive', 'zipdownload'];\n")
	out.WriteString("$config['skin'] = 'elastic';\n\n")

	out.WriteString("// The installer is a page that can rewrite this configuration and\n")
	out.WriteString("// connect to arbitrary hosts. It is disabled because the panel has\n")
	out.WriteString("// already done the installing, and leaving it reachable is how a\n")
	out.WriteString("// webmail installation becomes somebody else's.\n")
	out.WriteString("$config['enable_installer'] = false;\n")

	return []byte(out.String()), nil
}

// randomSecret produces the key Roundcube encrypts session data with.
func randomSecret() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate the webmail session key: %w", err)
	}
	// Base64 without padding: the value goes inside single quotes in a PHP
	// file, and the alphabet here contains nothing that would end the string.
	return strings.TrimRight(base64.URLEncoding.EncodeToString(buf), "="), nil
}

// replaceTree moves an unpacked application into place.
//
// The existing document root is moved aside and put back if anything fails,
// which is the same shape as the restore in Phase 14 and exists for the same
// reason: the failure this guards against is an installation that got halfway
// and left a directory that is neither the old site nor the new one.
func replaceTree(source, destination string) error {
	previous := destination + ".replaced"
	_ = os.RemoveAll(previous)

	restore := false
	if _, err := os.Stat(destination); err == nil {
		if err := os.Rename(destination, previous); err != nil {
			return fmt.Errorf("move the existing document root aside: %w", err)
		}
		restore = true
	}

	if err := os.Rename(source, destination); err != nil {
		if restore {
			if renameErr := os.Rename(previous, destination); renameErr != nil {
				// Both failed. Say so completely: the operator now has a
				// document root that is not where it was, and needs to know
				// where it went.
				return fmt.Errorf(
					"install webmail: %w — and the previous document root could not be "+
						"put back, it is at %s: %v", err, previous, renameErr)
			}
		}
		return fmt.Errorf("install webmail: %w", err)
	}

	if restore {
		if err := os.RemoveAll(previous); err != nil {
			// The installation succeeded; the leftover is untidy and not a
			// failure. Reporting it as one would make a successful install
			// look broken.
			return nil
		}
	}
	return nil
}

// ownWebmail gives the unpacked application to the website's own account.
//
// Not to root. PHP-FPM runs as the site's account, and an application owned by
// root is one that cannot write its own cache or its temporary directory — so
// webmail would load, log in, and fail at the first attachment.
func (p *Provider) ownWebmail(ctx context.Context, req WebmailRequest) error {
	if req.Owner == "" || p.accounts == nil {
		return nil
	}
	uid, gid, err := p.accounts.EnsureAccount(ctx, req.Owner, req.DocumentRoot)
	if err != nil {
		return fmt.Errorf("resolve the website account %s: %w", req.Owner, err)
	}
	return filepath.WalkDir(req.DocumentRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if chownErr := os.Chown(path, uid, gid); chownErr != nil {
			return fmt.Errorf("give %s to %s: %w", path, req.Owner, chownErr)
		}
		return nil
	})
}

// RemoveWebmail deletes the application from a document root.
//
// The directory itself is left, because it belongs to a website the panel
// created and deleting it would be deleting the website.
func (p *Provider) RemoveWebmail(root string) error {
	if root == "" || !filepath.IsAbs(root) {
		return fmt.Errorf("%w: no document root to remove webmail from", ErrWebmailUnavailable)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read the webmail directory: %w", err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return fmt.Errorf("remove %s: %w", entry.Name(), err)
		}
	}
	return nil
}
