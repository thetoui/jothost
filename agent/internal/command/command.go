// Package command runs external programs under the constraints in CLAUDE.md
// section 6: explicit, allowlisted, parameterised, validated, audited, and
// timeout-protected.
//
// The rules this package exists to enforce:
//
//   - No shell, ever. Programs are executed directly with an argv slice, so
//     shell metacharacters in an argument are inert data rather than syntax.
//     There is no code path here that reaches "sh -c".
//   - Only allowlisted binaries, named by absolute path. PATH is never
//     searched, so a hostile PATH entry cannot substitute a program.
//   - Every execution is bounded: a timeout, an output cap, and a sanitised
//     environment.
package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Errors returned by the runner.
var (
	ErrNotAllowed     = errors.New("command is not allowlisted")
	ErrNotAbsolute    = errors.New("command path must be absolute")
	ErrInvalidArg     = errors.New("argument is not permitted")
	ErrTimeout        = errors.New("command timed out")
	ErrOutputTooLarge = errors.New("command produced too much output")
	ErrUnavailable    = errors.New("command is not installed")
)

// Defaults for a single execution.
const (
	DefaultTimeout   = 30 * time.Second
	DefaultMaxOutput = 1 << 20 // 1 MiB
	// waitDelay bounds how long Wait lingers after cancellation before
	// abandoning the child's output pipes.
	waitDelay = 2 * time.Second
)

// maxArgs bounds the argv length so a caller cannot build an unbounded
// command line.
const maxArgs = 64

// maxArgLength bounds one argument.
const maxArgLength = 4096

// Result is the outcome of one execution.
type Result struct {
	// ExitCode is the process exit status. It is 0 on success.
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
	// TimedOut reports whether the deadline fired before the process exited.
	TimedOut bool
}

// Succeeded reports whether the command exited zero.
func (r Result) Succeeded() bool { return r.ExitCode == 0 && !r.TimedOut }

// Spec describes one allowlisted program.
type Spec struct {
	// Name is the identifier callers use, e.g. "systemctl".
	Name string
	// Path is the absolute location of the binary. PATH is never consulted.
	Path string
	// Timeout bounds a single execution. Zero means DefaultTimeout.
	Timeout time.Duration
	// MaxOutput caps combined stdout and stderr. Zero means DefaultMaxOutput.
	MaxOutput int
	// AllowedEnv names the environment variables a caller may set for this
	// program, on top of the sanitised base environment.
	//
	// It is an allowlist rather than a free-form map because the environment
	// is an execution channel: LD_PRELOAD, PATH, or IFS supplied by a caller
	// would change what runs, not merely how. A spec that names nothing here
	// — which is every spec but the database clients — cannot have its
	// environment influenced at all.
	AllowedEnv []string
	// AllowAnyEnv lifts the allowlist for a program whose environment is the
	// caller's data rather than the Agent's configuration.
	//
	// Exactly one spec sets it: the Node.js runtime, where an application's
	// environment variables are unbounded by nature — a database URL, an API
	// key, a feature flag — and no fixed list could ever contain them.
	//
	// The boundary does not disappear, it moves. shared/validate.EnvKey is
	// what refuses the names that change what runs rather than how it behaves
	// (LD_PRELOAD, PATH, NODE_OPTIONS and the rest), and every variable
	// reaching this path has been through it.
	AllowAnyEnv bool
}

// Runner executes allowlisted commands.
type Runner struct {
	specs map[string]Spec
	// env is the sanitised environment handed to every child. It deliberately
	// omits the parent's environment, which on a root daemon may hold
	// credentials, and pins PATH so nothing resolves a program by search.
	env []string
}

// NewRunner builds a Runner over the given allowlist.
func NewRunner(specs ...Spec) (*Runner, error) {
	if len(specs) == 0 {
		return nil, errors.New("at least one command spec is required")
	}

	byName := make(map[string]Spec, len(specs))
	for _, spec := range specs {
		if spec.Name == "" {
			return nil, errors.New("command spec requires a name")
		}
		if !filepath.IsAbs(spec.Path) {
			return nil, fmt.Errorf("command %q: %w", spec.Name, ErrNotAbsolute)
		}
		if _, exists := byName[spec.Name]; exists {
			return nil, fmt.Errorf("duplicate command spec %q", spec.Name)
		}
		if spec.Timeout <= 0 {
			spec.Timeout = DefaultTimeout
		}
		if spec.MaxOutput <= 0 {
			spec.MaxOutput = DefaultMaxOutput
		}
		byName[spec.Name] = spec
	}

	return &Runner{
		specs: byName,
		env: []string{
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"LC_ALL=C", // stable, parseable output regardless of host locale
			"LANG=C",
		},
	}, nil
}

// Available reports whether an allowlisted command exists on this host.
//
// Callers use it to degrade explicitly — reporting that systemd is absent —
// rather than surfacing an execution failure as an internal error.
func (r *Runner) Available(name string) bool {
	spec, ok := r.specs[name]
	if !ok {
		return false
	}
	return isExecutable(spec.Path)
}

// Names returns the allowlisted command names.
func (r *Runner) Names() []string {
	names := make([]string, 0, len(r.specs))
	for name := range r.specs {
		names = append(names, name)
	}
	return names
}

// Options are the per-execution extras a caller may supply.
type Options struct {
	// Stdin is written to the child's standard input and then closed.
	//
	// This is how a secret reaches a program without appearing in argv: the
	// process table is world-readable on a normal Linux host, so a password
	// passed as an argument is visible to every account on the machine for as
	// long as the command runs.
	Stdin string
	// StdinFile streams a file into the child's standard input instead of a
	// string.
	//
	// It exists for exactly one shape of work: feeding a database dump to a
	// client that reads a stream. A dump is unbounded — a customer's database
	// may be gigabytes — and Stdin would mean holding all of it in the Agent's
	// memory to hand to a program that is going to read it a buffer at a time
	// anyway.
	//
	// The path is opened by this package and must be absolute. It is never a
	// value from a request: every caller passes a path the Agent itself
	// created.
	StdinFile string
	// Env adds variables to the sanitised base environment. Every name must
	// appear in the spec's AllowedEnv or the execution is refused.
	Env map[string]string
	// Dir overrides the working directory. It must be absolute. Empty keeps
	// the default of "/", which is deliberately not a directory any caller
	// controls.
	Dir string
	// UID and GID run the program as another account, applied only when
	// SetCredential is set — 0 is a valid uid, and root is exactly the value
	// that must never be applied by accident.
	//
	// It exists so work done on behalf of a website is done as that website:
	// npm writing node_modules as root produces a directory the application
	// can read and never update.
	UID           int
	GID           int
	SetCredential bool
}

// Run executes an allowlisted command with the given arguments.
//
// args are passed as argv entries. They are never concatenated into a string
// and never interpreted by a shell, so a value like "; rm -rf /" is handed to
// the program as one literal argument.
func (r *Runner) Run(ctx context.Context, name string, args ...string) (Result, error) {
	return r.RunWith(ctx, name, Options{}, args...)
}

// RunWith executes an allowlisted command with per-execution options.
func (r *Runner) RunWith(ctx context.Context, name string, opts Options, args ...string) (Result, error) {
	spec, ok := r.specs[name]
	if !ok {
		return Result{}, fmt.Errorf("%w: %q", ErrNotAllowed, name)
	}
	if err := validateArgs(args); err != nil {
		return Result{}, err
	}
	env, err := r.environment(spec, opts.Env)
	if err != nil {
		return Result{}, err
	}
	if !isExecutable(spec.Path) {
		return Result{}, fmt.Errorf("%w: %s", ErrUnavailable, spec.Name)
	}

	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()

	// exec.CommandContext with an absolute path performs no PATH lookup.
	cmd := exec.CommandContext(ctx, spec.Path, args...)
	cmd.Env = env
	// A working directory the caller does not control avoids relative-path
	// surprises inside the child.
	cmd.Dir = "/"
	if opts.Dir != "" {
		if !strings.HasPrefix(opts.Dir, "/") {
			return Result{}, fmt.Errorf("%w: a working directory must be absolute", ErrInvalidArg)
		}
		cmd.Dir = opts.Dir
	}
	// Never hand the child a terminal or the parent's own stdin. A caller that
	// supplied input gets a reader over exactly that string and nothing else.
	cmd.Stdin = nil
	if opts.Stdin != "" && opts.StdinFile != "" {
		return Result{}, fmt.Errorf("%w: input may come from a string or a file, not both",
			ErrInvalidArg)
	}
	if opts.Stdin != "" {
		cmd.Stdin = strings.NewReader(opts.Stdin)
	}
	if opts.StdinFile != "" {
		if !strings.HasPrefix(opts.StdinFile, "/") {
			return Result{}, fmt.Errorf("%w: an input file must be absolute", ErrInvalidArg)
		}
		input, err := os.Open(opts.StdinFile)
		if err != nil {
			return Result{}, fmt.Errorf("open input file: %w", err)
		}
		defer func() { _ = input.Close() }()
		cmd.Stdin = input
	}

	// The child gets its own process group, and cancellation kills the group
	// rather than just the program that was started. A grandchild holding the
	// output pipe open would otherwise keep Wait blocked past the deadline,
	// pinning an Agent worker.
	configureProcessGroup(cmd)
	if opts.SetCredential {
		applyCredential(cmd, opts.UID, opts.GID)
	}
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	// A backstop for a child that survives the kill: after WaitDelay, the
	// pipes are closed and Wait returns regardless.
	cmd.WaitDelay = waitDelay

	var stdout, stderr cappedBuffer
	stdout.limit = spec.MaxOutput
	stderr.limit = spec.MaxOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err = cmd.Run()
	result := Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(start),
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
		result.ExitCode = -1
		return result, fmt.Errorf("%w after %s: %s", ErrTimeout, spec.Timeout, spec.Name)
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// A non-zero exit is an outcome, not a failure of this package:
			// callers decide whether it matters.
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, fmt.Errorf("run %s: %w", spec.Name, err)
	}

	if stdout.truncated || stderr.truncated {
		return result, fmt.Errorf("%w: %s", ErrOutputTooLarge, spec.Name)
	}
	return result, nil
}

// environment builds the child's environment from the sanitised base plus any
// allowlisted additions.
func (r *Runner) environment(spec Spec, extra map[string]string) ([]string, error) {
	if len(extra) == 0 {
		return r.env, nil
	}

	allowed := make(map[string]struct{}, len(spec.AllowedEnv))
	for _, name := range spec.AllowedEnv {
		allowed[name] = struct{}{}
	}

	// Sorted so the environment a command receives is the same on every run,
	// which keeps failures reproducible.
	names := make([]string, 0, len(extra))
	for name := range extra {
		if !spec.AllowAnyEnv {
			if _, ok := allowed[name]; !ok {
				return nil, fmt.Errorf("%w: %s may not set %s", ErrNotAllowed, spec.Name, name)
			}
		}
		names = append(names, name)
	}
	sort.Strings(names)

	env := make([]string, len(r.env), len(r.env)+len(names))
	copy(env, r.env)
	for _, name := range names {
		value := extra[name]
		// A newline would let one variable forge another, and a null byte
		// truncates the entry inside the kernel.
		if strings.ContainsAny(value, "\x00\n\r") {
			return nil, fmt.Errorf("%w: %s contains a control character", ErrInvalidArg, name)
		}
		env = append(env, name+"="+value)
	}
	return env, nil
}

// validateArgs rejects argument shapes that indicate a caller mistake.
//
// Shell metacharacters are already harmless because there is no shell, so this
// is not what prevents injection. It is a second line of defence against a
// future caller building a command string by hand, and it blocks the two
// shapes that are dangerous regardless of a shell: null bytes, which truncate
// the argument inside the kernel, and newlines, which forge log lines.
func validateArgs(args []string) error {
	if len(args) > maxArgs {
		return fmt.Errorf("%w: at most %d arguments", ErrInvalidArg, maxArgs)
	}
	for _, arg := range args {
		if len(arg) > maxArgLength {
			return fmt.Errorf("%w: argument exceeds %d bytes", ErrInvalidArg, maxArgLength)
		}
		if strings.ContainsRune(arg, '\x00') {
			return fmt.Errorf("%w: argument contains a null byte", ErrInvalidArg)
		}
		if strings.ContainsAny(arg, "\n\r") {
			return fmt.Errorf("%w: argument contains a newline", ErrInvalidArg)
		}
	}
	return nil
}

// isExecutable reports whether path is a regular file with an execute bit.
func isExecutable(path string) bool {
	info, err := statFile(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// cappedBuffer collects output up to a limit and then discards the rest.
//
// Writing to a bytes.Buffer without a cap would let a program that emits
// unbounded output exhaust the Agent's memory.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	remaining := c.limit - c.buf.Len()
	if remaining <= 0 {
		c.truncated = true
		// Report the full length so the child is not blocked by short writes.
		return len(p), nil
	}
	if len(p) > remaining {
		c.buf.Write(p[:remaining])
		c.truncated = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *cappedBuffer) String() string { return c.buf.String() }
