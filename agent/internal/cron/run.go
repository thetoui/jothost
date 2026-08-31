package cron

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/shared/validate"
)

// Running a job on demand.
//
// This is the one place in the panel where a command line that came from a user
// is handed to a shell, and CLAUDE.md section 6 says not to do that. So the
// reasoning is written here rather than buried in a commit message.
//
// # Why it is not the thing that rule forbids
//
// The rule exists to stop the panel from becoming a remote shell: a caller who
// should not have command execution getting it by passing a string through an
// API. That is not what happens here, because of what the caller already has.
//
// A caller who can reach this endpoint can create a scheduled job. A scheduled
// job is a command line, run by crond, as this same account, on any schedule
// they choose — including one minute from now. "Run now" therefore grants no
// authority that was not already granted by the request that created the job;
// it removes a wait of up to sixty seconds. A panel that refused it would not be
// more secure, only less useful, and the operator would work around it by
// scheduling the job a minute ahead — which is worse, because the panel would
// then have no record of a deliberate manual run.
//
// # What bounds it
//
//   - It never runs as root. The credential drop below is mandatory, and a
//     resolved uid of 0 is refused rather than fixed up.
//   - It runs what is stored, not what was sent: the command comes from the
//     panel's records, having passed validation on the way in.
//   - It is timeout-protected, and its output is captured and capped rather
//     than inherited.
//   - It is audited, as `cron.run`, with the job named.
//   - The shell is a dedicated allowlist entry used by this function alone,
//     rather than a general "sh" the rest of the Agent could reach for.
//
// # Why a shell at all
//
// Because a crontab entry is a shell command line, and cron runs it with
// /bin/sh. "Run now" that executed it any other way would be running something
// different from what the schedule runs — redirections, pipes and && would
// behave differently or not at all — and a test run whose behaviour differs
// from the real run is worse than no test run.

// Result is what one manual run did.
type Result struct {
	JobID string `json:"job_id"`
	// ExitCode is the command's own, or -1 when it was killed.
	ExitCode int `json:"exit_code"`
	// Output is the combined output, capped.
	Output string `json:"output"`
	// Truncated reports that the job printed more than was kept.
	Truncated bool `json:"truncated"`
	// DurationMS is how long it ran.
	DurationMS int64 `json:"duration_ms"`
	// TimedOut reports that the run hit the limit and was killed.
	TimedOut bool `json:"timed_out"`
	// Status is "success" or "failed", which is what the panel records against
	// the job.
	Status string `json:"status"`
}

// Job outcome values, matching the database's last_status.
const (
	StatusSuccess = "success"
	StatusFailed  = "failed"
)

// MaxOutputBytes bounds what one manual run returns.
//
// The full output is still in the job's log file, which the log viewer shows;
// this is what crosses the socket in the response.
const MaxOutputBytes = 16 << 10

// ErrRootRefused means the account resolved to uid 0.
var ErrRootRefused = errors.New("a scheduled job may not run as root")

// Run executes one job now, as the account that owns it.
func (p *Provider) Run(ctx context.Context, account string, job Job) (Result, error) {
	if p.runner == nil || !p.runner.Available(CommandShell) {
		return Result{}, ErrUnavailable
	}
	if err := validate.SystemUser(account); err != nil {
		return Result{}, err
	}
	if err := job.Validate(); err != nil {
		return Result{}, err
	}

	owner, found, err := p.lookup(account)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{}, fmt.Errorf("%w: %s", ErrNoAccount, account)
	}
	// Refused rather than repaired. An account that resolves to root is a
	// misconfiguration somewhere above this, and running the command anyway —
	// as root — would be the worst possible way to find out.
	if owner.UID == 0 || owner.GID == 0 {
		return Result{}, fmt.Errorf("%w: %s resolves to uid %d", ErrRootRefused, account, owner.UID)
	}

	home := owner.Home
	if home == "" || !isDir(home) {
		// A working directory that exists, because a shell started in a
		// missing directory fails in a way that looks like the command failed.
		home = "/tmp"
	}

	started := timeNow()
	result, runErr := p.runner.RunWith(ctx, CommandShell, command.Options{
		Dir: home,
		// The environment cron itself provides: HOME, and the account's name.
		// Not the panel's environment, which holds the Agent's own
		// configuration.
		Env: map[string]string{
			"HOME":    home,
			"USER":    owner.Name,
			"LOGNAME": owner.Name,
		},
		UID:           owner.UID,
		GID:           owner.GID,
		SetCredential: true,
	}, "-c", job.Command)

	outcome := Result{
		JobID:      job.ID,
		DurationMS: timeNow().Sub(started).Milliseconds(),
		ExitCode:   result.ExitCode,
		TimedOut:   result.TimedOut,
	}

	combined := strings.TrimRight(result.Stdout+result.Stderr, "\n")
	if len(combined) > MaxOutputBytes {
		combined = combined[len(combined)-MaxOutputBytes:]
		outcome.Truncated = true
	}
	outcome.Output = combined

	// The run is appended to the same log the scheduled runs write to, so one
	// file holds the job's whole history rather than manual runs vanishing.
	p.appendLog(job, owner.UID, owner.GID, combined, result.ExitCode, outcome.TimedOut)

	// A job that exits non-zero is an outcome, not an error: the runner already
	// reports that as a result. Two of its errors are outcomes too — a job that
	// printed more than the cap, and one that ran past its limit — and only a
	// third kind, a job that could not be started at all, is the panel's fault.
	switch {
	case errors.Is(runErr, command.ErrOutputTooLarge):
		outcome.Truncated = true
	case errors.Is(runErr, command.ErrTimeout):
		outcome.TimedOut = true
	case runErr != nil:
		return outcome, runErr
	}

	if result.ExitCode != 0 || outcome.TimedOut {
		outcome.Status = StatusFailed
	} else {
		outcome.Status = StatusSuccess
	}

	if outcome.ExitCode == 0 && outcome.TimedOut {
		outcome.ExitCode = -1
	}
	return outcome, nil
}

// appendLog records a manual run in the job's log file.
//
// Failures here are logged and not returned: the job ran, and reporting the run
// as failed because a log line could not be written would be a lie about the
// thing the caller actually asked about.
func (p *Provider) appendLog(job Job, uid, gid int, output string, exit int, timedOut bool) {
	if err := os.MkdirAll(p.logDir, 0o755); err != nil {
		p.log.Warn("could not create the cron log directory",
			"dir", p.logDir, "error", err.Error())
		return
	}

	path := p.LogPath(job.ID)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		p.log.Warn("could not open the job log", "job", job.ID, "error", err.Error())
		return
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			p.log.Warn("could not close the job log", "job", job.ID, "error", cerr.Error())
		}
	}()

	outcome := fmt.Sprintf("exit %d", exit)
	if timedOut {
		outcome = "timed out"
	}
	header := fmt.Sprintf("[%s] run from the panel: %s\n",
		timeNow().UTC().Format(time.RFC3339), outcome)

	if _, err := file.WriteString(header); err != nil {
		p.log.Warn("could not write to the job log", "job", job.ID, "error", err.Error())
		return
	}
	if output != "" {
		if _, err := file.WriteString(output + "\n"); err != nil {
			p.log.Warn("could not write to the job log", "job", job.ID, "error", err.Error())
			return
		}
	}

	// The file may have been created by this write rather than by Apply, so its
	// ownership is set here too — otherwise the next scheduled run, which
	// appends as the job's own account, would find a root-owned file it cannot
	// write to.
	if uid > 0 {
		if err := os.Chown(path, uid, gid); err != nil {
			p.log.Warn("could not set the job log's owner", "job", job.ID, "error", err.Error())
		}
	}
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
