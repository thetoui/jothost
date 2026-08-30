package nodejs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/agent/internal/sites"
)

// State is what an application is doing.
type State string

// Application states.
const (
	StateRunning State = "running"
	StateStopped State = "stopped"
	// StateFailed means the panel started it and it is no longer there.
	StateFailed State = "failed"
)

// Status is an application's runtime state.
type Status struct {
	Name    string `json:"name"`
	State   State  `json:"state"`
	PID     int    `json:"pid,omitempty"`
	Port    int    `json:"port,omitempty"`
	Uptime  int64  `json:"uptime_seconds,omitempty"`
	Managed string `json:"managed_by"`
	// Listening reports whether something is actually accepting connections on
	// the port. A process that is up but not listening is the difference
	// between "started" and "working", and the panel should not conflate them.
	Listening bool   `json:"listening"`
	Detail    string `json:"detail,omitempty"`
}

// How the process is managed.
const (
	ManagedBySystemd    = "systemd"
	ManagedBySupervisor = "agent"
)

// startupGrace is how long the panel waits for a freshly started application
// to begin listening before reporting on it.
//
// Node applications take a moment to bind. Reporting "not listening" the
// instant after start would be true and useless.
const startupGrace = 5 * time.Second

// Supervisor runs applications on a host without systemd.
//
// It is not a general-purpose init: it starts a detached process, records its
// pid, and can tell whether that pid is still the process it started. Anything
// more — dependency ordering, socket activation, cgroup limits — is what
// systemd is for, and where systemd exists the Agent uses it instead.
type Supervisor struct {
	runner *command.Runner
	users  *sites.UserProvider
	log    *slog.Logger
}

// NewSupervisor builds a Supervisor.
func NewSupervisor(runner *command.Runner, users *sites.UserProvider, log *slog.Logger) *Supervisor {
	if log == nil {
		log = slog.Default()
	}
	return &Supervisor{runner: runner, users: users, log: log}
}

// Start launches an application and records its process id.
//
// Idempotent: an application already running is left alone rather than started
// twice, because two processes on one port means one of them is failing and
// the panel would not know which.
func (s *Supervisor) Start(ctx context.Context, app App, binary string) (Status, error) {
	if err := app.Validate(); err != nil {
		return Status{}, err
	}

	if status := s.Status(app); status.State == StateRunning {
		return status, nil
	}

	if _, err := os.Stat(app.StartupPath()); err != nil {
		return Status{}, fmt.Errorf("%w: %s", ErrStartupMissing, app.Startup)
	}

	// Checked before starting rather than after failing: "address already in
	// use" arrives in the application's own log, which nobody is looking at
	// yet, whereas this is a refusal the panel can show.
	if inUse(app.Port) {
		return Status{}, fmt.Errorf("%w: %d", ErrPortInUse, app.Port)
	}

	account, err := s.account(app)
	if err != nil {
		return Status{}, err
	}

	if err := s.prepare(app, account); err != nil {
		return Status{}, err
	}

	pid, err := s.runner.StartDetached(ctx, CommandNode, command.DetachedOptions{
		Dir:           app.Root,
		Env:           app.Environ(),
		Stdout:        app.OutLog(),
		Stderr:        app.ErrLog(),
		UID:           account.UID,
		GID:           account.GID,
		SetCredential: true,
	}, app.StartupPath())
	if err != nil {
		return Status{}, fmt.Errorf("start %s: %w", app.Name, err)
	}

	if err := s.writePID(app, pid); err != nil {
		// The process is running; not being able to record it means the panel
		// cannot stop it later, which is worse than not having started it.
		_ = syscall.Kill(pid, syscall.SIGTERM)
		return Status{}, err
	}

	s.log.Info("started node application", "app", app.Name, "pid", pid, "port", app.Port)
	return s.awaitListening(app), nil
}

// Stop ends an application.
//
// SIGTERM first, because a Node process given one runs its shutdown handlers
// and finishes the requests it is holding. SIGKILL only after a grace period,
// for a process that ignored it.
func (s *Supervisor) Stop(app App) error {
	pid, err := s.readPID(app)
	if err != nil || pid == 0 {
		// Nothing recorded: already stopped, which is the outcome asked for.
		return s.clearPID(app)
	}

	if !s.isOurs(app, pid) {
		// The pid has been reused by something else. Signalling it would kill
		// an unrelated process.
		s.log.Warn("stale pid file for a node application", "app", app.Name, "pid", pid)
		return s.clearPID(app)
	}

	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stop %s: %w", app.Name, err)
	}

	deadline := time.Now().Add(stopGrace)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return s.clearPID(app)
		}
		time.Sleep(100 * time.Millisecond)
	}

	s.log.Warn("node application did not stop; killing it", "app", app.Name, "pid", pid)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill %s: %w", app.Name, err)
	}
	return s.clearPID(app)
}

// stopGrace is how long a process gets to shut down on its own.
const stopGrace = 10 * time.Second

// Status reports what an application is doing.
func (s *Supervisor) Status(app App) Status {
	status := Status{Name: app.Name, Port: app.Port, Managed: ManagedBySupervisor}

	pid, err := s.readPID(app)
	if err != nil || pid == 0 {
		status.State = StateStopped
		return status
	}

	if !s.isOurs(app, pid) {
		// The panel started something and it is no longer there. That is a
		// different fact from "stopped", and an operator needs to see it.
		status.State = StateFailed
		status.Detail = "the process the panel started is no longer running"
		return status
	}

	status.State = StateRunning
	status.PID = pid
	status.Uptime = processUptime(pid)
	status.Listening = inUse(app.Port)
	if !status.Listening {
		status.Detail = "the process is running but nothing is listening on its port yet"
	}
	return status
}

// awaitListening gives a freshly started application a moment to bind.
func (s *Supervisor) awaitListening(app App) Status {
	deadline := time.Now().Add(startupGrace)
	for {
		status := s.Status(app)
		if status.State != StateRunning || status.Listening || time.Now().After(deadline) {
			return status
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// prepare creates the directories an application writes to.
func (s *Supervisor) prepare(app App, account sites.Account) error {
	// The parent is traversable, the leaf is not readable by anyone else. Both
	// halves matter: created 0700 all the way down, the application's own
	// account cannot reach its own log directory.
	if err := os.MkdirAll(filepath.Dir(app.LogDir()), 0o755); err != nil {
		return fmt.Errorf("create the Node log directory: %w", err)
	}
	if err := os.MkdirAll(app.LogDir(), 0o750); err != nil {
		return fmt.Errorf("create %s: %w", app.LogDir(), err)
	}
	if err := os.Chown(app.LogDir(), account.UID, account.GID); err != nil {
		return fmt.Errorf("own %s: %w", app.LogDir(), err)
	}
	if err := os.MkdirAll(StateDir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", StateDir, err)
	}
	return WriteEnvFile(app, account.UID, account.GID)
}

// account resolves the system account an application runs as.
func (s *Supervisor) account(app App) (sites.Account, error) {
	if s.users == nil {
		return sites.Account{}, fmt.Errorf("%w: no user management is available", ErrUnsupported)
	}

	account, found, err := s.users.Lookup(app.User)
	if err != nil {
		return sites.Account{}, fmt.Errorf("look up %s: %w", app.User, err)
	}
	if !found {
		return sites.Account{}, fmt.Errorf("%w: the account %s does not exist",
			ErrUnsupported, app.User)
	}
	return account, nil
}

// writePID records the process the panel started.
func (s *Supervisor) writePID(app App, pid int) error {
	if err := os.MkdirAll(StateDir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", StateDir, err)
	}
	// Owned by root and not writable by the application: an application that
	// could rewrite its own pid file could point the panel's stop at another
	// process.
	if err := os.WriteFile(app.PIDFile(), []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		return fmt.Errorf("write the pid file: %w", err)
	}
	return nil
}

func (s *Supervisor) readPID(app App) (int, error) {
	content, err := os.ReadFile(app.PIDFile())
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read the pid file: %w", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil || pid <= 0 {
		return 0, nil
	}
	return pid, nil
}

func (s *Supervisor) clearPID(app App) error {
	if err := os.Remove(app.PIDFile()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear the pid file: %w", err)
	}
	return nil
}

// isOurs reports whether a pid is still the application the panel started.
//
// A pid on its own is not enough: pids are reused, and signalling a recycled
// one would kill an unrelated process. The command line is checked as well, so
// what is signalled is a node process running this application's entry point.
func (s *Supervisor) isOurs(app App, pid int) bool {
	cmdline, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}

	// /proc cmdline is NUL-separated.
	args := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
	if len(args) == 0 {
		return false
	}

	if !strings.Contains(filepath.Base(args[0]), "node") {
		return false
	}
	for _, arg := range args {
		if arg == app.StartupPath() {
			return true
		}
	}
	return false
}

// processAlive reports whether a pid exists.
func processAlive(pid int) bool {
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return err == nil
}

// processUptime reports how long a pid has been running, in seconds.
func processUptime(pid int) int64 {
	info, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	if err != nil {
		return 0
	}
	seconds := int64(time.Since(info.ModTime()).Seconds())
	if seconds < 0 {
		return 0
	}
	return seconds
}

// inUse reports whether something is listening on a port.
//
// A connect rather than a bind: binding to test would race with the
// application the panel is about to start, and would fail on a port held by
// another user's process in a way that looks like the port is free.
func inUse(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
