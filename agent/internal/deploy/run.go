package deploy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jothost/panel/shared/validate"
)

// nowFunc is the clock, replaceable in tests.
var nowFunc = time.Now

// Deploy puts a commit on the host and runs the steps that make it a site.
//
// The order is the order it has to be in, and each step is where it is because
// of what breaks otherwise:
//
//  1. Resolve the account and refuse root. Everything after this runs as
//     somebody, and getting that wrong is the one failure with no recovery.
//  2. Record what the site is on *now*. A rollback goes back to this, and by
//     the time a build has failed the working tree no longer knows what it was.
//  3. Fetch, then resolve the target. In that order: resolving first would pin
//     the deployment to whatever this host last saw rather than to what the
//     repository says.
//  4. Check out. This is the moment the site changes.
//  5. Run the steps, stopping at the first failure.
//  6. On failure, put the source back — and say plainly what that did and did
//     not restore.
//
// Step 2 before step 3 matters more than it looks: a fetch can fail, and a
// deployment that failed before it changed anything should say so rather than
// offering a rollback to a commit it never left.
func (p *Provider) Deploy(ctx context.Context, req Request,
	progress func(percent int, message string),
) (Result, error) {
	report := func(percent int, message string) {
		if progress != nil {
			progress(percent, message)
		}
	}

	if !p.Available() {
		return Result{}, ErrUnavailable
	}
	if err := checkRequest(req); err != nil {
		return Result{}, err
	}

	started := nowFunc()
	owner, err := p.lookupAccount(req.Account)
	if err != nil {
		return Result{}, err
	}

	var log logBuilder
	result := Result{Branch: req.Branch}

	// What the site is on now. Read before anything changes it.
	result.PreviousCommit = p.gitOutput(ctx, owner, req.DocumentRoot, "rev-parse", "HEAD")

	report(5, "Preparing the repository")
	if err := p.Clone(ctx, req, func(message string) { log.section(message) }); err != nil {
		result.Log, result.LogTruncated = log.finish()
		result.DurationMS = nowFunc().Sub(started).Milliseconds()
		return result, err
	}

	report(20, "Working out what to deploy")
	target, err := p.resolve(ctx, owner, req)
	if err != nil {
		result.Log, result.LogTruncated = log.finish()
		result.DurationMS = nowFunc().Sub(started).Milliseconds()
		return result, err
	}

	if result.PreviousCommit == target && p.isClean(ctx, owner, req.DocumentRoot) &&
		len(req.Actions) == 0 {
		// Nothing to do, and saying so is better than reporting a deployment
		// that did nothing as a deployment that did something.
		log.section("Already on " + short(target) + ", and there are no steps to run")
		result.Commit = target
		result.Succeeded = true
		result.Message, result.Author = p.describe(ctx, owner, req.DocumentRoot, target)
		result.Log, result.LogTruncated = log.finish()
		result.DurationMS = nowFunc().Sub(started).Milliseconds()
		return result, nil
	}

	report(30, "Checking out "+short(target))
	log.section("Checking out " + short(target))
	commit, err := p.Checkout(ctx, req, target)
	if err != nil {
		result.Log, result.LogTruncated = log.finish()
		result.DurationMS = nowFunc().Sub(started).Milliseconds()
		return result, err
	}
	result.Commit = commit
	result.Message, result.Author = p.describe(ctx, owner, req.DocumentRoot, commit)
	log.line(commit + " " + result.Message)

	// The steps.
	total := len(req.Actions)
	for index, action := range req.Actions {
		percent := 35 + (index*55)/max(total, 1)
		report(percent, actionLabel(action.Kind))
		log.section(actionLabel(action.Kind))

		step, err := p.runAction(ctx, owner, req, action.Kind)
		if err != nil {
			// The step could not be *started* — a missing tool, an invalid
			// action. That is the panel's fault rather than the build's, and it
			// is reported as an error rather than as a failed build.
			log.line(err.Error())
			result.FailedStep = actionLabel(action.Kind)
			// The rollback writes into the log, so the log is finished after
			// it rather than before: the other order silently dropped the one
			// part of the record an operator most needs to read.
			p.rollback(ctx, owner, req, &result, &log)
			result.Log, result.LogTruncated = log.finish()
			result.DurationMS = nowFunc().Sub(started).Milliseconds()
			return result, err
		}

		log.line(step.output)
		if step.timedOut {
			log.line(fmt.Sprintf("[the step was stopped after %d seconds]",
				req.ScriptTimeoutSeconds))
		}
		if step.exitCode != 0 {
			result.ExitCode = step.exitCode
			result.FailedStep = step.label
			log.line(fmt.Sprintf("[%s exited %d]", step.label, step.exitCode))
			p.rollback(ctx, owner, req, &result, &log)
			result.Log, result.LogTruncated = log.finish()
			result.DurationMS = nowFunc().Sub(started).Milliseconds()
			return result, fmt.Errorf("%w: %s", ErrActionFailed, step.label)
		}
	}

	report(100, "Deployed "+short(commit))
	result.Succeeded = true
	result.Log, result.LogTruncated = log.finish()
	result.DurationMS = nowFunc().Sub(started).Milliseconds()
	return result, nil
}

// rollback puts the source back after a failed deployment.
//
// What it restores is the working tree's *tracked* files. It does not undo a
// build, and it deliberately does not try: the files a build wrote are not in
// the repository, so the only way to remove them would be to delete everything
// git does not know about — which on a real site is the customer's uploads,
// their storage directory, and their .env.
//
// So the promise is narrow and it is written into the log in words, because
// "rolled back" on a page reads as "nothing happened" and that is not true.
//
// A rollback that itself fails is recorded rather than swallowed. It is the
// state an operator most needs to be told about: the site is then on neither
// commit, and nothing else in the panel would say so.
func (p *Provider) rollback(ctx context.Context, owner account, req Request,
	result *Result, log *logBuilder,
) {
	if !req.RollbackOnFailure {
		return
	}
	if result.PreviousCommit == "" {
		log.section("Not rolling back: this is the first deployment, so there is " +
			"nothing to go back to")
		return
	}
	if result.PreviousCommit == result.Commit {
		log.section("Not rolling back: the working tree is already on " +
			short(result.PreviousCommit))
		return
	}

	log.section("Rolling back to " + short(result.PreviousCommit))
	if _, err := p.Checkout(ctx, req, result.PreviousCommit); err != nil {
		result.RollbackError = err.Error()
		log.line("The rollback failed: " + err.Error())
		log.line("This site is now on neither commit. Its working tree needs looking at.")
		return
	}

	result.RolledBack = true
	result.Commit = result.PreviousCommit
	log.line("The source is back on " + short(result.PreviousCommit) + ".")
	log.line("Files the build wrote are still there: they are not in the repository, " +
		"so restoring the source cannot remove them.")
}

// checkRequest refuses a deployment that cannot work.
//
// Validated here as well as at the API boundary, because this is the process
// that runs the commands: a check that lives only in the caller is one a future
// caller can skip.
func checkRequest(req Request) error {
	if err := validate.SystemUser(req.Account); err != nil {
		return err
	}
	if req.DocumentRoot == "" || !strings.HasPrefix(req.DocumentRoot, "/") {
		return fmt.Errorf("%w: a deployment needs the website's document root", ErrNotCloned)
	}
	if err := validate.GitRemote(req.Remote); err != nil {
		return err
	}
	if err := validate.GitBranch(req.Branch); err != nil {
		return err
	}
	if req.Commit != "" {
		if err := validate.CommitSHA(req.Commit); err != nil {
			return err
		}
	}
	for _, action := range req.Actions {
		if err := validate.DeployAction(action.Kind); err != nil {
			return err
		}
	}
	if req.Script != "" {
		if err := validate.DeployScript(req.Script); err != nil {
			return err
		}
	}
	return nil
}

// actionLabel names a step for the log.
func actionLabel(kind string) string {
	if template, known := templates[kind]; known {
		return template.label
	}
	if kind == validate.ActionScript {
		return "deployment script"
	}
	return kind
}

// short abbreviates a commit for a human.
func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// logBuilder accumulates a deployment's output under a cap.
//
// Capped rather than unbounded, and capped at the *end* rather than the start:
// a build that prints a hundred megabytes is a build with a loop in it, and the
// part worth keeping is what it said just before it stopped.
type logBuilder struct {
	out       strings.Builder
	truncated bool
}

func (l *logBuilder) section(title string) {
	if l.out.Len() > 0 {
		l.out.WriteString("\n")
	}
	l.out.WriteString("=== " + title + "\n")
}

func (l *logBuilder) line(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	l.out.WriteString(text)
	l.out.WriteString("\n")
}

func (l *logBuilder) finish() (string, bool) {
	text := l.out.String()
	if len(text) <= MaxLogBytes {
		return text, l.truncated
	}
	return "[earlier output was dropped]\n" + text[len(text)-MaxLogBytes:], true
}
