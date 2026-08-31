// Package cron writes and runs the host's scheduled jobs.
//
// # What executes a job, and what does not
//
// The panel does not run scheduled jobs. It writes crontab entries, and the
// host's cron daemon runs them — as the website's own unprivileged account, on
// the schedule that was asked for. This is worth being precise about, because
// "the panel schedules commands" sounds like the panel executes them and it
// does not: the command is *data* written into a file, and the thing that turns
// it into a process is crond, exactly as it would be for an entry the customer
// wrote themselves with `crontab -e`.
//
// That framing decides what this package has to defend against. Not what the
// command does — the account it runs as already has that authority over its own
// files, with or without this panel — but what the command could do to the
// *crontab file*, which is a line-oriented format with no quoting and no
// escaping. A command containing a newline is not a long command; it is two
// entries, the second of which the caller wrote. So the file is generated whole
// from the panel's records, every value in it is refused if it contains a line
// break (shared/validate), and nothing is ever appended to a line a user
// supplied.
//
// # The one place the panel does execute a job
//
// "Run now" runs it, and there is no way to implement that which does not
// execute the command. See run.go for why this is acceptable and what bounds
// it: in short, the same request could schedule the job one minute from now,
// so running it immediately grants no authority that was not already granted.
package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/agent/internal/sites"
	"github.com/jothost/panel/shared/validate"
)

// Allowlist key for the shell that "run now" uses. See run.go.
const CommandShell = "cron-shell"

// Where things live.
const (
	// DefaultSpoolDir is where per-user crontabs are kept. Debian, RHEL and
	// Alpine agree on this path; Alpine reaches it through a symlink from
	// /etc/crontabs, which resolves to the same directory.
	DefaultSpoolDir = "/var/spool/cron/crontabs"
	// DefaultLogDir is where each job's output is collected. A file per job,
	// so "what did last night's backup print" is a question with an answer.
	DefaultLogDir = "/var/log/jothost/cron"
)

// Errors returned by this package.
var (
	// ErrUnavailable means this host has no cron daemon to schedule with.
	ErrUnavailable = errors.New("this host has no cron daemon")
	// ErrNoAccount means the website's system account does not exist, so
	// there is nothing to own the crontab.
	ErrNoAccount = errors.New("the account for these jobs does not exist")
)

// Job is one scheduled job as the Agent needs it.
//
// It carries no website and no owner beyond the account name: what a job
// belongs to is the panel's business, and this package's business is a line in
// a file owned by a user.
type Job struct {
	// ID is the panel's identifier, used for the log file name and written
	// into the crontab as a comment so an operator reading the file over SSH
	// can find the job in the panel.
	ID string `json:"id"`
	// Name is what an operator called it.
	Name string `json:"name"`
	// Schedule is a five-field expression, already normalised.
	Schedule string `json:"schedule"`
	// Command is the command line, already built and validated.
	Command string `json:"command"`
	// Enabled decides whether the entry is written at all. A disabled job is
	// absent from the file rather than commented out: a commented entry is one
	// `crontab -e` away from running, and the panel would not know.
	Enabled bool `json:"enabled"`
}

// Validate checks a job before it can reach a file.
//
// The API validates too. This is not redundant: the Agent is the last place a
// value can be stopped before it becomes a line in a file that a daemon
// executes, and it must not depend on the correctness of its caller.
func (j Job) Validate() error {
	if err := validate.UUID(j.ID); err != nil {
		return fmt.Errorf("job id: %w", err)
	}
	if err := validate.JobName(j.Name); err != nil {
		return err
	}
	if _, err := validate.Schedule(j.Schedule); err != nil {
		return err
	}
	return validate.Command(j.Command)
}

// Provider writes crontabs and runs jobs.
type Provider struct {
	runner   *command.Runner
	users    *sites.UserProvider
	log      *slog.Logger
	spoolDir string
	logDir   string
}

// Options configure a Provider.
type Options struct {
	Runner   *command.Runner
	Users    *sites.UserProvider
	Log      *slog.Logger
	SpoolDir string
	LogDir   string
}

// NewProvider builds a Provider.
func NewProvider(opts Options) *Provider {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	spool := opts.SpoolDir
	if spool == "" {
		spool = DefaultSpoolDir
	}
	logDir := opts.LogDir
	if logDir == "" {
		logDir = DefaultLogDir
	}
	return &Provider{
		runner:   opts.Runner,
		users:    opts.Users,
		log:      log,
		spoolDir: spool,
		logDir:   logDir,
	}
}

// Available reports whether this host can schedule anything.
//
// The spool directory is the test, not the daemon's binary: a host can have
// crond installed and stopped, and jobs written while it is stopped run as soon
// as it starts. A host with no spool directory has nowhere to put them at all.
func (p *Provider) Available() bool {
	if p == nil {
		return false
	}
	info, err := os.Stat(p.spoolDir)
	return err == nil && info.IsDir()
}

// SpoolDir and LogDir report where this Provider writes.
func (p *Provider) SpoolDir() string { return p.spoolDir }
func (p *Provider) LogDir() string   { return p.logDir }

// LogPath is where one job's output is collected.
func (p *Provider) LogPath(id string) string {
	return filepath.Join(p.logDir, id+".log")
}

// Status describes the host's cron arrangement.
func (p *Provider) Status(_ context.Context) map[string]any {
	return map[string]any{
		"available": p.Available(),
		"spool_dir": p.spoolDir,
		"log_dir":   p.logDir,
	}
}

// Apply writes one account's jobs, replacing whatever the panel wrote before.
//
// The whole managed block is rewritten from the records given, rather than the
// file being edited in place. Editing means parsing what is already there, and
// a parser is a thing that can be wrong about a file somebody changed by hand;
// generating means the file always says exactly what the panel's records say.
func (p *Provider) Apply(ctx context.Context, account string, jobs []Job) (map[string]any, error) {
	if !p.Available() {
		return nil, ErrUnavailable
	}
	if err := validate.SystemUser(account); err != nil {
		return nil, err
	}

	owner, found, err := p.lookup(account)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%w: %s", ErrNoAccount, account)
	}

	written := make([]Job, 0, len(jobs))
	for _, job := range jobs {
		if err := job.Validate(); err != nil {
			return nil, err
		}
		if !job.Enabled {
			continue
		}
		// The log file is created here rather than left to the shell
		// redirection: the directory is root-owned, so a job appending to a
		// file that does not exist yet would fail on the first run and succeed
		// on none.
		if err := p.ensureLog(job.ID, owner); err != nil {
			return nil, err
		}
		written = append(written, job)
	}

	path := p.crontabPath(account)
	if err := p.write(path, written); err != nil {
		return nil, err
	}

	// The names are recorded for every job given, enabled or not: a disabled
	// job still has the output of the runs it made while it was enabled, and
	// that output should still be findable by name.
	if err := p.writeIndex(jobs); err != nil {
		return nil, err
	}

	// Touching the directory is what makes a daemon notice on hosts that watch
	// the spool directory's timestamp rather than each file's. The rename below
	// already updates it; this is belt and braces on filesystems where it does
	// not.
	if err := touch(p.spoolDir); err != nil {
		p.log.Warn("could not touch the cron spool directory",
			"dir", p.spoolDir, "error", err.Error())
	}

	_ = ctx
	return map[string]any{
		"account": account,
		"path":    path,
		"jobs":    len(written),
		"skipped": len(jobs) - len(written),
	}, nil
}

// Remove drops the panel's block from an account's crontab.
//
// Anything the customer wrote outside the block is left exactly as it was. A
// panel that deleted the file would be deleting work it never owned.
func (p *Provider) Remove(ctx context.Context, account string) (map[string]any, error) {
	if !p.Available() {
		return nil, ErrUnavailable
	}
	if err := validate.SystemUser(account); err != nil {
		return nil, err
	}

	path := p.crontabPath(account)

	// The logs of the jobs in the block go with it. Read before the write,
	// because afterwards there is nothing left to read them from.
	removed := 0
	if content, err := os.ReadFile(path); err == nil { //nolint:gosec // spoolDir + a validated account name
		for _, id := range managedJobIDs(string(content)) {
			if err := p.RemoveLog(id); err != nil {
				p.log.Warn("a job log could not be removed with its account",
					"job", id, "error", err.Error())
				continue
			}
			removed++
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read the existing crontab: %w", err)
	}

	if err := p.write(path, nil); err != nil {
		return nil, err
	}
	_ = ctx
	return map[string]any{
		"account": account, "path": path, "jobs": 0, "logs_removed": removed,
	}, nil
}

// RemoveLog deletes one job's collected output.
//
// Called when a job is deleted. A log file with no job is one nothing in the
// panel can name, and it would sit in the log picker for ever.
func (p *Provider) RemoveLog(id string) error {
	if err := validate.UUID(id); err != nil {
		return err
	}
	if err := os.Remove(p.LogPath(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove the job log: %w", err)
	}
	return p.forgetName(id)
}

// crontabPath is where one account's crontab lives.
//
// The account name has been through validate.SystemUser, so it cannot contain a
// separator or a traversal; joining is safe because of that check and not
// instead of it.
func (p *Provider) crontabPath(account string) string {
	return filepath.Join(p.spoolDir, account)
}

// lookup resolves the account that will own the file.
func (p *Provider) lookup(account string) (sites.Account, bool, error) {
	if p.users == nil {
		return sites.Account{}, false, ErrNoAccount
	}
	return p.users.Lookup(account)
}

// ensureLog creates a job's log file, owned by the account that will append to
// it.
func (p *Provider) ensureLog(id string, owner sites.Account) error {
	if err := os.MkdirAll(p.logDir, 0o755); err != nil {
		return fmt.Errorf("create the cron log directory: %w", err)
	}

	path := p.LogPath(id)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("create the job log: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close the job log: %w", err)
	}

	// 0640 and owned by the job's account: the account writes it, the panel
	// reads it as root, and no other site can read what a job printed — which
	// may be a connection string or a customer's data.
	if err := os.Chown(path, owner.UID, owner.GID); err != nil {
		return fmt.Errorf("set the job log's owner: %w", err)
	}
	return nil
}

// The index of job names.
//
// Each job's output goes to a file named after its identifier, which is exactly
// what a log picker must not show a person: "8f3c2b1a-…" names nothing. The
// panel knows the names, and this is where it leaves them for the log viewer to
// find — a small file beside the logs, rewritten whenever the jobs are.
//
// A file rather than a payload on the log request, because the two features
// should not have to be deployed in step: the log viewer asks the filesystem
// what is there, as it does for every other source it offers.
const indexName = "index"

// IndexPath is where the names are kept.
func (p *Provider) IndexPath() string { return filepath.Join(p.logDir, indexName) }

// writeIndex records the names of the jobs whose logs this directory holds.
func (p *Provider) writeIndex(jobs []Job) error {
	existing, err := p.Names()
	if err != nil {
		return err
	}
	for _, job := range jobs {
		existing[job.ID] = job.Name
	}

	var b strings.Builder
	for id, name := range existing {
		// A name with a line break could not have got this far — validation
		// refuses it — but this file is read back as lines, so the last check
		// happens where the assumption is made.
		if validate.UUID(id) != nil || strings.ContainsAny(name, "\n\r") {
			continue
		}
		b.WriteString(id)
		b.WriteString(" ")
		b.WriteString(name)
		b.WriteString("\n")
	}

	if err := os.MkdirAll(p.logDir, 0o755); err != nil {
		return fmt.Errorf("create the cron log directory: %w", err)
	}
	if err := os.WriteFile(p.IndexPath(), []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write the job name index: %w", err)
	}
	return nil
}

// Names returns the job names this host has recorded, by identifier.
//
// A missing file is an empty map rather than an error: a host that has never
// scheduled anything has no index, which is not a fault.
func (p *Provider) Names() (map[string]string, error) {
	names := map[string]string{}

	content, err := os.ReadFile(p.IndexPath()) //nolint:gosec // a path this package owns
	if err != nil {
		if os.IsNotExist(err) {
			return names, nil
		}
		return names, fmt.Errorf("read the job name index: %w", err)
	}

	for _, line := range strings.Split(string(content), "\n") {
		id, name, found := strings.Cut(strings.TrimSpace(line), " ")
		if !found || validate.UUID(id) != nil {
			continue
		}
		names[id] = name
	}
	return names, nil
}

// forgetName drops one job from the index.
func (p *Provider) forgetName(id string) error {
	names, err := p.Names()
	if err != nil {
		return err
	}
	if _, present := names[id]; !present {
		return nil
	}
	delete(names, id)

	var b strings.Builder
	for existing, name := range names {
		b.WriteString(existing)
		b.WriteString(" ")
		b.WriteString(name)
		b.WriteString("\n")
	}
	if err := os.WriteFile(p.IndexPath(), []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write the job name index: %w", err)
	}
	return nil
}

// touch updates a path's modification time.
func touch(path string) error {
	now := timeNow()
	return os.Chtimes(path, now, now)
}
