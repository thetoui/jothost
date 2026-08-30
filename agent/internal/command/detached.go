package command

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// DetachedOptions describe a long-running program the Agent starts but does
// not wait for.
type DetachedOptions struct {
	// Dir is the working directory. It must be absolute.
	Dir string
	// Env adds variables to the sanitised base environment. Every name must
	// appear in the spec's AllowedEnv, exactly as for Run.
	Env map[string]string
	// Stdout and Stderr are appended to. Both are opened by this package, so a
	// caller cannot hand the child an arbitrary descriptor.
	Stdout string
	Stderr string
	// User and Group are the account the program runs as. Empty means the
	// Agent's own, which for a daemon running as root is almost never what a
	// caller wants — every caller in this Agent supplies one.
	UID int
	GID int
	// SetCredential applies UID and GID. A separate flag because 0 is a valid
	// uid, and root is exactly the value that must never be applied by
	// accident.
	SetCredential bool
}

// StartDetached runs an allowlisted program in the background and returns its
// process id.
//
// It is the same boundary as Run — an allowlisted binary at a pinned absolute
// path, an argument vector, no shell, a sanitised environment — with the one
// difference that the process outlives the call.
//
// The child is given its own session, so it survives the Agent restarting and
// is not killed by a signal sent to the Agent's process group. That is the
// point: an application the panel started should keep serving while the panel
// is upgraded. The cost is that the Agent cannot wait() on it, which is why
// liveness is checked by reading the process table rather than by waiting.
func (r *Runner) StartDetached(ctx context.Context, name string, opts DetachedOptions,
	args ...string,
) (int, error) {
	spec, ok := r.specs[name]
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrNotAllowed, name)
	}
	if err := validateArgs(args); err != nil {
		return 0, err
	}
	env, err := r.environment(spec, opts.Env)
	if err != nil {
		return 0, err
	}
	if !isExecutable(spec.Path) {
		return 0, fmt.Errorf("%w: %s", ErrUnavailable, spec.Name)
	}
	if opts.Dir == "" || !strings.HasPrefix(opts.Dir, "/") {
		return 0, fmt.Errorf("%w: a working directory must be absolute", ErrInvalidArg)
	}

	stdout, err := openLog(opts.Stdout, opts.UID, opts.GID, opts.SetCredential)
	if err != nil {
		return 0, err
	}
	defer func() { _ = stdout.Close() }()

	stderr := stdout
	if opts.Stderr != "" && opts.Stderr != opts.Stdout {
		stderr, err = openLog(opts.Stderr, opts.UID, opts.GID, opts.SetCredential)
		if err != nil {
			return 0, err
		}
		defer func() { _ = stderr.Close() }()
	}

	// Deliberately not CommandContext: the context bounds starting the
	// process, not its lifetime. A background application must not be killed
	// because the request that started it returned.
	cmd := exec.Command(spec.Path, args...)
	cmd.Env = env
	cmd.Dir = opts.Dir
	cmd.Stdin = nil
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	configureDetached(cmd, opts)

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start %s: %w", spec.Name, err)
	}

	pid := cmd.Process.Pid

	// The child is in its own session, so it is reparented to init when this
	// goroutine releases it. Release rather than Wait: waiting would block for
	// the application's whole lifetime, and the Agent has no use for its exit
	// status — liveness is read from the process table.
	if err := cmd.Process.Release(); err != nil {
		return pid, fmt.Errorf("release %s: %w", spec.Name, err)
	}
	return pid, nil
}

// openLog opens an application's log file for appending.
//
// The file is opened here rather than by the caller so a caller cannot pass a
// descriptor to something else. It is owned by the account the application
// runs as, because the application is what writes to it.
func openLog(path string, uid, gid int, own bool) (*os.File, error) {
	if path == "" {
		return os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("%w: a log path must be absolute", ErrInvalidArg)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open log %s: %w", path, err)
	}
	if own {
		if err := file.Chown(uid, gid); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("own log %s: %w", path, err)
		}
	}
	return file, nil
}
