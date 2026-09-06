package mail

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CommandFreshclam updates the virus signature database.
const CommandFreshclam = "freshclam"

// AntivirusPreparation reports what it took to make the scanner usable.
type AntivirusPreparation struct {
	// SignaturesFetched reports whether a database was downloaded this run.
	SignaturesFetched bool `json:"signatures_fetched"`
	// SignaturesPresent reports whether the host has a database at all,
	// however it got one. This is the fact that decides whether clamd can
	// start; the fetch above is only how it usually arrives.
	SignaturesPresent bool `json:"signatures_present"`
	// Started reports whether clamd is now running.
	Started bool `json:"started"`
	// Detail explains a preparation that did not finish.
	Detail string `json:"detail,omitempty"`
}

// signatureDirs are where ClamAV keeps its database, by distribution.
var signatureDirs = []string{
	"/var/lib/clamav",
	"/usr/share/clamav",
	"/usr/local/share/clamav",
}

// signatureFiles are the database files worth finding. Any one of them means
// the host has something to scan with.
var signatureFiles = []string{"main.cvd", "main.cld", "daily.cvd", "daily.cld", "bytecode.cvd"}

// HasSignatures reports whether ClamAV has a virus database on this host.
//
// This is the difference between a scanner and a package. clamd refuses to
// start with no database — "No supported database files found" — so a panel
// that installed the package, reported success and left it there would be
// claiming mail was scanned by a daemon that had never run.
func HasSignatures() bool {
	for _, dir := range signatureDirs {
		for _, name := range signatureFiles {
			info, err := os.Stat(filepath.Join(dir, name))
			// Size matters: freshclam writes the file before it has finished
			// filling it, and an interrupted download leaves an empty one that
			// exists and is useless.
			if err == nil && info.Size() > 0 {
				return true
			}
		}
	}
	return false
}

// PrepareAntivirus fetches signatures and starts the scanner.
//
// Installing the package is the easy half and was the only half the panel did.
// A freshly installed ClamAV has no database, and clamd exits immediately
// rather than starting without one — so virus scanning could be turned on, and
// reported as on, while every message went through unscanned. Rspamd's own
// configuration is written to make that visible rather than silent, but the
// panel should not create the situation in the first place.
//
// The download is hundreds of megabytes and takes minutes on a slow link, so
// the caller reports progress. It is skipped where a database is already
// present: freshclam is happy to be run repeatedly, but making somebody wait
// for a download they do not need is its own kind of wrong.
func (p *Provider) PrepareAntivirus(ctx context.Context, report func(int, string)) AntivirusPreparation {
	prep := AntivirusPreparation{}

	progress := func(percent int, message string) {
		if report != nil {
			report(percent, message)
		}
	}

	prep.SignaturesPresent = HasSignatures()

	if !prep.SignaturesPresent {
		// A nil runner is a provider built without command execution — which
		// is a real configuration, not only a test one: the panel constructs
		// one on a host where no allowlisted binary resolved.
		if p.runner == nil || !p.runner.Available(CommandFreshclam) {
			prep.Detail = "no virus database and no freshclam to fetch one, " +
				"so the scanner cannot start"
			return prep
		}

		progress(20, "Downloading virus signatures")
		result, err := p.runner.Run(ctx, CommandFreshclam)
		switch {
		case err != nil:
			prep.Detail = fmt.Sprintf("the signature download did not finish: %v", err)
		case !result.Succeeded() && !HasSignatures():
			// freshclam exits non-zero for things that are not failures — "up
			// to date" among them on some builds — so the filesystem is asked
			// rather than the exit code trusted. What matters is whether there
			// is a database now.
			prep.Detail = "the signature download failed: " + summarise(result.Stdout+result.Stderr)
		default:
			prep.SignaturesFetched = true
		}
		prep.SignaturesPresent = HasSignatures()
	}

	if !prep.SignaturesPresent {
		if prep.Detail == "" {
			prep.Detail = "the host has no virus database, so the scanner cannot start"
		}
		return prep
	}

	if p.services == nil {
		prep.Detail = "signatures are present, but nothing on this host can start the scanner"
		return prep
	}

	progress(80, "Starting the virus scanner")
	// Restart rather than start: on a host where clamd already exited for want
	// of a database, "start" on some init systems reports success against the
	// dead unit it remembers.
	if err := p.services.Restart(ctx, DaemonClamAV); err != nil {
		prep.Detail = fmt.Sprintf("the scanner would not start: %v", err)
		return prep
	}

	prep.Started = p.services.Running(ctx, DaemonClamAV)
	if !prep.Started {
		prep.Detail = "the scanner was started and is not running; its log will say why"
	}
	return prep
}

// summarise trims daemon output to something worth putting in a message.
func summarise(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return "no output"
	}
	lines := strings.Split(trimmed, "\n")
	// The last line is where these tools put the reason; the ones above it are
	// progress.
	last := strings.TrimSpace(lines[len(lines)-1])
	if len(last) > 200 {
		return last[:200]
	}
	return last
}
