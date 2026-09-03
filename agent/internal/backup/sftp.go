package backup

import (
	"context"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/shared/validate"
)

// The SFTP destination.
//
// # Why the openssh client rather than a Go SSH library
//
// The Agent has no third-party dependencies (see s3.go for why), and an SSH
// implementation is not something to write out by hand the way a signature is.
// So this drives the sftp client that is already on any host with OpenSSH,
// through the same allowlisted, argv-only runner as everything else.
//
// # What makes that safe
//
// sftp takes its commands from a batch file, which is the one place in this
// package where a line of text is interpreted rather than passed as an
// argument. Three things keep that from being an injection point:
//
//   - Every key in a batch line has been through validate.BackupKey, which
//     permits only letters, digits, dots, dashes, underscores and slashes. No
//     space, quote, newline or backslash can reach a batch line.
//   - Every local path in a batch line is one the Agent itself created in its
//     own working directory.
//   - The batch file is written by this package, mode 0600, in a private
//     directory, and deleted afterwards.
//
// # Host keys
//
// StrictHostKeyChecking is never turned off, and there is no option to turn it
// off. A backup sent to whatever answered on port 22 is a copy of every site on
// the host handed to a stranger, and "it stopped working after we rebuilt the
// backup server" is a far better failure than that. The destination carries the
// server's public key and this writes a known_hosts file from it.

// CommandSFTP is the allowlist name for the OpenSSH sftp client.
const CommandSFTP = "sftp"

// sftpStore writes archives to another machine over SSH.
type sftpStore struct {
	runner *command.Runner
	log    logger

	host       string
	port       int
	user       string
	root       string
	privateKey string
	hostKey    string
	workDir    string
}

// logger is the small part of slog this file uses, so the struct above does not
// need the whole thing.
type logger interface {
	Warn(msg string, args ...any)
}

// sftpStore builds and checks an SFTP destination.
func (p *Provider) sftpStore(dest Destination) (Store, error) {
	if p.runner == nil || !p.runner.Available(CommandSFTP) {
		return nil, fmt.Errorf("%w: this host has no sftp client installed",
			ErrUnsupportedDestination)
	}
	if err := validate.SFTPHost(dest.Host); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	port := dest.Port
	if port == 0 {
		port = 22
	}
	if err := validate.SFTPPort(port); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	if err := validate.SFTPUser(dest.User); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	if dest.PrivateKey == "" {
		return nil, fmt.Errorf("%w: an SFTP destination needs a private key",
			ErrDestinationFailed)
	}
	if strings.TrimSpace(dest.HostKey) == "" {
		return nil, fmt.Errorf(
			"%w: an SFTP destination needs the server's host key, or there is no way to tell it from whatever answers",
			ErrDestinationFailed)
	}
	if strings.ContainsAny(dest.HostKey, "\n\r") {
		return nil, fmt.Errorf("%w: a host key is a single line", ErrDestinationFailed)
	}

	root := strings.TrimRight(dest.Path, "/")
	if root == "" {
		root = "."
	}
	if root != "." {
		if !strings.HasPrefix(root, "/") {
			return nil, fmt.Errorf("%w: the remote directory must be an absolute path",
				ErrDestinationFailed)
		}
		if err := validate.LocalBackupRoot(root); err != nil {
			return nil, fmt.Errorf("%w: the remote directory %s", ErrDestinationFailed, err)
		}
	}

	return &sftpStore{
		runner:     p.runner,
		log:        p.log,
		host:       dest.Host,
		port:       port,
		user:       dest.User,
		root:       root,
		privateKey: dest.PrivateKey,
		hostKey:    strings.TrimSpace(dest.HostKey),
		workDir:    p.workDir,
	}, nil
}

func (s *sftpStore) Kind() string { return validate.DestinationSFTP }

// remotePath returns the absolute remote path for a key.
func (s *sftpStore) remotePath(key string) (string, error) {
	if err := validate.BackupKey(key); err != nil {
		return "", fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	if s.root == "." {
		return key, nil
	}
	return path.Join(s.root, key), nil
}

// session holds the temporary files one sftp invocation needs.
type session struct {
	dir        string
	keyPath    string
	knownHosts string
}

// begin writes the credential files for one invocation.
//
// They are written per invocation and removed afterwards rather than kept, so a
// private key belonging to somebody's backup server is on this host's disk for
// the length of one upload rather than indefinitely.
func (s *sftpStore) begin() (*session, error) {
	dir, err := os.MkdirTemp(s.workDir, "sftp-*")
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}

	sess := &session{
		dir:        dir,
		keyPath:    filepathJoin(dir, "id"),
		knownHosts: filepathJoin(dir, "known_hosts"),
	}

	key := s.privateKey
	if !strings.HasSuffix(key, "\n") {
		// OpenSSH refuses a key file whose final line has no newline, with an
		// error that says only "invalid format".
		key += "\n"
	}
	// 0600: ssh refuses to use a key file anybody else can read, and it is
	// right to.
	if err := os.WriteFile(sess.keyPath, []byte(key), 0o600); err != nil {
		sess.end()
		return nil, fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}

	// The known_hosts entry is scoped to the host and port being connected to,
	// so a key for one server cannot authenticate another.
	entry := s.host
	if s.port != 22 {
		entry = "[" + s.host + "]:" + strconv.Itoa(s.port)
	}
	line := entry + " " + s.hostKey + "\n"
	if err := os.WriteFile(sess.knownHosts, []byte(line), 0o600); err != nil {
		sess.end()
		return nil, fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	return sess, nil
}

func (s *session) end() {
	if s == nil || s.dir == "" {
		return
	}
	_ = os.RemoveAll(s.dir)
}

// filepathJoin joins two path elements. It is a function rather than a call to
// path/filepath so this file does not import a package for one use.
func filepathJoin(dir, name string) string {
	return strings.TrimRight(dir, "/") + "/" + name
}

// run executes one sftp batch.
func (s *sftpStore) run(ctx context.Context, batch []string) (command.Result, error) {
	sess, err := s.begin()
	if err != nil {
		return command.Result{}, err
	}
	defer sess.end()

	batchPath := filepathJoin(sess.dir, "batch")
	content := strings.Join(batch, "\n") + "\n"
	if err := os.WriteFile(batchPath, []byte(content), 0o600); err != nil {
		return command.Result{}, fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}

	args := []string{
		"-b", batchPath,
		"-i", sess.keyPath,
		"-P", strconv.Itoa(s.port),
		// Every one of these is a refusal, not a convenience:
		//   BatchMode      never prompt; a prompt on a daemon's stdin hangs
		//   Strict…        never trust an unknown host key
		//   UserKnownHosts the key this destination was configured with, and
		//                  nothing the host happens to have accepted before
		//   IdentitiesOnly the key given here, not whatever an agent offers
		//   PasswordAuth   off, because there is no password to give
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + sess.knownHosts,
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "IdentitiesOnly=yes",
		"-o", "PasswordAuthentication=no",
		"-o", "PubkeyAuthentication=yes",
		"-o", "ConnectTimeout=20",
		s.user + "@" + s.host,
	}

	result, err := s.runner.RunWith(ctx, CommandSFTP, command.Options{}, args...)
	if err != nil {
		return result, fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	return result, nil
}

// sftpError turns a failed batch into a message worth showing.
func sftpError(action string, result command.Result) error {
	detail := collapseWhitespace(result.Stderr)
	if detail == "" {
		detail = collapseWhitespace(result.Stdout)
	}
	if len(detail) > 512 {
		detail = detail[:512]
	}
	if detail == "" {
		detail = "the sftp client exited " + strconv.Itoa(result.ExitCode)
	}
	return fmt.Errorf("%w: could not %s: %s", ErrDestinationFailed, action, detail)
}

func (s *sftpStore) Put(ctx context.Context, key, source string) error {
	remote, err := s.remotePath(key)
	if err != nil {
		return err
	}

	// The remote directories are created one level at a time. sftp has no
	// "mkdir -p", and a mkdir of a directory that already exists fails — hence
	// the leading "-", which tells sftp to carry on past a failed line. That
	// is safe only because the *last* line, the put, has no "-": a failed
	// upload still fails the batch.
	batch := []string{}
	for _, dir := range parentDirs(remote) {
		batch = append(batch, "-mkdir "+dir)
	}
	// Uploaded under a temporary name and renamed, so a reader never sees a
	// partial archive under the name of a finished one — the same reasoning as
	// the local store. "-rm" clears a leftover from an interrupted attempt.
	staging := remote + ".partial"
	batch = append(batch,
		"-rm "+staging,
		"put "+source+" "+staging,
		"-rm "+remote,
		"rename "+staging+" "+remote,
	)

	result, err := s.run(ctx, batch)
	if err != nil {
		return err
	}
	if !result.Succeeded() {
		return sftpError("upload "+key, result)
	}
	return nil
}

func (s *sftpStore) Get(ctx context.Context, key, destination string) error {
	remote, err := s.remotePath(key)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("%w: %s already exists", ErrDestinationFailed, destination)
	}

	result, err := s.run(ctx, []string{"get " + remote + " " + destination})
	if err != nil {
		return err
	}
	if !result.Succeeded() {
		_ = os.Remove(destination)
		if strings.Contains(strings.ToLower(result.Stderr), "no such file") {
			return fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return sftpError("download "+key, result)
	}
	if _, err := os.Stat(destination); err != nil {
		return fmt.Errorf("%w: the download produced no file: %s", ErrDestinationFailed, err)
	}
	return nil
}

func (s *sftpStore) Stat(ctx context.Context, key string) (int64, error) {
	remote, err := s.remotePath(key)
	if err != nil {
		return 0, err
	}

	result, err := s.run(ctx, []string{"ls -l " + remote})
	if err != nil {
		return 0, err
	}
	if !result.Succeeded() {
		if strings.Contains(strings.ToLower(result.Stderr), "no such file") {
			return 0, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return 0, sftpError("check "+key, result)
	}

	size, ok := parseListingSize(result.Stdout)
	if !ok {
		// The object is there — ls succeeded — but its size could not be read.
		// Reporting zero would look like an empty archive, so this says it
		// does not know by returning an error the caller can distinguish.
		return 0, fmt.Errorf("%w: the server's listing of %s could not be read",
			ErrDestinationFailed, key)
	}
	return size, nil
}

func (s *sftpStore) Delete(ctx context.Context, key string) error {
	remote, err := s.remotePath(key)
	if err != nil {
		return err
	}
	// "-rm": removing something that is not there succeeds, because retention
	// runs repeatedly and an object somebody already deleted by hand must not
	// make it fail forever afterwards.
	result, err := s.run(ctx, []string{"-rm " + remote})
	if err != nil {
		return err
	}
	if !result.Succeeded() {
		return sftpError("delete "+key, result)
	}
	return nil
}

// parentDirs returns each ancestor directory of a remote path, outermost first.
func parentDirs(remote string) []string {
	dir := path.Dir(remote)
	if dir == "." || dir == "/" {
		return nil
	}

	absolute := strings.HasPrefix(dir, "/")
	segments := strings.Split(strings.Trim(dir, "/"), "/")
	dirs := make([]string, 0, len(segments))
	current := ""
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		current = current + "/" + segment
		if absolute {
			dirs = append(dirs, current)
		} else {
			dirs = append(dirs, strings.TrimPrefix(current, "/"))
		}
	}
	return dirs
}

// parseListingSize reads the size out of an "ls -l" line.
//
// The format is the server's, not a protocol, so this is deliberately forgiving:
// it takes the last line with enough fields and reads the fifth. A failure to
// parse is reported as a failure rather than as zero.
func parseListingSize(output string) (int64, bool) {
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		size, err := strconv.ParseInt(fields[4], 10, 64)
		if err != nil {
			continue
		}
		return size, true
	}
	return 0, false
}
