package deploy

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/shared/validate"
)

// The template actions, as argv.
//
// This table is the whole of what the panel can be asked to run, and every
// entry is a fixed command line: nothing from a request appears in any of them.
// A caller names a *kind* from a closed set and this decides what that means.
//
// The flags are not decoration. Each one is here because its absence produces a
// deployment that hangs, prompts, or does something other than deploying:
//
//   - composer's --no-interaction, because a prompt on a terminal nobody is
//     attached to is a build that hangs until its timeout.
//   - composer's --no-dev and npm's --omit=dev, because a deployment installs
//     what the site runs, not what its developers use.
//   - npm ci rather than npm install, because a deployment that resolves
//     versions afresh is one that can deploy code nobody tested.
//   - artisan's --force, because Laravel refuses to migrate in production
//     without it — by prompting, which is the hang again.
var templates = map[string]struct {
	// tool is the allowlist entry this action needs.
	tool string
	// argv is the command line, after the program itself.
	argv []string
	// label is what appears in the log above the output.
	label string
}{
	validate.ActionComposerInstall: {
		tool:  CommandComposer,
		argv:  []string{"install", "--no-interaction", "--no-dev", "--optimize-autoloader", "--prefer-dist"},
		label: "composer install",
	},
	validate.ActionNpmCI: {
		tool:  CommandNpm,
		argv:  []string{"ci", "--omit=dev", "--no-audit", "--no-fund"},
		label: "npm ci",
	},
	validate.ActionNpmInstall: {
		tool:  CommandNpm,
		argv:  []string{"install", "--omit=dev", "--no-audit", "--no-fund"},
		label: "npm install",
	},
	validate.ActionNpmBuild: {
		tool:  CommandNpm,
		argv:  []string{"run", "build"},
		label: "npm run build",
	},
	validate.ActionArtisanMigrate: {
		tool:  CommandPHP,
		argv:  []string{"artisan", "migrate", "--force", "--no-interaction"},
		label: "php artisan migrate",
	},
	validate.ActionArtisanOptimise: {
		tool:  CommandPHP,
		argv:  []string{"artisan", "optimize", "--no-interaction"},
		label: "php artisan optimize",
	},
}

// stepResult is one action's outcome.
type stepResult struct {
	label    string
	exitCode int
	output   string
	timedOut bool
}

// runAction runs one step of a deployment.
func (p *Provider) runAction(ctx context.Context, owner account, req Request,
	kind string,
) (stepResult, error) {
	if kind == validate.ActionScript {
		return p.runScript(ctx, owner, req)
	}

	template, known := templates[kind]
	if !known {
		return stepResult{}, fmt.Errorf("%w: %q", validate.ErrInvalidAction, kind)
	}
	if !p.runner.Available(template.tool) {
		return stepResult{}, fmt.Errorf("%w: %s needs %s, which is not installed on this host",
			ErrActionUnavailable, template.label, template.tool)
	}

	result, err := p.runner.RunWith(ctx, template.tool, command.Options{
		Dir:           req.DocumentRoot,
		Env:           buildEnvironment(owner),
		UID:           owner.UID,
		GID:           owner.GID,
		SetCredential: true,
	}, template.argv...)
	if err != nil {
		return stepResult{}, wrap("run "+template.label, err)
	}

	return stepResult{
		label:    template.label,
		exitCode: result.ExitCode,
		output:   strings.TrimRight(result.Stdout+result.Stderr, "\n"),
		timedOut: result.TimedOut,
	}, nil
}

// buildEnvironment is what a deployment step runs with.
//
// The site's own identity and nothing else. Not the Agent's environment, which
// holds the token the API authenticates to it with, the database password, and
// the encryption key — a build script that printed its environment would
// otherwise print all three into a log the panel then stores.
//
// CI=1 because half the build tools in the world use it to decide not to be
// interactive, and an interactive build tool on a machine with no terminal is a
// deployment that hangs.
func buildEnvironment(owner account) map[string]string {
	return map[string]string{
		"HOME":    owner.Home,
		"USER":    owner.Name,
		"LOGNAME": owner.Name,
		"CI":      "1",
		// composer and npm write caches, and without these they try to write
		// into the Agent's home — which the site account cannot — and report it
		// as an obscure permission failure rather than as a missing setting.
		"COMPOSER_HOME":              owner.Home + "/.composer",
		"COMPOSER_NO_INTERACTION":    "1",
		"NPM_CONFIG_CACHE":           owner.Home + "/.npm",
		"NPM_CONFIG_UPDATE_NOTIFIER": "false",
	}
}

// runScript runs the deployment script.
//
// The one place in this phase where text somebody wrote reaches a shell, and
// the whole of what makes that acceptable is in the package comment. What is
// here is the mechanism, and one detail of it is worth stating twice:
//
// **The script is written to a file and run as `sh <file>`, never as
// `sh -c <text>`.** The process table on a Linux host is world-readable, so a
// script passed on a command line is visible to every account on the machine
// for as long as it runs — and a deployment script is exactly the kind of text
// that contains a token, because that is what people put in them. The file is
// 0600, owned by the site account, and removed afterwards.
func (p *Provider) runScript(ctx context.Context, owner account, req Request) (stepResult, error) {
	script := strings.TrimSpace(req.Script)
	if script == "" {
		// A script step with no script is a step that does nothing, and saying
		// so is better than reporting a successful run of an empty file.
		return stepResult{
			label:  "deployment script",
			output: "no script is configured, so this step did nothing",
		}, nil
	}
	if err := validate.DeployScript(req.Script); err != nil {
		return stepResult{}, err
	}
	if !p.runner.Available(CommandDeployShell) {
		return stepResult{}, fmt.Errorf("%w: this host has no shell to run a deployment script with",
			ErrActionUnavailable)
	}
	if err := p.ensureDirs(); err != nil {
		return stepResult{}, err
	}

	timeout := req.ScriptTimeoutSeconds
	if err := validate.ScriptTimeout(timeout); err != nil {
		return stepResult{}, err
	}

	path := p.paths.ScriptPath(owner.Name)
	// "set -e" so a script whose third line fails stops there rather than
	// running the rest against a half-built tree and reporting success.
	// Prepended by the panel rather than expected of the author: a deployment
	// that silently ignores a failed step is the failure this phase is least
	// able to detect.
	body := "#!/bin/sh\nset -e\n" + script + "\n"
	if err := writeScript(path, body, owner); err != nil {
		return stepResult{}, err
	}
	defer func() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			p.log.Warn("could not remove a deployment script", "path", path, "error", err)
		}
	}()

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	result, err := p.runner.RunWith(ctx, CommandDeployShell, command.Options{
		Dir:           req.DocumentRoot,
		Env:           buildEnvironment(owner),
		UID:           owner.UID,
		GID:           owner.GID,
		SetCredential: true,
	}, path)
	if err != nil {
		return stepResult{}, wrap("run the deployment script", err)
	}

	return stepResult{
		label:    "deployment script",
		exitCode: result.ExitCode,
		output:   strings.TrimRight(result.Stdout+result.Stderr, "\n"),
		timedOut: result.TimedOut,
	}, nil
}

// writeScript writes the script where only its account can read it.
func writeScript(path, body string, owner account) error {
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write the deployment script: %w", err)
	}
	if err := os.Chown(path, owner.UID, owner.GID); err != nil {
		return fmt.Errorf("give the deployment script to %s: %w", owner.Name, err)
	}
	// Set again after the chown: on some systems changing the owner clears the
	// setuid and setgid bits, and re-stating the mode here is cheap insurance
	// that the file is what this function says it is.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure the deployment script: %w", err)
	}
	return nil
}
