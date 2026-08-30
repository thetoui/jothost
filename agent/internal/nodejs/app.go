package nodejs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jothost/panel/shared/validate"
)

// Errors returned by the application manager.
var (
	// ErrAppNotFound means no application by that name is known here.
	ErrAppNotFound = errors.New("no such Node.js application")
	// ErrStartupMissing means the entry point is not on disk.
	ErrStartupMissing = errors.New("the startup file does not exist")
	// ErrPortInUse means something else is already listening.
	ErrPortInUse = errors.New("that port is already in use")
	// ErrUnsupported means the host cannot run applications at all.
	ErrUnsupported = errors.New("Node.js applications cannot be managed on this host")
)

// StateDir is where the panel keeps what it knows about running applications.
//
// Outside any application's own directory, because an application must not be
// able to rewrite the record of what it is or which pid it has.
//
// A variable rather than a constant only so the tests can point it at a
// temporary directory; nothing at runtime changes it.
var StateDir = "/var/lib/jothost/node"

// setStateDir redirects the state directory. Tests only.
func setStateDir(path string) { StateDir = path }

// App is one Node.js application as the Agent runs it.
type App struct {
	// Name identifies the application. It becomes the unit name, the pid file
	// name, and the log file names, so it is validated tightly.
	Name string `json:"name"`
	// Version is the major Node.js version it runs under.
	Version string `json:"version"`
	// Root is the application's own directory, absolute.
	Root string `json:"root"`
	// Startup is the entry point, relative to Root.
	Startup string `json:"startup_file"`
	// Port is what it listens on. The panel tells the application through the
	// PORT variable, and points nginx at the same number.
	Port int `json:"port"`
	// User and Group are the account it runs as: the website's own, so an
	// application can reach its own files and nobody else's.
	User  string `json:"user"`
	Group string `json:"group"`
	// Environment is the application's own configuration.
	Environment map[string]string `json:"-"`
}

// Validate checks an application description.
//
// Every field here reaches either a unit file, a process's argument vector, or
// an environment, so all of them are checked in one place rather than at each
// point of use.
func (a App) Validate() error {
	if err := validate.AppName(a.Name); err != nil {
		return err
	}
	if err := validate.NodeVersion(a.Version); err != nil {
		return err
	}
	if err := validate.StartupFile(a.Startup); err != nil {
		return err
	}
	if err := validate.AppPort(a.Port); err != nil {
		return err
	}
	if err := validate.SystemUser(a.User); err != nil {
		return fmt.Errorf("application user: %w", err)
	}
	if a.Group != "" {
		if err := validate.GroupName(a.Group); err != nil {
			return fmt.Errorf("application group: %w", err)
		}
	}
	if !filepath.IsAbs(a.Root) || strings.Contains(a.Root, "..") {
		return fmt.Errorf("%w: the application root must be an absolute path",
			validate.ErrInvalidPath)
	}

	for key, value := range a.Environment {
		if err := validate.EnvKey(key); err != nil {
			return err
		}
		if err := validate.EnvValue(value); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return nil
}

// StartupPath returns the absolute path of the entry point.
func (a App) StartupPath() string {
	return filepath.Join(a.Root, a.Startup)
}

// UnitName is the systemd unit this application would run as.
func (a App) UnitName() string { return "jothost-node-" + a.Name + ".service" }

// PIDFile is where the Agent records the process it started.
func (a App) PIDFile() string { return filepath.Join(StateDir, a.Name+".pid") }

// EnvFile is where the application's own configuration is written.
//
// A file rather than inline directives in the unit, because it holds secrets:
// a database URL with a password in it is a normal thing for an application to
// need, and a unit file is world-readable while this is not.
func (a App) EnvFile() string { return filepath.Join(StateDir, a.Name+".env") }

// LogDir holds an application's output.
func (a App) LogDir() string { return filepath.Join("/var/log/jothost/node", a.Name) }

// OutLog and ErrLog are where the process's output goes when the Agent
// supervises it. Under systemd the journal has it instead.
func (a App) OutLog() string { return filepath.Join(a.LogDir(), "out.log") }
func (a App) ErrLog() string { return filepath.Join(a.LogDir(), "error.log") }

// Environ renders the environment the process is started with.
//
// PORT is set by the panel from the application's record, and is refused as a
// user-supplied variable, so there is exactly one place it comes from.
func (a App) Environ() map[string]string {
	env := map[string]string{
		"PORT":     strconv.Itoa(a.Port),
		"HOME":     a.Root,
		"USER":     a.User,
		"NODE_ENV": "production",
	}
	for key, value := range a.Environment {
		// NODE_ENV is the one panel default an application may override: a
		// staging deployment legitimately wants a different value, and unlike
		// PORT it does not have to agree with anything outside the process.
		if key == "NODE_ENV" {
			env[key] = value
			continue
		}
		if _, reserved := env[key]; reserved {
			continue
		}
		env[key] = value
	}
	return env
}

// WriteEnvFile writes the application's configuration where only it can read
// it.
//
// 0640 owned by the application's account: it holds whatever secrets the
// application needs, and every other account on a shared host has no business
// reading them.
func WriteEnvFile(app App, uid, gid int) error {
	if err := os.MkdirAll(StateDir, 0o750); err != nil {
		return fmt.Errorf("create the Node state directory: %w", err)
	}

	env := app.Environ()
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	// Sorted so the file is stable between writes, which makes a diff of it
	// mean something.
	sort.Strings(keys)

	var builder strings.Builder
	builder.WriteString("# Written by JotHost Panel. Edits are replaced.\n")
	for _, key := range keys {
		// systemd's EnvironmentFile and a plain shell env file agree on
		// KEY=value with no quoting, provided the value has no newline —
		// which validate.EnvValue has already guaranteed.
		builder.WriteString(key)
		builder.WriteString("=")
		builder.WriteString(env[key])
		builder.WriteString("\n")
	}

	path := app.EnvFile()
	if err := os.WriteFile(path, []byte(builder.String()), 0o640); err != nil {
		return fmt.Errorf("write the environment file: %w", err)
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("own the environment file: %w", err)
	}
	return nil
}

// ReadEnvFile reads back an application's configuration.
//
// Used to report what is set without the panel having to be the only record:
// if somebody edits the file by hand, the panel should show what the process
// will actually see.
func ReadEnvFile(app App) (map[string]string, error) {
	content, err := os.ReadFile(app.EnvFile())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("read the environment file: %w", err)
	}

	env := make(map[string]string, 8)
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		env[key] = value
	}
	return env, nil
}

// RemoveState deletes everything the panel keeps about an application.
func RemoveState(app App) error {
	for _, path := range []string{app.EnvFile(), app.PIDFile()} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}
	return nil
}
