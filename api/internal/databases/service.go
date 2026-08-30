package databases

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/websites"
	"github.com/jothost/panel/shared/validate"
)

// Audit action names for database work (CLAUDE.md section 15).
const (
	ActionDatabaseCreate   = "database.create"
	ActionDatabaseDelete   = "database.delete"
	ActionDatabaseUserAdd  = "database.user.create"
	ActionDatabaseUserDrop = "database.user.delete"
	ActionPasswordChange   = "database.user.password"
	ActionPasswordReveal   = "database.user.reveal"
	ActionGrantChange      = "database.grant"

	ResourceTypeDatabase = "database"
)

// Errors returned by the service.
var (
	// ErrEngineUnavailable means the host does not run that engine.
	ErrEngineUnavailable = errors.New("that database engine is not available on this server")
	// ErrNoServer means no host is registered, so there is nothing to act on.
	ErrNoServer = errors.New("no server is registered, so databases cannot be managed")
	// ErrHostNotSupported means a host pattern was given for an engine that
	// does not identify accounts by one.
	ErrHostNotSupported = errors.New("this engine does not use host patterns for accounts")
	// ErrInvalidPrivilege means the privilege level is not one the panel offers.
	ErrInvalidPrivilege = errors.New("unknown privilege level")
	// ErrDatabaseInUse means a database still has accounts granted on it.
	ErrDatabaseInUse = errors.New("this database still has users; remove them or confirm the deletion")
	// ErrUserExistsUnmanaged means the account is already on the database
	// server but the panel has no record of it, so its password is unknown.
	ErrUserExistsUnmanaged = errors.New(
		"an account with that name already exists on the database server, and the panel does not " +
			"know its password; choose another name, or remove the account on the server first")
)

// Agent is the subset of the agent client this service uses.
//
// It is an interface so the service can be tested without a host: the tests
// that matter here are about what the panel records and refuses, and those
// should not need a running MariaDB.
type Agent interface {
	DatabaseEngines(ctx context.Context, requestID string) (agentclient.DatabaseEnginesResult, error)
	DatabaseCreate(ctx context.Context, requestID, engine, name string) (agentclient.DatabaseCreateResult, error)
	DatabaseDelete(ctx context.Context, requestID, engine, name string) error
	DatabaseSize(ctx context.Context, requestID, engine, name string) (agentclient.DatabaseSizeResult, error)
	DatabaseList(ctx context.Context, requestID, engine string) (agentclient.DatabaseListResult, error)
	DatabaseUserCreate(ctx context.Context, requestID string, req agentclient.DatabaseUserRequest) (agentclient.DatabaseUserResult, error)
	DatabaseUserPassword(ctx context.Context, requestID string, req agentclient.DatabaseUserRequest) (agentclient.DatabaseUserResult, error)
	DatabaseUserDelete(ctx context.Context, requestID, engine, username, host string) error
	DatabaseGrant(ctx context.Context, requestID, engine, username, host, database, privilege string) error
}

// Service coordinates the panel's records with the host's database servers.
//
// Unlike websites and certificates, these operations are synchronous. Creating
// a database is a single DDL statement that completes in milliseconds; putting
// it through the job queue would add a poll cycle of latency and leave the
// panel unable to hand back the generated password in the response that asked
// for the account. The trade is deliberate: the request holds until the host
// has actually done the work, so a 201 means the database exists.
type Service struct {
	repo     *Repository
	websites *websites.Repository
	agent    Agent
	audit    *audit.Recorder
	log      *slog.Logger
	serverID string
}

// ServiceOptions configure a Service.
type ServiceOptions struct {
	Repository *Repository
	Websites   *websites.Repository
	Agent      Agent
	Audit      *audit.Recorder
	Log        *slog.Logger
	ServerID   string
}

// NewService builds a Service.
func NewService(opts ServiceOptions) *Service {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		repo:     opts.Repository,
		websites: opts.Websites,
		agent:    opts.Agent,
		audit:    opts.Audit,
		log:      log,
		serverID: opts.ServerID,
	}
}

// Actor identifies who asked, for the audit trail.
type Actor struct {
	UserID    string
	IPAddress string
	UserAgent string
}

// Engines reports what the host can do.
func (s *Service) Engines(ctx context.Context, requestID string) (agentclient.DatabaseEnginesResult, error) {
	return s.agent.DatabaseEngines(ctx, requestID)
}

// CreateRequest asks for a new database.
type CreateRequest struct {
	Name      string
	Engine    string
	WebsiteID string
	// CreateUser asks for a dedicated account with full rights on the new
	// database, which is what almost every caller wants: a database with no
	// account is unusable, and the alternative is making everyone perform two
	// steps that only ever happen together.
	CreateUser bool
	Username   string
	Host       string
	Password   string
	Actor      Actor
	RequestID  string
}

// CreateResult is a new database and, when one was asked for, its account.
type CreateResult struct {
	Database Database
	User     *User
	// Password is returned exactly once, in the response to the request that
	// created the account. It is never included in any listing.
	Password string
}

// Create records a database and creates it on the host.
func (s *Service) Create(ctx context.Context, req CreateRequest) (CreateResult, error) {
	if s.serverID == "" {
		return CreateResult{}, ErrNoServer
	}

	name := strings.ToLower(strings.TrimSpace(req.Name))
	if err := validate.DatabaseName(name); err != nil {
		return CreateResult{}, err
	}
	engine, err := s.resolveEngine(ctx, req.RequestID, req.Engine)
	if err != nil {
		return CreateResult{}, err
	}

	var websiteID *string
	if req.WebsiteID != "" {
		site, err := s.websites.Get(ctx, req.WebsiteID)
		if err != nil {
			return CreateResult{}, err
		}
		websiteID = &site.ID
	}

	// The row is written first, in "creating", so a failure on the host leaves
	// a visible record of what was attempted rather than nothing at all.
	record, err := s.repo.Create(ctx, CreateParams{
		ServerID:  s.serverID,
		WebsiteID: websiteID,
		Name:      name,
		Engine:    engine.Engine,
		Status:    StatusCreating,
	})
	if err != nil {
		return CreateResult{}, err
	}

	created, err := s.agent.DatabaseCreate(ctx, req.RequestID, engine.Engine, name)
	if err != nil {
		s.failDatabase(ctx, record.ID)
		return CreateResult{}, err
	}

	if err := s.repo.SetStatus(ctx, record.ID, StatusActive); err != nil {
		return CreateResult{}, err
	}
	if err := s.repo.RecordSize(ctx, record.ID, created.SizeBytes); err != nil {
		// A missing size is cosmetic; the database exists either way.
		s.log.Warn("could not record a new database's size",
			"database", name, "error", err.Error())
	}
	if err := s.repo.RecordEncoding(ctx, record.ID, created.Charset, created.Collation); err != nil {
		s.log.Warn("could not record a new database's encoding",
			"database", name, "error", err.Error())
	}

	s.record(ctx, req.Actor, ActionDatabaseCreate, record.ID, map[string]any{
		"name": name, "engine": engine.Engine,
	})

	result := CreateResult{}
	if req.CreateUser {
		user, password, err := s.addUser(ctx, addUserRequest{
			RequestID:  req.RequestID,
			Engine:     engine,
			DatabaseID: record.ID,
			Database:   name,
			Username:   req.Username,
			Host:       req.Host,
			Password:   req.Password,
			Privilege:  privilegeFull,
			Actor:      req.Actor,
		})
		if err != nil {
			// The database was created and stays: deleting it because the
			// account failed would destroy something the operator asked for
			// over a problem they can fix by adding a user by hand.
			s.log.Error("database created but its user could not be added",
				"database", name, "error", err.Error())
			return CreateResult{}, err
		}
		result.User = &user
		result.Password = password
	}

	stored, err := s.repo.Get(ctx, record.ID)
	if err != nil {
		return CreateResult{}, err
	}
	result.Database = stored
	return result, nil
}

// Delete removes a database from the host and the panel's records.
//
// The host is asked first. If the panel forgot the row and the drop then
// failed, the database would exist with nothing tracking it — invisible, still
// consuming disk, and impossible to remove through the panel.
func (s *Service) Delete(ctx context.Context, requestID, id string, actor Actor) error {
	record, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}

	if err := s.repo.SetStatus(ctx, id, StatusDeleting); err != nil {
		return err
	}

	if err := s.agent.DatabaseDelete(ctx, requestID, record.Engine, record.Name); err != nil {
		s.failDatabase(ctx, id)
		return err
	}

	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}

	s.record(ctx, actor, ActionDatabaseDelete, id, map[string]any{
		"name": record.Name, "engine": record.Engine,
	})
	return nil
}

// RefreshSize asks the host how big a database is and records the answer.
func (s *Service) RefreshSize(ctx context.Context, requestID, id string) (Database, error) {
	record, err := s.repo.Get(ctx, id)
	if err != nil {
		return Database{}, err
	}

	size, err := s.agent.DatabaseSize(ctx, requestID, record.Engine, record.Name)
	if err != nil {
		return Database{}, err
	}
	if err := s.repo.RecordSize(ctx, id, size.SizeBytes); err != nil {
		return Database{}, err
	}
	return s.repo.Get(ctx, id)
}

// AddUserRequest asks for an account on one database.
type AddUserRequest struct {
	DatabaseID string
	Username   string
	Host       string
	Password   string
	Privilege  string
	Actor      Actor
	RequestID  string
}

// AddUser creates an account and grants it access to one database.
func (s *Service) AddUser(ctx context.Context, req AddUserRequest) (User, string, error) {
	record, err := s.repo.Get(ctx, req.DatabaseID)
	if err != nil {
		return User{}, "", err
	}
	engine, err := s.resolveEngine(ctx, req.RequestID, record.Engine)
	if err != nil {
		return User{}, "", err
	}

	privilege := req.Privilege
	if privilege == "" {
		privilege = privilegeFull
	}
	if !validPrivilege(privilege) {
		return User{}, "", fmt.Errorf("%w: %q", ErrInvalidPrivilege, privilege)
	}

	return s.addUser(ctx, addUserRequest{
		RequestID:  req.RequestID,
		Engine:     engine,
		DatabaseID: record.ID,
		Database:   record.Name,
		Username:   req.Username,
		Host:       req.Host,
		Password:   req.Password,
		Privilege:  privilege,
		Actor:      req.Actor,
	})
}

// addUserRequest is the internal shape both entry points converge on.
type addUserRequest struct {
	RequestID  string
	Engine     agentclient.DatabaseEngine
	DatabaseID string
	Database   string
	Username   string
	Host       string
	Password   string
	Privilege  string
	Actor      Actor
}

func (s *Service) addUser(ctx context.Context, req addUserRequest) (User, string, error) {
	username := strings.ToLower(strings.TrimSpace(req.Username))
	if username == "" {
		// Defaulting to the database's own name keeps the pair obvious in a
		// server that may hold fifty of each.
		username = truncateUser(req.Database)
	}
	if err := validate.DatabaseUser(username); err != nil {
		return User{}, "", err
	}

	host, err := resolveHost(req.Engine, req.Host)
	if err != nil {
		return User{}, "", err
	}

	// The account is created on the host first: it is the host that decides
	// the password when none was supplied, and recording a password the server
	// never accepted would hand the operator a credential that does not work.
	created, err := s.agent.DatabaseUserCreate(ctx, req.RequestID, agentclient.DatabaseUserRequest{
		Engine:   req.Engine.Engine,
		Username: username,
		Host:     host,
		Password: req.Password,
	})
	if err != nil {
		return User{}, "", err
	}

	var user User
	if created.Created {
		user, err = s.repo.CreateUser(ctx, CreateUserParams{
			ServerID: s.serverID,
			Engine:   req.Engine.Engine,
			Username: username,
			Host:     host,
			Password: created.Password,
		})
		if errors.Is(err, ErrDuplicateUser) {
			// The host created the account, so the panel's record is stale:
			// something removed the account on the server without the panel
			// hearing about it, and the stored password is for an account that
			// no longer exists. The password just set is the authoritative one,
			// so the record is corrected rather than left to hand out a
			// credential that cannot work.
			existing, lookupErr := s.userByIdentity(ctx, req.Engine.Engine, username, host)
			if lookupErr != nil {
				return User{}, "", err
			}
			if updateErr := s.repo.SetPassword(ctx, existing.ID, created.Password); updateErr != nil {
				return User{}, "", updateErr
			}
			s.log.Warn("a database account was recreated on the host; its stored password was replaced",
				"engine", req.Engine.Engine, "username", username)
			user = existing
			err = nil
		}
		if err != nil {
			return User{}, "", err
		}
	} else {
		// The account was already on the server, so it kept the password it
		// had and the Agent returned none. The panel's own record is the only
		// place that password could be — and if there is no record, nobody
		// knows it. Saying so is the only honest answer: writing a row with
		// the password that was *not* applied would produce a credential that
		// looks right and does not work.
		existing, lookupErr := s.userByIdentity(ctx, req.Engine.Engine, username, host)
		if lookupErr != nil {
			if errors.Is(lookupErr, ErrUserNotFound) {
				return User{}, "", ErrUserExistsUnmanaged
			}
			return User{}, "", lookupErr
		}
		user = existing
	}

	if err := s.agent.DatabaseGrant(ctx, req.RequestID, req.Engine.Engine,
		username, host, req.Database, req.Privilege); err != nil {
		return User{}, "", err
	}
	if err := s.repo.SetGrant(ctx, user.ID, req.DatabaseID, req.Privilege); err != nil {
		return User{}, "", err
	}

	s.record(ctx, req.Actor, ActionDatabaseUserAdd, req.DatabaseID, map[string]any{
		"username": username, "host": host, "engine": req.Engine.Engine,
		"database": req.Database, "privilege": req.Privilege,
	})

	user.Grants = []Grant{{
		DatabaseID: req.DatabaseID, DatabaseName: req.Database, Privilege: req.Privilege,
	}}
	// Empty when the account already existed. The caller shows the stored
	// password through the deliberate, audited read instead.
	return user, created.Password, nil
}

// DeleteUser removes an account from the host and the panel's records.
func (s *Service) DeleteUser(ctx context.Context, requestID, userID string, actor Actor) error {
	user, err := s.repo.GetUser(ctx, userID)
	if err != nil {
		return err
	}

	if err := s.agent.DatabaseUserDelete(ctx, requestID, user.Engine, user.Username, user.Host); err != nil {
		return err
	}
	if err := s.repo.DeleteUser(ctx, userID); err != nil {
		return err
	}

	s.record(ctx, actor, ActionDatabaseUserDrop, "", map[string]any{
		"username": user.Username, "host": user.Host, "engine": user.Engine,
	})
	return nil
}

// SetPassword changes an account's password on the host and in the record.
func (s *Service) SetPassword(ctx context.Context, requestID, userID, password string,
	actor Actor,
) (string, error) {
	user, err := s.repo.GetUser(ctx, userID)
	if err != nil {
		return "", err
	}

	changed, err := s.agent.DatabaseUserPassword(ctx, requestID, agentclient.DatabaseUserRequest{
		Engine:   user.Engine,
		Username: user.Username,
		Host:     user.Host,
		Password: password,
	})
	if err != nil {
		return "", err
	}

	// The stored copy is updated only after the server accepted the change. A
	// panel that records a password the server rejected shows the operator a
	// credential that silently does not work.
	if err := s.repo.SetPassword(ctx, userID, changed.Password); err != nil {
		return "", err
	}

	s.record(ctx, actor, ActionPasswordChange, "", map[string]any{
		"username": user.Username, "host": user.Host, "engine": user.Engine,
	})
	return changed.Password, nil
}

// RevealPassword returns an account's stored password.
//
// Every call is audited, because this is the one endpoint in the panel that
// hands back a working credential, and "who looked at this and when" is the
// only question worth being able to answer afterwards.
func (s *Service) RevealPassword(ctx context.Context, userID string, actor Actor) (string, error) {
	user, err := s.repo.GetUser(ctx, userID)
	if err != nil {
		return "", err
	}

	password, err := s.repo.RevealPassword(ctx, userID)
	if err != nil {
		return "", err
	}

	s.record(ctx, actor, ActionPasswordReveal, "", map[string]any{
		"username": user.Username, "host": user.Host, "engine": user.Engine,
	})
	return password, nil
}

// SetGrant changes an account's privilege on a database, or removes it.
func (s *Service) SetGrant(ctx context.Context, requestID, databaseID, userID, privilege string,
	actor Actor,
) error {
	record, err := s.repo.Get(ctx, databaseID)
	if err != nil {
		return err
	}
	user, err := s.repo.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if privilege != "" && !validPrivilege(privilege) {
		return fmt.Errorf("%w: %q", ErrInvalidPrivilege, privilege)
	}

	if err := s.agent.DatabaseGrant(ctx, requestID, record.Engine,
		user.Username, user.Host, record.Name, privilege); err != nil {
		return err
	}

	if privilege == "" {
		if err := s.repo.RemoveGrant(ctx, userID, databaseID); err != nil {
			return err
		}
	} else if err := s.repo.SetGrant(ctx, userID, databaseID, privilege); err != nil {
		return err
	}

	s.record(ctx, actor, ActionGrantChange, databaseID, map[string]any{
		"username": user.Username, "host": user.Host,
		"database": record.Name, "privilege": privilege,
	})
	return nil
}

// resolveEngine picks the engine to use and checks the host can run it.
//
// An empty name means "whatever this host has", which is the right default: a
// host running one database server should not make anybody choose.
func (s *Service) resolveEngine(ctx context.Context, requestID, requested string) (agentclient.DatabaseEngine, error) {
	engines, err := s.agent.DatabaseEngines(ctx, requestID)
	if err != nil {
		return agentclient.DatabaseEngine{}, err
	}

	available := make([]agentclient.DatabaseEngine, 0, len(engines.Engines))
	for _, engine := range engines.Engines {
		if engine.Available {
			available = append(available, engine)
		}
	}
	if len(available) == 0 {
		return agentclient.DatabaseEngine{}, ErrEngineUnavailable
	}

	if requested == "" {
		return available[0], nil
	}
	if err := validate.DatabaseEngine(requested); err != nil {
		return agentclient.DatabaseEngine{}, err
	}
	for _, engine := range available {
		if engine.Engine == requested {
			return engine, nil
		}
	}
	return agentclient.DatabaseEngine{}, fmt.Errorf("%w: %s", ErrEngineUnavailable, requested)
}

// userByIdentity finds an account by what identifies it on the server.
func (s *Service) userByIdentity(ctx context.Context, engine, username, host string) (User, error) {
	users, err := s.repo.ListAllUsers(ctx)
	if err != nil {
		return User{}, err
	}
	for _, user := range users {
		if user.Engine == engine && user.Username == username && user.Host == host {
			return user, nil
		}
	}
	return User{}, ErrUserNotFound
}

// failDatabase marks a record failed, logging rather than masking the original
// error that brought us here.
func (s *Service) failDatabase(ctx context.Context, id string) {
	if err := s.repo.SetStatus(ctx, id, StatusFailed); err != nil {
		s.log.Error("could not mark a database failed", "database_id", id, "error", err.Error())
	}
}

// record writes an audit event, logging rather than failing the request.
func (s *Service) record(ctx context.Context, actor Actor, action, databaseID string,
	metadata map[string]any,
) {
	if s.audit == nil {
		return
	}
	event := audit.Event{
		UserID:       actor.UserID,
		Action:       action,
		ResourceType: ResourceTypeDatabase,
		IPAddress:    actor.IPAddress,
		UserAgent:    actor.UserAgent,
		Status:       audit.StatusSuccess,
		Metadata:     metadata,
	}
	// ResourceID is a uuid column, so it is set only when there is a real one.
	// An account has no database of its own to point at.
	if databaseID != "" {
		event.ResourceID = databaseID
	}
	s.audit.RecordAsync(ctx, event)
}

// Privilege levels, mirroring the Agent's set and migration 0008's CHECK.
const (
	privilegeReadOnly  = "readonly"
	privilegeReadWrite = "readwrite"
	privilegeFull      = "full"
)

// Privileges reports the offered privilege levels, weakest first.
func Privileges() []string {
	return []string{privilegeReadOnly, privilegeReadWrite, privilegeFull}
}

func validPrivilege(level string) bool {
	switch level {
	case privilegeReadOnly, privilegeReadWrite, privilegeFull:
		return true
	default:
		return false
	}
}

// resolveHost decides the host half of an account for this engine.
func resolveHost(engine agentclient.DatabaseEngine, requested string) (string, error) {
	if !engine.SupportsHostPatterns {
		if requested != "" {
			return "", ErrHostNotSupported
		}
		return "", nil
	}

	host := requested
	if host == "" {
		// localhost by default: an account reachable from anywhere is a
		// deliberate choice, never one made by leaving a field blank.
		host = "localhost"
	}
	if err := validate.DatabaseHostPattern(host); err != nil {
		return "", err
	}
	return host, nil
}

// truncateUser fits a database name into MySQL's 32-character account limit.
func truncateUser(name string) string {
	if len(name) <= validate.MaxDatabaseUserLength {
		return name
	}
	return name[:validate.MaxDatabaseUserLength]
}

// Translate maps a domain error to its HTTP shape.
func Translate(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return httpx.NotFound("Database not found")
	case errors.Is(err, ErrUserNotFound):
		return httpx.NotFound("Database user not found")
	case errors.Is(err, websites.ErrNotFound):
		return httpx.NotFound("Website not found")
	case errors.Is(err, ErrDuplicate), errors.Is(err, ErrDuplicateUser):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrDatabaseInUse), errors.Is(err, ErrUserExistsUnmanaged):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrEngineUnavailable), errors.Is(err, ErrNoServer):
		return httpx.Unavailable(err.Error())
	case errors.Is(err, ErrHostNotSupported), errors.Is(err, ErrInvalidPrivilege),
		errors.Is(err, validate.ErrInvalidDatabaseName),
		errors.Is(err, validate.ErrInvalidDatabaseUser),
		errors.Is(err, validate.ErrInvalidHostPattern),
		errors.Is(err, validate.ErrInvalidEngine):
		return httpx.ValidationFailed(err.Error())
	case agentclient.IsUnsupported(err):
		return httpx.Unavailable("This host does not have a database server the panel can manage")
	default:
		var failed *agentclient.ErrOperationFailed
		if errors.As(err, &failed) {
			// The server's own refusal — "Access denied", "database exists" —
			// is exactly what the operator needs to read, so it is passed
			// through rather than collapsed into a generic 500.
			return httpx.BadRequest(failed.Message)
		}
		return httpx.Internal(err)
	}
}
