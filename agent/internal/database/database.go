// Package database manages the database servers running on the host.
//
// Everything here obeys one rule: no value that came from a request is ever
// concatenated into SQL without first passing shared/validate, and no secret
// is ever passed as a command-line argument.
//
// The second half matters as much as the first. The process table on a Linux
// host is world-readable, so `mysql -p<password>` publishes that password to
// every account on the machine for the lifetime of the command. Every provider
// here sends its SQL — including the statement that sets a password — on the
// client's standard input, and supplies its own credentials through a
// mode-0600 option file or an environment variable, never argv.
package database

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"sort"
	"strings"

	"github.com/jothost/panel/shared/validate"
)

// Errors returned by the manager and its providers.
var (
	// ErrEngineUnavailable means no server of that engine is reachable.
	ErrEngineUnavailable = errors.New("that database engine is not available on this host")
	// ErrNotFound means the database or user does not exist.
	ErrNotFound = errors.New("not found")
	// ErrExists means the database or user is already there.
	ErrExists = errors.New("already exists")
	// ErrUserExists means the account was already on the server, so the
	// password supplied to create it was not applied.
	ErrUserExists = errors.New("that account already exists on this database server")
	// ErrInvalidPassword means a supplied password is outside the accepted set.
	ErrInvalidPassword = errors.New("invalid database password")
	// ErrQueryFailed wraps a non-zero exit from a database client.
	ErrQueryFailed = errors.New("database command failed")
)

// Privilege levels the panel offers.
//
// Raw privilege lists are deliberately not accepted from callers. A panel that
// forwards an arbitrary GRANT string is a panel that can be asked to grant
// SUPER or FILE, either of which turns a website account into a way to read
// the whole server. These three cover what a hosted application needs.
const (
	PrivilegeReadOnly  = "readonly"
	PrivilegeReadWrite = "readwrite"
	PrivilegeFull      = "full"
)

// Privileges reports the offered privilege levels, weakest first.
func Privileges() []string {
	return []string{PrivilegeReadOnly, PrivilegeReadWrite, PrivilegeFull}
}

// ValidPrivilege reports whether level is one the panel offers.
func ValidPrivilege(level string) bool {
	switch level {
	case PrivilegeReadOnly, PrivilegeReadWrite, PrivilegeFull:
		return true
	default:
		return false
	}
}

// Database is one database as the server reports it.
type Database struct {
	Name   string `json:"name"`
	Engine string `json:"engine"`
	// Charset and Collation are empty on engines that do not report them per
	// database in a single query.
	Charset   string `json:"charset,omitempty"`
	Collation string `json:"collation,omitempty"`
	Owner     string `json:"owner,omitempty"`
	SizeBytes int64  `json:"size_bytes"`
}

// User is a database account.
//
// Host is meaningful only on MySQL and MariaDB, where an account is identified
// by the pair. PostgreSQL roles are global, so it is empty there rather than
// filled with a value that would imply a restriction that does not exist.
type User struct {
	Username string `json:"username"`
	Host     string `json:"host,omitempty"`
	Engine   string `json:"engine"`
}

// Grant is one account's access to one database.
type Grant struct {
	Username  string `json:"username"`
	Host      string `json:"host,omitempty"`
	Database  string `json:"database"`
	Privilege string `json:"privilege"`
}

// EngineInfo describes one engine as found on this host.
type EngineInfo struct {
	Engine    string `json:"engine"`
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	// Detail says why an engine is unavailable, so the panel can explain the
	// absence instead of just hiding a feature.
	Detail string `json:"detail,omitempty"`
	// SupportsHostPatterns reports whether accounts are identified by a
	// user/host pair. The panel uses it to show or hide the host control.
	SupportsHostPatterns bool `json:"supports_host_patterns"`
}

// Provider drives one database server.
type Provider interface {
	// Engine returns the engine name this provider reports as. It is resolved
	// at detection time, because "mysql" and "mariadb" are the same client
	// speaking to different servers.
	Engine() string
	Available() bool
	// Unavailable explains why the engine cannot be used, or returns empty
	// when it can. It exists so the panel can distinguish "not installed"
	// from "installed but not answering": the two have different fixes, and
	// reporting the second as the first sends an operator hunting for a
	// package that is already there.
	Unavailable() string
	Version(ctx context.Context) (string, error)
	SupportsHostPatterns() bool

	ListDatabases(ctx context.Context) ([]Database, error)
	CreateDatabase(ctx context.Context, name string) error
	DropDatabase(ctx context.Context, name string) error
	DatabaseSize(ctx context.Context, name string) (int64, error)

	ListUsers(ctx context.Context) ([]User, error)
	// CreateUser reports whether it actually created the account.
	//
	// The distinction is not cosmetic. Creating an account is idempotent, so a
	// second call leaves an existing account's password alone — and a caller
	// that assumed otherwise would hand the operator a freshly generated
	// password the server never accepted, which fails only later and gives no
	// clue why.
	CreateUser(ctx context.Context, user User, password string) (bool, error)
	DropUser(ctx context.Context, user User) error
	SetPassword(ctx context.Context, user User, password string) error
	Grant(ctx context.Context, grant Grant) error
	RevokeAll(ctx context.Context, user User, database string) error
}

// Manager routes an operation to the provider for its engine.
type Manager struct {
	providers map[string]Provider
	log       *slog.Logger
}

// ManagerOptions configure a Manager.
type ManagerOptions struct {
	Providers []Provider
	Log       *slog.Logger
}

// NewManager builds a Manager over the providers that detected a server.
//
// A provider whose server is not installed is kept rather than dropped: the
// panel reports "MariaDB is not installed on this host", which is a far more
// useful answer than an engine silently missing from a list.
func NewManager(opts ManagerOptions) *Manager {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}

	providers := make(map[string]Provider, len(opts.Providers))
	for _, provider := range opts.Providers {
		providers[provider.Engine()] = provider
	}
	return &Manager{providers: providers, log: log}
}

// Engines reports every engine the panel knows about and whether it is usable.
func (m *Manager) Engines(ctx context.Context) []EngineInfo {
	infos := make([]EngineInfo, 0, len(m.providers))
	for _, provider := range m.providers {
		info := EngineInfo{
			Engine:               provider.Engine(),
			Available:            provider.Available(),
			SupportsHostPatterns: provider.SupportsHostPatterns(),
		}
		if !info.Available {
			info.Detail = provider.Unavailable()
			infos = append(infos, info)
			continue
		}

		version, err := provider.Version(ctx)
		if err != nil {
			// Installed but unreachable is a third state, and conflating it
			// with "not installed" would send an operator looking for a
			// package that is already there.
			info.Available = false
			info.Detail = "the server is installed but stopped answering"
			m.log.Warn("database engine did not answer",
				"engine", provider.Engine(), "error", err.Error())
			infos = append(infos, info)
			continue
		}
		info.Version = version
		infos = append(infos, info)
	}

	sort.Slice(infos, func(i, j int) bool { return infos[i].Engine < infos[j].Engine })
	return infos
}

// Provider returns the provider for an engine.
func (m *Manager) Provider(engine string) (Provider, error) {
	if err := validate.DatabaseEngine(engine); err != nil {
		return nil, err
	}
	provider, ok := m.providers[engine]
	if !ok || !provider.Available() {
		return nil, fmt.Errorf("%w: %s", ErrEngineUnavailable, engine)
	}
	return provider, nil
}

// Available reports whether any engine at all is usable.
func (m *Manager) Available() bool {
	for _, provider := range m.providers {
		if provider.Available() {
			return true
		}
	}
	return false
}

// passwordAlphabet is the character set a database password may draw from.
//
// It deliberately excludes the quote characters, the backslash, and the
// backtick. A password reaches the server inside a string literal in a
// statement — there is no parameter form for CREATE USER on either engine —
// so a password containing a quote would end that literal. Escaping would work
// and is applied anyway, but a password that cannot contain a quote in the
// first place removes the possibility of an escaping bug mattering.
//
// The set still gives 6.2 bits per character, so a 24-character generated
// password carries about 149 bits of entropy.
const passwordAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789!#%()*+,-.:;=?@^_~"

// Password length bounds.
const (
	MinPasswordLength       = 12
	MaxPasswordLength       = 128
	GeneratedPasswordLength = 24
)

// ValidatePassword checks a caller-supplied password.
func ValidatePassword(password string) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("%w: must be at least %d characters",
			ErrInvalidPassword, MinPasswordLength)
	}
	if len(password) > MaxPasswordLength {
		return fmt.Errorf("%w: must be at most %d characters",
			ErrInvalidPassword, MaxPasswordLength)
	}
	for _, r := range password {
		if !strings.ContainsRune(passwordAlphabet, r) {
			return fmt.Errorf("%w: it contains a character that is not permitted "+
				"(quotes and backslashes cannot be used)", ErrInvalidPassword)
		}
	}
	return nil
}

// GeneratePassword returns a new random password.
//
// crypto/rand, and rejection-free selection by index into the alphabet using
// big.Int, so the distribution is uniform. math/rand would produce passwords
// predictable from the process start time.
func GeneratePassword() (string, error) {
	limit := big.NewInt(int64(len(passwordAlphabet)))
	out := make([]byte, GeneratedPasswordLength)

	for i := range out {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("generate password: %w", err)
		}
		out[i] = passwordAlphabet[n.Int64()]
	}
	return string(out), nil
}
