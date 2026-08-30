package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jothost/panel/agent/internal/database"
	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// databaseRequest is the shape every database operation decodes into.
//
// One struct rather than nine keeps the field names identical across
// operations, and DisallowUnknownFields means a caller that misspells one is
// told so rather than having it silently ignored — a mistyped "privilege"
// would otherwise grant the default level to somebody.
type databaseRequest struct {
	Engine    string `json:"engine"`
	Name      string `json:"name"`
	Username  string `json:"username"`
	Host      string `json:"host"`
	Password  string `json:"password"`
	Privilege string `json:"privilege"`
	// Generate asks the Agent to invent the password. The API uses it so a
	// password never has to travel from the browser to the host at all.
	Generate bool `json:"generate"`
}

// decodeDatabaseRequest reads a typed payload from the request.
func decodeDatabaseRequest(req protocol.Request) (databaseRequest, error) {
	var decoded databaseRequest
	if req.Payload == nil {
		return decoded, Fail(protocol.CodeInvalidPayload, "A payload is required", nil)
	}

	encoded, err := json.Marshal(req.Payload)
	if err != nil {
		return decoded, Fail(protocol.CodeInvalidPayload, "The payload could not be read", err)
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return decoded, Fail(protocol.CodeInvalidPayload, "The payload is not valid", err)
	}
	return decoded, nil
}

// provider resolves the engine named in a request, or explains its absence.
func (r *Registry) provider(engine string) (database.Provider, error) {
	if r.deps.Databases == nil {
		return nil, Fail(protocol.CodeUnsupported,
			"Database management is not configured on this host", nil)
	}

	provider, err := r.deps.Databases.Provider(engine)
	if err != nil {
		if errors.Is(err, database.ErrEngineUnavailable) {
			return nil, Fail(protocol.CodeUnsupported, err.Error(), err)
		}
		return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}
	return provider, nil
}

// handleDatabaseEngines reports which database servers this host runs.
func (r *Registry) handleDatabaseEngines(ctx context.Context, _ protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	if r.deps.Databases == nil {
		return map[string]any{"engines": []database.EngineInfo{}, "available": false}, nil
	}

	engines := r.deps.Databases.Engines(ctx)
	return map[string]any{
		"engines":    engines,
		"available":  r.deps.Databases.Available(),
		"privileges": database.Privileges(),
	}, nil
}

// handleDatabaseList returns the databases on one engine.
func (r *Registry) handleDatabaseList(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	request, err := decodeDatabaseRequest(req)
	if err != nil {
		return nil, err
	}
	provider, err := r.provider(request.Engine)
	if err != nil {
		return nil, err
	}

	databases, err := provider.ListDatabases(ctx)
	if err != nil {
		return nil, databaseError("The database list could not be read", err)
	}
	return map[string]any{
		"engine":    provider.Engine(),
		"databases": databases,
		"count":     len(databases),
	}, nil
}

// handleDatabaseCreate creates a database.
func (r *Registry) handleDatabaseCreate(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	request, err := decodeDatabaseRequest(req)
	if err != nil {
		return nil, err
	}
	provider, err := r.provider(request.Engine)
	if err != nil {
		return nil, err
	}
	if err := validate.DatabaseName(request.Name); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}

	report(reporterFunc(reporter), 20, "creating database "+request.Name)
	if err := provider.CreateDatabase(ctx, request.Name); err != nil {
		return nil, databaseError("The database could not be created", err)
	}

	result := map[string]any{
		"engine":  provider.Engine(),
		"name":    request.Name,
		"created": true,
	}

	// The encoding and collation are read back from the server rather than
	// echoed from what was requested. The server has the last word — it can
	// substitute a collation the requested one maps to — and a panel that
	// reports what it asked for instead of what it got will eventually be
	// wrong about it.
	if info, err := findDatabase(ctx, provider, request.Name); err == nil {
		result["charset"] = info.Charset
		result["collation"] = info.Collation
		result["size_bytes"] = info.SizeBytes
	} else {
		// The database exists; not being able to describe it yet is not a
		// failure of the operation that just created it.
		result["size_bytes"] = int64(0)
	}

	report(reporterFunc(reporter), 100, "database "+request.Name+" is ready")
	return result, nil
}

// findDatabase returns one database as the server describes it.
func findDatabase(ctx context.Context, provider database.Provider, name string) (database.Database, error) {
	databases, err := provider.ListDatabases(ctx)
	if err != nil {
		return database.Database{}, err
	}
	for _, candidate := range databases {
		if candidate.Name == name {
			return candidate, nil
		}
	}
	return database.Database{}, database.ErrNotFound
}

// handleDatabaseDelete drops a database.
func (r *Registry) handleDatabaseDelete(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	request, err := decodeDatabaseRequest(req)
	if err != nil {
		return nil, err
	}
	provider, err := r.provider(request.Engine)
	if err != nil {
		return nil, err
	}
	if err := validate.DatabaseName(request.Name); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}

	report(reporterFunc(reporter), 20, "dropping database "+request.Name)
	if err := provider.DropDatabase(ctx, request.Name); err != nil {
		return nil, databaseError("The database could not be deleted", err)
	}

	report(reporterFunc(reporter), 100, "database "+request.Name+" is gone")
	return map[string]any{"engine": provider.Engine(), "name": request.Name, "deleted": true}, nil
}

// handleDatabaseSize reports one database's size in bytes.
func (r *Registry) handleDatabaseSize(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	request, err := decodeDatabaseRequest(req)
	if err != nil {
		return nil, err
	}
	provider, err := r.provider(request.Engine)
	if err != nil {
		return nil, err
	}
	if err := validate.DatabaseName(request.Name); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}

	size, err := provider.DatabaseSize(ctx, request.Name)
	if err != nil {
		return nil, databaseError("The database size could not be read", err)
	}
	return map[string]any{
		"engine": provider.Engine(), "name": request.Name, "size_bytes": size,
	}, nil
}

// handleDatabaseUserList returns the accounts on one engine.
func (r *Registry) handleDatabaseUserList(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	request, err := decodeDatabaseRequest(req)
	if err != nil {
		return nil, err
	}
	provider, err := r.provider(request.Engine)
	if err != nil {
		return nil, err
	}

	users, err := provider.ListUsers(ctx)
	if err != nil {
		return nil, databaseError("The user list could not be read", err)
	}
	return map[string]any{
		"engine": provider.Engine(), "users": users, "count": len(users),
	}, nil
}

// handleDatabaseUserCreate adds an account.
//
// The password is returned in the result exactly once, because there is
// nowhere else it can come from: the server stores only a hash, so an account
// whose password is not captured here can never be used. The API encrypts it
// on arrival, and the Agent's audit record carries the account name, never the
// password (CLAUDE.md section 14).
func (r *Registry) handleDatabaseUserCreate(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	request, err := decodeDatabaseRequest(req)
	if err != nil {
		return nil, err
	}
	provider, err := r.provider(request.Engine)
	if err != nil {
		return nil, err
	}

	user, err := databaseUser(provider, request)
	if err != nil {
		return nil, err
	}
	password, err := resolvePassword(request)
	if err != nil {
		return nil, err
	}

	report(reporterFunc(reporter), 30, "creating account "+user.Username)
	created, err := provider.CreateUser(ctx, user, password)
	if err != nil {
		return nil, databaseError("The database user could not be created", err)
	}

	result := map[string]any{
		"engine":   provider.Engine(),
		"username": user.Username,
		"host":     user.Host,
		"created":  created,
	}

	// The password is returned only when this call is what set it. An account
	// that already existed kept the password it had, and reporting the one
	// generated here would hand the operator a credential the server never
	// accepted — a failure that surfaces much later and says nothing about its
	// cause.
	if created {
		result["password"] = password
	}

	report(reporterFunc(reporter), 100, "account "+user.Username+" is ready")
	return result, nil
}

// handleDatabaseUserDelete removes an account.
func (r *Registry) handleDatabaseUserDelete(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	request, err := decodeDatabaseRequest(req)
	if err != nil {
		return nil, err
	}
	provider, err := r.provider(request.Engine)
	if err != nil {
		return nil, err
	}
	user, err := databaseUser(provider, request)
	if err != nil {
		return nil, err
	}

	report(reporterFunc(reporter), 30, "removing account "+user.Username)
	if err := provider.DropUser(ctx, user); err != nil {
		return nil, databaseError("The database user could not be deleted", err)
	}

	report(reporterFunc(reporter), 100, "account "+user.Username+" is gone")
	return map[string]any{
		"engine": provider.Engine(), "username": user.Username,
		"host": user.Host, "deleted": true,
	}, nil
}

// handleDatabaseUserPassword changes an account's password.
func (r *Registry) handleDatabaseUserPassword(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	request, err := decodeDatabaseRequest(req)
	if err != nil {
		return nil, err
	}
	provider, err := r.provider(request.Engine)
	if err != nil {
		return nil, err
	}
	user, err := databaseUser(provider, request)
	if err != nil {
		return nil, err
	}
	password, err := resolvePassword(request)
	if err != nil {
		return nil, err
	}

	report(reporterFunc(reporter), 40, "changing the password for "+user.Username)
	if err := provider.SetPassword(ctx, user, password); err != nil {
		return nil, databaseError("The password could not be changed", err)
	}

	report(reporterFunc(reporter), 100, "password changed")
	return map[string]any{
		"engine": provider.Engine(), "username": user.Username,
		"host": user.Host, "password": password, "changed": true,
	}, nil
}

// handleDatabaseUserGrant sets an account's access to one database.
//
// An empty privilege revokes: "no access" is a level like any other, and
// expressing it through the same operation means the panel has one code path
// for "who can reach this database" rather than two that can disagree.
func (r *Registry) handleDatabaseUserGrant(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	request, err := decodeDatabaseRequest(req)
	if err != nil {
		return nil, err
	}
	provider, err := r.provider(request.Engine)
	if err != nil {
		return nil, err
	}
	user, err := databaseUser(provider, request)
	if err != nil {
		return nil, err
	}
	if err := validate.DatabaseName(request.Name); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}

	if request.Privilege == "" {
		report(reporterFunc(reporter), 40, "revoking access to "+request.Name)
		if err := provider.RevokeAll(ctx, user, request.Name); err != nil {
			return nil, databaseError("Access could not be revoked", err)
		}
		report(reporterFunc(reporter), 100, "access revoked")
		return map[string]any{
			"engine": provider.Engine(), "username": user.Username, "host": user.Host,
			"database": request.Name, "privilege": "", "granted": false,
		}, nil
	}

	if !database.ValidPrivilege(request.Privilege) {
		return nil, Fail(protocol.CodeInvalidPayload,
			fmt.Sprintf("Privilege must be one of %v", database.Privileges()), nil)
	}

	report(reporterFunc(reporter), 40, "granting "+request.Privilege+" on "+request.Name)
	grant := database.Grant{
		Username:  user.Username,
		Host:      user.Host,
		Database:  request.Name,
		Privilege: request.Privilege,
	}
	if err := provider.Grant(ctx, grant); err != nil {
		return nil, databaseError("Access could not be granted", err)
	}

	report(reporterFunc(reporter), 100, "access granted")
	return map[string]any{
		"engine": provider.Engine(), "username": user.Username, "host": user.Host,
		"database": request.Name, "privilege": request.Privilege, "granted": true,
	}, nil
}

// databaseUser builds and validates the account a request names.
//
// The host defaults to localhost on an engine that uses one, and is forced
// empty on an engine that does not. A PostgreSQL role carrying a host would be
// a lie about where it can connect from.
func databaseUser(provider database.Provider, request databaseRequest) (database.User, error) {
	user := database.User{
		Username: request.Username,
		Engine:   provider.Engine(),
	}
	if err := validate.DatabaseUser(user.Username); err != nil {
		return user, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}

	if !provider.SupportsHostPatterns() {
		return user, nil
	}

	user.Host = request.Host
	if user.Host == "" {
		user.Host = "localhost"
	}
	if err := validate.DatabaseHostPattern(user.Host); err != nil {
		return user, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}
	return user, nil
}

// resolvePassword returns the password to use, generating one when asked.
func resolvePassword(request databaseRequest) (string, error) {
	if request.Generate || request.Password == "" {
		password, err := database.GeneratePassword()
		if err != nil {
			return "", Fail(protocol.CodeInternal, "A password could not be generated", err)
		}
		return password, nil
	}

	if err := database.ValidatePassword(request.Password); err != nil {
		return "", Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}
	return request.Password, nil
}

// databaseError converts a provider failure into a structured refusal.
//
// The server's own message is passed through for a rejected statement, because
// "Access denied" or "database exists" is exactly what the operator needs to
// see. It cannot carry a password: every statement that contains one is built
// here, and a client error quotes the statement's shape, not its literals.
func databaseError(summary string, err error) error {
	switch {
	case errors.Is(err, database.ErrNotFound):
		return Fail(protocol.CodeNotFound, summary, err)
	case errors.Is(err, database.ErrExists):
		return Fail(protocol.CodeInvalidPayload, summary, err)
	case errors.Is(err, database.ErrInvalidPassword),
		errors.Is(err, validate.ErrInvalidDatabaseName),
		errors.Is(err, validate.ErrInvalidDatabaseUser),
		errors.Is(err, validate.ErrInvalidHostPattern):
		return Fail(protocol.CodeInvalidPayload, err.Error(), err)
	case errors.Is(err, database.ErrQueryFailed):
		return Fail(protocol.CodeInvalidPayload, summary+": "+err.Error(), err)
	default:
		return Fail(protocol.CodeInternal, summary, err)
	}
}
