// Package deploy puts a website's source on the host from a git repository and
// runs the steps that turn it into a working site.
//
// # What runs, as whom, and why that is allowed
//
// A deployment executes code: `composer install` runs a package's install
// scripts, `npm run build` runs whatever the project's build script says, and
// the optional deployment script is a shell script somebody wrote. CLAUDE.md
// section 4 says never to execute arbitrary user-provided shell commands, so
// the reasoning is written here rather than left implicit.
//
// It is the same reasoning agent/internal/cron/run.go sets out, and it holds
// more strongly here. A caller who can configure a deployment can already
// schedule a cron job, which is a command line run by crond as this same
// account on any schedule they choose; and the website's own application code
// already runs as that account, on every request, through PHP-FPM. A deployment
// step therefore grants no authority that was not already granted — it changes
// when the code runs, not who it runs as or what it can reach.
//
// What is not allowed, and is not offered anywhere in this phase, is a *panel*
// endpoint that takes a string and runs it. Every step but one is a typed
// action from a closed set that this package turns into an argv; the exception
// is the deployment script, and it is bounded as follows:
//
//   - It never runs as root. The credential drop is mandatory and a resolved
//     uid of 0 is refused rather than repaired.
//   - It runs what the panel stored, not what a request sent.
//   - It runs in the website's own document root, as the website's own account.
//   - It is timeout-protected and its output is captured and capped.
//   - It is **written to a file and executed as `sh <file>`**, never as
//     `sh -c <text>`. That is a real difference rather than a stylistic one:
//     the process table on a Linux host is world-readable, so a script passed
//     on a command line is visible to every account on the machine for as long
//     as it runs — and a deployment script is exactly the kind of text that
//     contains a token.
//   - The file is 0600 and owned by the site account, and it is removed
//     afterwards.
//
// # Where the deploy key lives
//
// On the host, and nowhere else — the same decision Phase 26 made about DKIM
// keys, for a sharper reason: this key grants read access to a customer's
// source code, and the control-plane database is backed up, replicated and read
// by every part of the API. The panel records the public half and the
// fingerprint, which is what somebody needs in order to add it to a forge.
//
// # Why the checkout is in place rather than a release directory
//
// The familiar arrangement is a directory per release with a symlink swapped at
// the end, which makes a deployment atomic. This panel does not do that, and
// the reason is what a document root actually contains: a WordPress site has
// wp-content/uploads in it, a Laravel site has storage/, and neither is in the
// repository. Swapping directories would deploy the code and lose the
// customer's files.
//
// So the working tree is updated in place, and the honesty is in what rollback
// promises: it restores the *source*, by resetting to the commit the site was
// on before, and it does not undo a build. That is stated on the page and in
// docs/PHASE27.md rather than implied by the word "rollback".
package deploy

import (
	"errors"
	"path/filepath"
)

// Allowlist keys for the programs this package drives.
//
// Every one is a fixed absolute path resolved at Agent startup, and none is
// ever handed a value from a request that has not been validated. The
// repository URL is the one that matters most: git's remote is a small
// language, three of whose dialects run programs, and validate.GitRemote is
// what stops one reaching this list.
// Every name is prefixed, and that is not decoration.
//
// npm, php and composer are already allowlisted by the Node.js and PHP phases,
// with their own timeouts and their own environment rules — the Agent refuses
// to start with two specs of one name, which is how this was found. Keeping
// these separate is also the right answer on its own terms: changing how long a
// deployment's build may run must not change how a customer's Node.js
// application is started.
const (
	// CommandGit is git itself.
	CommandGit = "deploy-git"
	// CommandSSHKeygen generates the deploy key and reads its fingerprint.
	CommandSSHKeygen = "deploy-ssh-keygen"
	// CommandDeployShell is the shell a deployment script runs under.
	//
	// A dedicated allowlist entry rather than a shared "sh", for the reason
	// cron has its own: it makes the set of places in this Agent that can
	// reach a shell a list somebody can read, rather than a property of
	// whoever imports what.
	CommandDeployShell = "deploy-shell"
	// CommandComposer, CommandNpm and CommandPHP are the build tools the
	// template actions drive. Each may be absent, which is reported as the
	// action being unavailable rather than as a deployment that failed
	// mysteriously.
	CommandComposer = "deploy-composer"
	CommandNpm      = "deploy-npm"
	CommandPHP      = "deploy-php"
)

// Where things live.
const (
	// DefaultStateDir holds what the panel keeps per repository that is not in
	// the website's own directory: the deploy key and the SSH configuration
	// git authenticates with.
	DefaultStateDir = "/var/lib/jothost/deploy"
	// keysDir holds the deploy keys, one pair per website.
	keysDir = "keys"
	// scriptName is the deployment script, written into the state directory
	// for the length of one run.
	scriptsDir = "scripts"
)

// Limits.
const (
	// MaxLogBytes bounds what one deployment's output can grow to.
	//
	// A build that prints a hundred megabytes is a build with a loop in it, and
	// keeping all of it would put the panel's own database at risk to record a
	// failure that the first few thousand lines already explain.
	MaxLogBytes = 256 << 10
	// cloneTimeout bounds the initial clone, which is the slowest thing here.
	cloneTimeout = 10 * 60
	// fetchTimeout bounds a fetch.
	fetchTimeout = 5 * 60
)

// Errors returned by this package.
var (
	// ErrUnavailable means this host has no git.
	ErrUnavailable = errors.New("this host has no git, so nothing can be deployed")
	// ErrNoAccount means the website has no system account to run as.
	ErrNoAccount = errors.New("this website has no system account to deploy as")
	// ErrRootRefused means the account resolved to uid 0.
	ErrRootRefused = errors.New("a deployment may not run as root")
	// ErrNotCloned means the repository has not been set up on this host yet.
	ErrNotCloned = errors.New("this website has no repository on the host yet")
	// ErrRemoteFailed means git could not reach the repository.
	ErrRemoteFailed = errors.New("the repository could not be reached")
	// ErrActionFailed means a deployment step exited non-zero.
	ErrActionFailed = errors.New("a deployment step failed")
	// ErrActionUnavailable means a step needs a tool this host does not have.
	ErrActionUnavailable = errors.New("this host cannot run that deployment step")
	// ErrDirty means the working tree has changes a deployment would destroy.
	ErrDirty = errors.New("the working tree has uncommitted changes")
)

// Paths tells the provider where to keep what it generates.
type Paths struct {
	// StateDir holds the deploy keys and the SSH configuration.
	StateDir string
}

// DefaultPaths returns the standard layout.
func DefaultPaths() Paths { return Paths{StateDir: DefaultStateDir} }

func (p Paths) withDefaults() Paths {
	if p.StateDir == "" {
		p.StateDir = DefaultStateDir
	}
	return p
}

// KeyDir is where the deploy keys live.
func (p Paths) KeyDir() string { return filepath.Join(p.StateDir, keysDir) }

// ScriptDir is where a deployment script is written for the length of a run.
func (p Paths) ScriptDir() string { return filepath.Join(p.StateDir, scriptsDir) }

// KeyPath is one website's deploy key.
//
// Named by the website's system account, which has been through
// validate.SystemUser and so contains no separator. That is the check that
// makes this safe; this function depends on it rather than repeating it, and
// every caller is inside this package.
func (p Paths) KeyPath(account string) string {
	return filepath.Join(p.KeyDir(), account+".key")
}

// SSHConfigPath is the SSH configuration git uses for one website.
//
// One per website rather than one shared file, because the whole point of a
// deploy key is that it is one repository's: a shared configuration would offer
// every key to every host, and a forge that accepts the first key it is offered
// would authenticate one customer's deployment as another's.
func (p Paths) SSHConfigPath(account string) string {
	return filepath.Join(p.KeyDir(), account+".ssh-config")
}

// ScriptPath is where one deployment's script is written.
func (p Paths) ScriptPath(account string) string {
	return filepath.Join(p.ScriptDir(), account+".sh")
}
