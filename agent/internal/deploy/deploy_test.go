package deploy

import (
	"os"
	"strings"
	"testing"

	"github.com/jothost/panel/shared/validate"
)

func request() Request {
	return Request{
		Account:              "web_example",
		DocumentRoot:         "/var/www/example.com/public",
		Remote:               "git@github.com:owner/repo.git",
		Branch:               "main",
		ScriptTimeoutSeconds: 600,
		Actions:              []Action{{Kind: validate.ActionComposerInstall}},
	}
}

// ---------------------------------------------------------------- templates

func TestEveryTemplateIsAFixedCommandLine(t *testing.T) {
	// The point of the template table: a caller names a *kind* from a closed
	// set and this decides what that means. Nothing from a request appears in
	// any of these, which is what makes them exempt from the argument this
	// package has to make about the deployment script.
	for _, kind := range validate.DeployActions {
		if kind == validate.ActionScript {
			continue
		}
		template, known := templates[kind]
		if !known {
			t.Errorf("%s is in the validated set and has no template", kind)
			continue
		}
		if len(template.argv) == 0 || template.tool == "" || template.label == "" {
			t.Errorf("%s has an incomplete template: %+v", kind, template)
		}
	}
}

func TestTheTemplatesCannotHangWaitingForAnAnswer(t *testing.T) {
	// A prompt on a terminal nobody is attached to is a build that hangs until
	// its timeout, and every one of these tools prompts by default in at least
	// one situation.
	for kind, wanted := range map[string]string{
		validate.ActionComposerInstall: "--no-interaction",
		validate.ActionArtisanMigrate:  "--force",
		validate.ActionArtisanOptimise: "--no-interaction",
	} {
		argv := strings.Join(templates[kind].argv, " ")
		if !strings.Contains(argv, wanted) {
			t.Errorf("%s is missing %s, so it can hang: %s", kind, wanted, argv)
		}
	}
}

func TestADeploymentInstallsWhatTheSiteRunsRatherThanWhatItsDevelopersUse(t *testing.T) {
	if !strings.Contains(strings.Join(templates[validate.ActionComposerInstall].argv, " "), "--no-dev") {
		t.Error("composer install would install development dependencies on a live site")
	}
	if !strings.Contains(strings.Join(templates[validate.ActionNpmCI].argv, " "), "--omit=dev") {
		t.Error("npm ci would install development dependencies on a live site")
	}
}

func TestTheDefaultNodeInstallIsTheOneThatUsesTheLockfile(t *testing.T) {
	// "npm ci" installs exactly what the lockfile says; "npm install" resolves
	// versions afresh, which is how a deployment ships a dependency nobody
	// tested. Both are offered, and the lockfile one is listed first.
	ci := -1
	install := -1
	for index, action := range validate.DeployActions {
		switch action {
		case validate.ActionNpmCI:
			ci = index
		case validate.ActionNpmInstall:
			install = index
		}
	}
	if ci < 0 || install < 0 || ci > install {
		t.Errorf("npm ci should be offered before npm install: ci=%d install=%d", ci, install)
	}
}

// -------------------------------------------------------------- environment

func TestABuildNeverSeesTheAgentsOwnEnvironment(t *testing.T) {
	// The Agent's environment holds the token the API authenticates to it
	// with, the database password and the encryption key. A build script that
	// printed its environment would otherwise print all three into a log the
	// panel then stores and shows.
	environment := buildEnvironment(account{Name: "web_example", Home: "/home/web_example"})

	for _, forbidden := range []string{
		"AGENT_TOKEN", "DATABASE_URL", "ENCRYPTION_KEY", "PATH", "AGENT_SOCKET",
	} {
		if _, present := environment[forbidden]; present {
			t.Errorf("a deployment step would be handed %s", forbidden)
		}
	}
	if environment["HOME"] != "/home/web_example" {
		t.Errorf("HOME is not the site's own: %q", environment["HOME"])
	}
	if environment["CI"] != "1" {
		t.Error("CI is not set, so half the build tools in the world will try to be interactive")
	}
}

func TestTheBuildToolsAreToldWhereToWriteTheirCaches(t *testing.T) {
	// Without these, composer and npm try to write into the Agent's home —
	// which the site account cannot — and report it as an obscure permission
	// failure rather than as a missing setting.
	environment := buildEnvironment(account{Name: "web_example", Home: "/home/web_example"})
	if !strings.HasPrefix(environment["COMPOSER_HOME"], "/home/web_example") {
		t.Errorf("COMPOSER_HOME: %q", environment["COMPOSER_HOME"])
	}
	if !strings.HasPrefix(environment["NPM_CONFIG_CACHE"], "/home/web_example") {
		t.Errorf("NPM_CONFIG_CACHE: %q", environment["NPM_CONFIG_CACHE"])
	}
}

// ------------------------------------------------------------------- checks

func TestADeploymentRefusesARemoteThatWouldRunAProgram(t *testing.T) {
	// Checked here as well as at the API boundary, because this is the process
	// that runs the commands: a check that lives only in the caller is one a
	// future caller can skip.
	req := request()
	req.Remote = "ext::sh -c whoami"
	if err := checkRequest(req); err == nil {
		t.Error("a remote naming a program to run was accepted")
	}
}

func TestADeploymentNeedsAnAbsoluteDocumentRoot(t *testing.T) {
	req := request()
	req.DocumentRoot = "public"
	if err := checkRequest(req); err == nil {
		t.Error("a relative document root was accepted")
	}
}

func TestAnUnknownActionIsRefusedRatherThanSkipped(t *testing.T) {
	// A step that silently did nothing would be a deployment reporting success
	// for a build it never ran.
	req := request()
	req.Actions = []Action{{Kind: "rm.everything"}}
	if err := checkRequest(req); err == nil {
		t.Error("an action outside the closed set was accepted")
	}
}

func TestAValidDeploymentPasses(t *testing.T) {
	if err := checkRequest(request()); err != nil {
		t.Errorf("an ordinary deployment was refused: %v", err)
	}
}

// -------------------------------------------------------------- ssh command

func TestTheDeployKeyIsTheOnlyIdentityOfferedAndTheHostKeyIsPinned(t *testing.T) {
	provider := &Provider{paths: DefaultPaths()}
	command := provider.sshCommand("web_example")

	// IdentitiesOnly, or ssh offers every key the agent holds — which on a
	// host running several sites means one customer's deployment can
	// authenticate as another.
	if !strings.Contains(command, "IdentitiesOnly=yes") {
		t.Error("ssh would offer identities other than this website's deploy key")
	}
	// accept-new pins on first use and refuses a *change*, which is the
	// property that matters: a changed host key is what an interception looks
	// like. "no" would accept a different key every time.
	if !strings.Contains(command, "StrictHostKeyChecking=accept-new") {
		t.Errorf("host key checking is not what this package documents: %s", command)
	}
	if strings.Contains(command, "StrictHostKeyChecking=no") {
		t.Error("host key checking is off, which makes it meaningless")
	}
	// A per-website known_hosts, so one customer's pinning cannot decide what
	// another customer's deployment trusts.
	if !strings.Contains(command, "web_example.key.known_hosts") {
		t.Errorf("the known hosts file is shared between websites: %s", command)
	}
	// -F /dev/null, or ssh reads root's own config and a "Host *" entry there
	// decides how a customer's deployment authenticates.
	if !strings.Contains(command, "-F /dev/null") {
		t.Error("ssh would read the Agent's own configuration")
	}
	if !strings.Contains(command, "BatchMode=yes") {
		t.Error("ssh could prompt, which on a machine with no terminal is a hang")
	}
}

func TestOneWebsiteCannotReachAnothersKey(t *testing.T) {
	// The account name has been through validate.SystemUser, which permits
	// only [a-z0-9_-] — so it cannot contain a separator. This is the check
	// that the path is built from it rather than around it.
	paths := DefaultPaths()
	if paths.KeyPath("web_a") == paths.KeyPath("web_b") {
		t.Fatal("two websites share a deploy key path")
	}
	if !strings.HasPrefix(paths.KeyPath("web_a"), paths.KeyDir()) {
		t.Errorf("a key path escaped the key directory: %s", paths.KeyPath("web_a"))
	}
}

// ----------------------------------------------------------------- the log

func TestTheLogKeepsTheEndRatherThanTheBeginning(t *testing.T) {
	// A build that prints a hundred megabytes is a build with a loop in it,
	// and the part worth keeping is what it said just before it stopped.
	var log logBuilder
	log.section("npm run build")
	log.line(strings.Repeat("noise\n", MaxLogBytes/4))
	log.line("Error: the thing that actually went wrong")

	text, truncated := log.finish()
	if !truncated {
		t.Fatal("an oversized log was not reported as truncated")
	}
	if len(text) > MaxLogBytes+64 {
		t.Errorf("the log is %d bytes, which is past the cap", len(text))
	}
	if !strings.Contains(text, "the thing that actually went wrong") {
		t.Error("the end of the log was dropped, which is the part that says why")
	}
	if !strings.Contains(text, "earlier output was dropped") {
		t.Error("the log does not say that it is incomplete")
	}
}

func TestAnEmptyLineIsNotRecorded(t *testing.T) {
	var log logBuilder
	log.line("   ")
	log.line("")
	if text, _ := log.finish(); text != "" {
		t.Errorf("blank output became log lines: %q", text)
	}
}

// --------------------------------------------------------------- rollback

func TestRollbackIsSkippedWhenThereIsNowhereToGoBackTo(t *testing.T) {
	// The first deployment of a site has no previous commit, and reporting a
	// rollback that did not happen would be worse than reporting none.
	provider := &Provider{paths: DefaultPaths()}
	req := request()
	req.RollbackOnFailure = true

	result := Result{PreviousCommit: ""}
	var log logBuilder
	provider.rollback(nil, account{}, req, &result, &log)

	if result.RolledBack {
		t.Error("a rollback was reported with no commit to go back to")
	}
	text, _ := log.finish()
	if !strings.Contains(text, "nothing to go back to") {
		t.Errorf("the log does not say why nothing happened:\n%s", text)
	}
}

func TestRollbackIsSkippedWhenTheTreeNeverMoved(t *testing.T) {
	provider := &Provider{paths: DefaultPaths()}
	req := request()
	req.RollbackOnFailure = true

	result := Result{PreviousCommit: "abc1234", Commit: "abc1234"}
	var log logBuilder
	provider.rollback(nil, account{}, req, &result, &log)

	if result.RolledBack {
		t.Error("a rollback was reported for a tree that never moved")
	}
}

func TestRollbackIsNotAttemptedWhenItWasNotAskedFor(t *testing.T) {
	provider := &Provider{paths: DefaultPaths()}
	req := request()
	req.RollbackOnFailure = false

	result := Result{PreviousCommit: "abc1234", Commit: "def5678"}
	var log logBuilder
	provider.rollback(nil, account{}, req, &result, &log)

	if result.RolledBack {
		t.Error("a rollback happened without being asked for")
	}
	if text, _ := log.finish(); text != "" {
		t.Errorf("a rollback that was not asked for wrote to the log: %q", text)
	}
}

// ------------------------------------------------------------------ labels

func TestEveryActionHasSomethingToCallItInTheLog(t *testing.T) {
	for _, kind := range validate.DeployActions {
		if actionLabel(kind) == "" {
			t.Errorf("%s has no label", kind)
		}
	}
}

func TestACommitIsAbbreviatedForAHuman(t *testing.T) {
	if short("5f2e8a19c4b7d0e3f6a2b9c8d1e4f7a0b3c6d9e2") != "5f2e8a19" {
		t.Errorf("short: %q", short("5f2e8a19c4b7d0e3f6a2b9c8d1e4f7a0b3c6d9e2"))
	}
	if short("abc") != "abc" {
		t.Error("a short value was truncated further")
	}
}

func TestTheKeyDirectoryCanBeTraversedAndNotListed(t *testing.T) {
	// The unusual mode, and the deliberate one. git reads the deploy key as the
	// website's own account, so a 0700 root-owned directory makes it
	// unreachable — and that does not surface as a permission error on the
	// file: ssh reports "Identity file not accessible" and the deployment fails
	// at authentication, looking exactly like a key the forge never received.
	//
	// Found by watching a real fetch fail.
	provider := &Provider{paths: Paths{StateDir: t.TempDir()}.withDefaults()}
	provider.paths.StateDir = t.TempDir()

	if err := provider.ensureDirs(); err != nil {
		t.Fatalf("ensureDirs: %v", err)
	}
	for _, dir := range []string{
		provider.paths.StateDir, provider.paths.KeyDir(), provider.paths.ScriptDir(),
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		mode := info.Mode().Perm()
		if mode&0o001 == 0 {
			t.Errorf("%s is mode %o: an unprivileged account cannot reach the files in it", dir, mode)
		}
		if mode&0o004 != 0 {
			t.Errorf("%s is mode %o: one website could list another's keys", dir, mode)
		}
	}
}
