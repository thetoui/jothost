package deploy

// The request boundary for everything in this package.
//
// A deployment request carries **what the panel recorded**: a remote that has
// been through validate.GitRemote, a branch through validate.GitBranch, a list
// of typed actions from a closed set, and the website's own account name and
// document root — both of which the website operations produced.
//
// What it cannot carry is a command line. Every action but one becomes an argv
// this package builds; the exception is the deployment script, which is stored
// text rather than sent text, and which is written to a file rather than passed
// on a command line. See the package comment.

// Request is one deployment.
type Request struct {
	// Account is the website's own system account. Everything runs as it, and
	// a resolved uid of 0 is refused rather than repaired.
	Account string `json:"account"`
	// DocumentRoot is the directory the working tree lives in. It is the
	// document root a website operation created, not a path from a request.
	DocumentRoot string `json:"document_root"`

	// Remote and Branch are what to deploy from.
	Remote string `json:"remote"`
	Branch string `json:"branch"`

	// Commit pins the deployment to one revision. Empty means the tip of the
	// branch, which is the ordinary case; a value is how a rollback names
	// where it is going back to.
	Commit string `json:"commit"`

	// Actions are the steps to run after the checkout, in order.
	Actions []Action `json:"actions"`

	// Script is the deployment script, when one of the actions is "script".
	Script string `json:"script"`
	// ScriptTimeoutSeconds bounds the whole run of the script.
	ScriptTimeoutSeconds int `json:"script_timeout_seconds"`

	// UseDeployKey authenticates with the key this host generated for the
	// website, rather than with whatever ambient credentials exist. It is what
	// makes an ssh:// remote work at all.
	UseDeployKey bool `json:"use_deploy_key"`

	// RollbackOnFailure resets the working tree to the commit it was on when a
	// step fails.
	//
	// What that restores is the *source*. It does not undo a build: files a
	// build wrote are not in the repository, so git does not know about them,
	// and deleting everything git does not know about would delete the
	// customer's uploads. The panel says so rather than implying otherwise.
	RollbackOnFailure bool `json:"rollback_on_failure"`
}

// Action is one step of a deployment.
type Action struct {
	// Kind is a member of validate.DeployActions.
	Kind string `json:"kind"`
}

// Result is what a deployment did.
type Result struct {
	// Commit is what the working tree is on now.
	Commit string `json:"commit"`
	// PreviousCommit is what it was on before, which is where a rollback goes.
	PreviousCommit string `json:"previous_commit"`
	// Branch is what was deployed.
	Branch string `json:"branch"`

	// Message and Author describe the commit, so a page can say what was
	// deployed rather than only its hash.
	Message string `json:"message"`
	Author  string `json:"author"`

	// Succeeded reports whether every step exited zero.
	Succeeded bool `json:"succeeded"`
	// ExitCode is the exit status of the step that failed, or 0.
	ExitCode int `json:"exit_code"`
	// FailedStep names the step that failed, so the log does not have to be
	// read to find out which one it was.
	FailedStep string `json:"failed_step,omitempty"`

	// Log is everything the deployment printed, capped.
	Log string `json:"log"`
	// LogTruncated reports that there was more.
	LogTruncated bool `json:"log_truncated"`

	// RolledBack reports whether the working tree was put back.
	RolledBack bool `json:"rolled_back"`
	// RollbackError is what went wrong if it could not be. This is the state
	// an operator most needs to be told about: the site is then on neither
	// commit.
	RollbackError string `json:"rollback_error,omitempty"`

	// DurationMS is how long the whole thing took.
	DurationMS int64 `json:"duration_ms"`
}

// Status is what the panel shows about one website's repository.
type Status struct {
	// Available reports whether this host can deploy at all.
	Available bool `json:"available"`
	// Reason says why not.
	Reason string `json:"reason,omitempty"`
	// GitVersion is what is installed.
	GitVersion string `json:"git_version,omitempty"`

	// Cloned reports whether the working tree exists.
	Cloned bool `json:"cloned"`
	// Commit, Branch, Message and Author describe what is checked out right
	// now — read from the working tree rather than from the panel's record,
	// because the two disagreeing is exactly what this reports.
	Commit  string `json:"commit,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Message string `json:"message,omitempty"`
	Author  string `json:"author,omitempty"`

	// Dirty reports uncommitted changes in the working tree. A deployment
	// would destroy them, so the panel says so before it does.
	Dirty bool `json:"dirty"`
	// DirtyFiles names a few of them, so "dirty" is actionable rather than
	// alarming.
	DirtyFiles []string `json:"dirty_files,omitempty"`

	// Remote is the origin the working tree actually has, which can differ
	// from the one the panel recorded if somebody changed it by hand.
	Remote string `json:"remote,omitempty"`

	// HasKey reports whether a deploy key exists on this host, and Fingerprint
	// identifies it. A repository configured with an ssh remote and no key is
	// one whose deployments will fail at authentication.
	HasKey      bool   `json:"has_key"`
	Fingerprint string `json:"fingerprint,omitempty"`

	// Tools reports which template actions this host can actually run.
	// An action whose tool is missing is reported here rather than discovered
	// at the end of a five-minute deployment.
	Tools map[string]bool `json:"tools"`

	// Warnings are things that are true and will disappoint somebody later.
	Warnings []string `json:"warnings,omitempty"`
}

// Key is a generated deploy key, as the panel records it.
type Key struct {
	// PublicKey is the line to paste into a forge's deploy key box.
	PublicKey string `json:"public_key"`
	// Fingerprint identifies it without carrying it.
	Fingerprint string `json:"fingerprint"`
	// Path is where the private half lives on this host. Reported so an
	// operator can find it; never its contents.
	Path string `json:"path"`
	// Type is the algorithm, so a page can say what was generated rather than
	// what the panel assumes it generates.
	Type string `json:"type"`
}
