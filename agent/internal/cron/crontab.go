package cron

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jothost/panel/shared/validate"
)

// The panel's block inside a user's crontab.
//
// A block rather than the whole file, because the file is not the panel's. A
// customer with SSH may have written entries there years before this panel
// existed, and deleting them because the panel now manages *some* of that file
// would be destroying work it never owned. So the panel owns what lies between
// these two lines and nothing else, and every write preserves the rest exactly.
const (
	beginMarker = "# BEGIN JotHost scheduled jobs — managed by the panel, do not edit"
	endMarker   = "# END JotHost scheduled jobs"
)

// timeNow is a variable so tests can be deterministic.
var timeNow = time.Now

// render builds the managed block for a set of jobs.
//
// Deliberately absent: SHELL, PATH and MAILTO assignments. A crontab
// environment assignment applies to every entry *after* it in the file, so
// setting one inside this block would silently change the behaviour of the
// customer's own entries below the end marker. The panel does not get to do
// that to a file it shares.
func (p *Provider) render(jobs []Job) string {
	var b strings.Builder

	b.WriteString(beginMarker)
	b.WriteString("\n")
	b.WriteString("# Written ")
	b.WriteString(timeNow().UTC().Format(time.RFC3339))
	b.WriteString(". Changes inside this block are overwritten by the panel;\n")
	b.WriteString("# anything outside it is left alone.\n")

	for _, job := range jobs {
		// The name is a comment and the id is how an operator reading this file
		// over SSH finds the job in the panel. Both have been through
		// validation that refuses line breaks, which is what keeps a comment a
		// comment.
		fmt.Fprintf(&b, "# %s (job %s)\n", job.Name, job.ID)
		// The output is redirected into the job's own log rather than left to
		// cron. What cron does with output by default is mail it to the
		// account, which on a host with no mail server means it is discarded —
		// so "the job ran and printed an error" and "the job ran silently"
		// would look identical, which is the state this whole arrangement
		// exists to avoid.
		//
		// The path is the Agent's, built from the job's identifier, and the
		// identifier has been checked to be one. Nothing a caller wrote appears
		// in this part of the line.
		//
		// The command is wrapped in a subshell so that the redirection applies
		// to all of it. Without the parentheses, `a; b >> log` redirects only
		// b — which is not a corner case: "run this, then that" is what half
		// the command lines anyone writes look like, and the missing output
		// would be the half that failed.
		fmt.Fprintf(&b, "%s ( %s ) >> %s 2>&1\n", job.Schedule, job.Command, p.LogPath(job.ID))
	}

	b.WriteString(endMarker)
	b.WriteString("\n")
	return b.String()
}

// managedJobIDs reads back the identifiers of the jobs in a managed block.
//
// It parses a format this package wrote, which is the only reason parsing is
// acceptable here: the comment above each entry is "# NAME (job UUID)", written
// a few lines above by render, and each identifier is checked to be one before
// it is used for anything.
//
// It exists so that removing an account's block can also remove the logs of the
// jobs that were in it. Without it, deleting a website leaves a log file per
// job that nothing in the panel can name, sitting in the log viewer for ever.
func managedJobIDs(existing string) []string {
	inside := false
	ids := make([]string, 0, 8)

	for _, line := range strings.Split(existing, "\n") {
		trimmed := strings.TrimRight(line, " 	")
		switch {
		case trimmed == beginMarker:
			inside = true
			continue
		case trimmed == endMarker:
			inside = false
			continue
		}
		if !inside {
			continue
		}

		_, rest, found := strings.Cut(trimmed, "(job ")
		if !found {
			continue
		}
		id, _, found := strings.Cut(rest, ")")
		if !found || validate.UUID(id) != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// merge replaces the managed block in an existing crontab, or appends one.
//
// An unterminated begin marker — someone deleted the end line by hand — is
// treated as running to the end of the file. The alternative is to give up and
// refuse to write, which leaves the panel unable to manage jobs until a person
// edits a file they may not know exists.
func merge(existing string, block string) string {
	lines := strings.Split(existing, "\n")

	start, end := -1, -1
	for i, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		switch {
		case trimmed == beginMarker && start < 0:
			start = i
		case trimmed == endMarker && start >= 0 && end < 0:
			end = i
		}
	}

	var before, after []string
	switch {
	case start < 0:
		// No block yet: keep the file and add one at the end.
		before = lines
	case end < 0:
		// A begin with no end. Everything from it onwards is treated as ours.
		before = lines[:start]
	default:
		before = lines[:start]
		after = lines[end+1:]
	}

	var b strings.Builder
	if joined := trimTrailingBlanks(before); joined != "" {
		b.WriteString(joined)
		b.WriteString("\n")
	}
	if block != "" {
		b.WriteString(block)
	}
	if joined := trimLeadingBlanks(after); joined != "" {
		b.WriteString(joined)
		b.WriteString("\n")
	}

	rendered := b.String()
	// A crontab must end with a newline. Vixie cron silently ignores a final
	// line without one, which produces a job that is in the file and never
	// runs — the least debuggable failure this package could cause.
	if rendered != "" && !strings.HasSuffix(rendered, "\n") {
		rendered += "\n"
	}
	return rendered
}

func trimTrailingBlanks(lines []string) string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

func trimLeadingBlanks(lines []string) string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// write replaces the managed block in one account's crontab.
//
// Written to a temporary file in the same directory and renamed into place, so
// a daemon reading the file never sees a half-written one: rename is atomic
// within a filesystem, and a crontab caught mid-write is a set of jobs that
// silently stops running.
func (p *Provider) write(path string, jobs []Job) error {
	existing := ""
	if content, err := os.ReadFile(path); err == nil { //nolint:gosec // path is spoolDir + a validated account name
		existing = string(content)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read the existing crontab: %w", err)
	}

	block := ""
	if len(jobs) > 0 {
		block = p.render(jobs)
	}
	merged := merge(existing, block)

	if strings.TrimSpace(merged) == "" {
		// Nothing left: no panel jobs and nothing the customer wrote. Removing
		// the file is tidier than leaving an empty one, and is what `crontab
		// -r` would do.
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove the empty crontab: %w", err)
		}
		return nil
	}

	temp, err := os.CreateTemp(filepath.Dir(path), ".jothost-crontab-*")
	if err != nil {
		return fmt.Errorf("create a temporary crontab: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		// Best effort: on the success path the file has already been renamed
		// away and this fails harmlessly.
		_ = os.Remove(tempPath)
	}()

	if _, err := temp.WriteString(merged); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write the crontab: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close the crontab: %w", err)
	}

	// Mode 0600, owned by root.
	//
	// Both halves were found the hard way. Vixie cron (Debian, RHEL) refuses a
	// crontab that is group- or world-writable and runs it otherwise; BusyBox
	// crond (Alpine) refuses one that is not owned by root — and refuses it
	// *silently*, with no message at any log level, which is how a correct file
	// full of correct entries sits in the spool directory doing nothing.
	//
	// Root ownership satisfies both: Vixie accepts a spool file owned by root
	// or by the user, BusyBox accepts only root. The user column is not what
	// decides who a job runs as in either implementation — the filename is —
	// so nothing is given away by this.
	if err := os.Chmod(tempPath, 0o600); err != nil {
		return fmt.Errorf("set the crontab's mode: %w", err)
	}
	if err := os.Chown(tempPath, 0, 0); err != nil {
		return fmt.Errorf("set the crontab's owner: %w", err)
	}

	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("install the crontab: %w", err)
	}
	return nil
}
