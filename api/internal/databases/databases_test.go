package databases_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/databases"
	"github.com/jothost/panel/api/internal/secrets"
	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/api/internal/websites"
)

// fakeAgent stands in for the host.
//
// It records what it was asked to do so the tests can assert the panel's half
// of the contract — what reaches the server, and in what order — without
// needing a MariaDB running beside the suite. The live behaviour is covered by
// the Phase 8 integration script, which drives a real server.
type fakeAgent struct {
	engines []agentclient.DatabaseEngine
	// calls records operation names in order.
	calls []string
	// failCreate makes the host refuse a CREATE DATABASE.
	failCreate error
	failGrant  error
	// lastPassword is what the last user operation was asked to set.
	lastPassword string
	// issued is the password the fake reports back, standing in for one the
	// host generated.
	issued string
	// databases the fake believes exist.
	created map[string]bool
	dropped []string
	// existingUsers are accounts already on the server, which an idempotent
	// create leaves untouched — and therefore returns no password for.
	existingUsers map[string]bool
}

func newFakeAgent() *fakeAgent {
	return &fakeAgent{
		engines: []agentclient.DatabaseEngine{
			{Engine: "mariadb", Available: true, Version: "11.4.2-MariaDB", SupportsHostPatterns: true},
			{Engine: "postgres", Available: false, Detail: "no server of this engine was found on the host"},
		},
		issued:        "Generated-Password_1",
		created:       map[string]bool{},
		existingUsers: map[string]bool{},
	}
}

func (f *fakeAgent) DatabaseEngines(context.Context, string) (agentclient.DatabaseEnginesResult, error) {
	available := false
	for _, engine := range f.engines {
		if engine.Available {
			available = true
		}
	}
	return agentclient.DatabaseEnginesResult{Engines: f.engines, Available: available}, nil
}

func (f *fakeAgent) DatabaseCreate(_ context.Context, _, engine, name string) (agentclient.DatabaseCreateResult, error) {
	f.calls = append(f.calls, "create:"+engine+":"+name)
	if f.failCreate != nil {
		return agentclient.DatabaseCreateResult{}, f.failCreate
	}
	f.created[name] = true
	return agentclient.DatabaseCreateResult{Engine: engine, Name: name, Created: true, SizeBytes: 16384}, nil
}

func (f *fakeAgent) DatabaseDelete(_ context.Context, _, engine, name string) error {
	f.calls = append(f.calls, "drop:"+engine+":"+name)
	f.dropped = append(f.dropped, name)
	delete(f.created, name)
	return nil
}

func (f *fakeAgent) DatabaseSize(_ context.Context, _, engine, name string) (agentclient.DatabaseSizeResult, error) {
	return agentclient.DatabaseSizeResult{Engine: engine, Name: name, SizeBytes: 65536}, nil
}

func (f *fakeAgent) DatabaseList(_ context.Context, _, engine string) (agentclient.DatabaseListResult, error) {
	records := make([]agentclient.DatabaseRecord, 0, len(f.created))
	for name := range f.created {
		records = append(records, agentclient.DatabaseRecord{Name: name, Engine: engine})
	}
	return agentclient.DatabaseListResult{Engine: engine, Databases: records, Count: len(records)}, nil
}

func (f *fakeAgent) DatabaseUserCreate(_ context.Context, _ string,
	req agentclient.DatabaseUserRequest,
) (agentclient.DatabaseUserResult, error) {
	f.calls = append(f.calls, "user.create:"+req.Username+"@"+req.Host)
	if f.existingUsers[req.Username] {
		// An account already on the server keeps the password it had, so the
		// Agent reports that it created nothing and returns no password.
		return agentclient.DatabaseUserResult{
			Engine: req.Engine, Username: req.Username, Host: req.Host, Created: false,
		}, nil
	}

	password := req.Password
	if password == "" {
		password = f.issued
	}
	f.lastPassword = password
	f.existingUsers[req.Username] = true
	return agentclient.DatabaseUserResult{
		Engine: req.Engine, Username: req.Username, Host: req.Host,
		Password: password, Created: true,
	}, nil
}

func (f *fakeAgent) DatabaseUserPassword(_ context.Context, _ string,
	req agentclient.DatabaseUserRequest,
) (agentclient.DatabaseUserResult, error) {
	f.calls = append(f.calls, "user.password:"+req.Username)
	password := req.Password
	if password == "" {
		password = "Rotated-Password_2"
	}
	f.lastPassword = password
	return agentclient.DatabaseUserResult{
		Engine: req.Engine, Username: req.Username, Host: req.Host,
		Password: password, Changed: true,
	}, nil
}

func (f *fakeAgent) DatabaseUserDelete(_ context.Context, _, _, username, _ string) error {
	f.calls = append(f.calls, "user.drop:"+username)
	return nil
}

func (f *fakeAgent) DatabaseGrant(_ context.Context, _, _, username, _, database, privilege string) error {
	f.calls = append(f.calls, "grant:"+username+":"+database+":"+privilege)
	return f.failGrant
}

// ------------------------------------------------------------------ fixture

type fixture struct {
	service *databases.Service
	repo    *databases.Repository
	agent   *fakeAgent
	pool    *pgxpool.Pool
	server  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	deps := testsupport.Require(t)
	deps.Reset(t)

	encrypter, err := secrets.NewEncrypter(testsupport.TestEncryptionKey)
	if err != nil {
		t.Fatalf("NewEncrypter: %v", err)
	}

	serverID := registerServer(t, deps.Pool)
	repo := databases.NewRepository(deps.Pool, encrypter)
	agent := newFakeAgent()

	return &fixture{
		repo:   repo,
		agent:  agent,
		pool:   deps.Pool,
		server: serverID,
		service: databases.NewService(databases.ServiceOptions{
			Repository: repo,
			Websites:   websites.NewRepository(deps.Pool),
			Agent:      agent,
			ServerID:   serverID,
		}),
	}
}

func registerServer(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()

	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO servers (hostname, status)
		VALUES ('database-test-host', 'online')
		RETURNING id::text`).Scan(&id)
	if err != nil {
		t.Fatalf("register server: %v", err)
	}
	return id
}

// -------------------------------------------------------------------- tests

func TestCreateProvisionsDatabaseAndAccount(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	result, err := f.service.Create(ctx, databases.CreateRequest{
		Name: "shop", CreateUser: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if result.Database.Status != databases.StatusActive {
		t.Errorf("status = %q, want active", result.Database.Status)
	}
	if result.Database.Engine != "mariadb" {
		t.Errorf("engine = %q, want the host's only available engine", result.Database.Engine)
	}
	if result.User == nil {
		t.Fatal("no account was created")
	}
	if result.Password == "" {
		t.Fatal("no password was returned; the account would be unusable")
	}
	// The account has to exist before it can be granted anything, and the
	// grant has to happen or the account cannot reach the database.
	want := []string{
		"create:mariadb:shop",
		"user.create:shop@localhost",
		"grant:shop:shop:full",
	}
	if strings.Join(f.agent.calls, "|") != strings.Join(want, "|") {
		t.Errorf("agent calls = %v, want %v", f.agent.calls, want)
	}
}

// A password the server generated is the only copy in existence: the server
// keeps a hash. If the panel does not store it, nobody can ever be shown it.
func TestPasswordIsStoredEncryptedAndRecoverable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	result, err := f.service.Create(ctx, databases.CreateRequest{Name: "shop", CreateUser: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var stored string
	err = f.pool.QueryRow(ctx,
		`SELECT password_encrypted FROM database_users WHERE id = $1::uuid`,
		result.User.ID).Scan(&stored)
	if err != nil {
		t.Fatalf("read stored password: %v", err)
	}
	if stored == "" || strings.Contains(stored, result.Password) {
		t.Fatalf("password is not encrypted at rest: %q", stored)
	}

	revealed, err := f.service.RevealPassword(ctx, result.User.ID, databases.Actor{})
	if err != nil {
		t.Fatalf("RevealPassword: %v", err)
	}
	if revealed != result.Password {
		t.Fatalf("revealed %q, want %q", revealed, result.Password)
	}
}

// The ciphertext is bound to its own row. A copy taken from another account
// must fail to decrypt rather than hand out that account's password.
func TestAPasswordCopiedBetweenRowsWillNotDecrypt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	first, err := f.service.Create(ctx, databases.CreateRequest{Name: "shop", CreateUser: true})
	if err != nil {
		t.Fatalf("Create shop: %v", err)
	}
	second, err := f.service.Create(ctx, databases.CreateRequest{
		Name: "blog", CreateUser: true, Username: "blog",
	})
	if err != nil {
		t.Fatalf("Create blog: %v", err)
	}

	_, err = f.pool.Exec(ctx, `
		UPDATE database_users
		SET password_encrypted = (SELECT password_encrypted FROM database_users WHERE id = $2::uuid)
		WHERE id = $1::uuid`, second.User.ID, first.User.ID)
	if err != nil {
		t.Fatalf("copy ciphertext: %v", err)
	}

	if _, err := f.repo.RevealPassword(ctx, second.User.ID); err == nil {
		t.Fatal("a ciphertext copied from another row decrypted successfully")
	}
}

func TestCreateRefusesAnInvalidName(t *testing.T) {
	f := newFixture(t)

	for _, name := range []string{"", "shop; DROP DATABASE other", "mysql", "1shop", "shop-prod"} {
		if _, err := f.service.Create(context.Background(), databases.CreateRequest{Name: name}); err == nil {
			t.Errorf("Create(%q) = nil, want a refusal", name)
		}
	}
	if len(f.agent.calls) != 0 {
		t.Errorf("a rejected name still reached the host: %v", f.agent.calls)
	}
}

// A name is lowercased and trimmed before validation, the same way a domain
// is: "Shop" and "shop" are one database, and refusing the first would be
// pedantry rather than safety.
func TestCreateNormalisesTheName(t *testing.T) {
	f := newFixture(t)

	result, err := f.service.Create(context.Background(), databases.CreateRequest{Name: "  Shop  "})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if result.Database.Name != "shop" {
		t.Fatalf("name = %q, want shop", result.Database.Name)
	}
}

func TestCreateRefusesAnEngineTheHostDoesNotRun(t *testing.T) {
	f := newFixture(t)

	_, err := f.service.Create(context.Background(), databases.CreateRequest{
		Name: "shop", Engine: "postgres",
	})
	if !errors.Is(err, databases.ErrEngineUnavailable) {
		t.Fatalf("Create = %v, want ErrEngineUnavailable", err)
	}
}

// A PostgreSQL role is global. Accepting a host for one would record a
// restriction the server does not enforce.
func TestHostIsRefusedOnAnEngineWithoutHostPatterns(t *testing.T) {
	f := newFixture(t)
	f.agent.engines = []agentclient.DatabaseEngine{
		{Engine: "postgres", Available: true, Version: "16.3", SupportsHostPatterns: false},
	}

	_, err := f.service.Create(context.Background(), databases.CreateRequest{
		Name: "shop", CreateUser: true, Host: "localhost",
	})
	if !errors.Is(err, databases.ErrHostNotSupported) {
		t.Fatalf("Create = %v, want ErrHostNotSupported", err)
	}
}

func TestPostgresAccountsAreStoredWithoutAHost(t *testing.T) {
	f := newFixture(t)
	f.agent.engines = []agentclient.DatabaseEngine{
		{Engine: "postgres", Available: true, Version: "16.3", SupportsHostPatterns: false},
	}

	result, err := f.service.Create(context.Background(), databases.CreateRequest{
		Name: "shop", CreateUser: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if result.User.Host != "" {
		t.Fatalf("host = %q, want empty on postgres", result.User.Host)
	}
}

// A failure on the host must leave a visible record of what was attempted,
// not a row claiming a database that is not there.
func TestAFailedCreateLeavesTheRecordMarkedFailed(t *testing.T) {
	f := newFixture(t)
	f.agent.failCreate = errors.New("Access denied for user")

	if _, err := f.service.Create(context.Background(), databases.CreateRequest{Name: "shop"}); err == nil {
		t.Fatal("Create succeeded despite the host refusing")
	}

	records, err := f.repo.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(records) != 1 || records[0].Status != databases.StatusFailed {
		t.Fatalf("records = %+v, want one failed row", records)
	}
}

func TestDeleteDropsOnTheHostBeforeForgettingTheRow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	result, err := f.service.Create(ctx, databases.CreateRequest{Name: "shop", CreateUser: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := f.service.Delete(ctx, "req", result.Database.ID, databases.Actor{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(f.agent.dropped) != 1 || f.agent.dropped[0] != "shop" {
		t.Fatalf("dropped = %v, want [shop]", f.agent.dropped)
	}

	if _, err := f.repo.Get(ctx, result.Database.ID); !errors.Is(err, databases.ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
}

func TestSetPasswordOnlyStoresWhatTheServerAccepted(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	result, err := f.service.Create(ctx, databases.CreateRequest{Name: "shop", CreateUser: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	changed, err := f.service.SetPassword(ctx, "req", result.User.ID, "", databases.Actor{})
	if err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if changed == result.Password {
		t.Fatal("the password did not change")
	}

	revealed, err := f.repo.RevealPassword(ctx, result.User.ID)
	if err != nil {
		t.Fatalf("RevealPassword: %v", err)
	}
	if revealed != changed {
		t.Fatalf("stored %q, want the accepted password %q", revealed, changed)
	}
}

func TestGrantsAreRecordedAndRevocable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	result, err := f.service.Create(ctx, databases.CreateRequest{Name: "shop", CreateUser: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	users, err := f.repo.ListUsers(ctx, result.Database.ID)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 || users[0].Grants[0].Privilege != "full" {
		t.Fatalf("users = %+v, want one full grant", users)
	}

	if err := f.service.SetGrant(ctx, "req", result.Database.ID, result.User.ID,
		"readonly", databases.Actor{}); err != nil {
		t.Fatalf("SetGrant: %v", err)
	}
	users, _ = f.repo.ListUsers(ctx, result.Database.ID)
	if users[0].Grants[0].Privilege != "readonly" {
		t.Fatalf("privilege = %q, want readonly", users[0].Grants[0].Privilege)
	}

	// An empty privilege revokes, and the account must then disappear from the
	// database's user list rather than linger with no access.
	if err := f.service.SetGrant(ctx, "req", result.Database.ID, result.User.ID,
		"", databases.Actor{}); err != nil {
		t.Fatalf("SetGrant revoke: %v", err)
	}
	users, _ = f.repo.ListUsers(ctx, result.Database.ID)
	if len(users) != 0 {
		t.Fatalf("users after revoke = %+v, want none", users)
	}
}

func TestSetGrantRefusesAPrivilegeThePanelDoesNotOffer(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	result, err := f.service.Create(ctx, databases.CreateRequest{Name: "shop", CreateUser: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// SUPER and FILE are server-wide. A panel that forwards a privilege string
	// is a panel that can be asked to hand out either.
	for _, privilege := range []string{"SUPER", "ALL PRIVILEGES", "owner"} {
		err := f.service.SetGrant(ctx, "req", result.Database.ID, result.User.ID,
			privilege, databases.Actor{})
		if !errors.Is(err, databases.ErrInvalidPrivilege) {
			t.Errorf("SetGrant(%q) = %v, want ErrInvalidPrivilege", privilege, err)
		}
	}
}

func TestDuplicateNamesAreRefusedOnTheSameEngine(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.service.Create(ctx, databases.CreateRequest{Name: "shop"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err := f.service.Create(ctx, databases.CreateRequest{Name: "shop"})
	if !errors.Is(err, databases.ErrDuplicate) {
		t.Fatalf("second Create = %v, want ErrDuplicate", err)
	}
}

func TestRefreshSizeRecordsWhatTheHostReported(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	result, err := f.service.Create(ctx, databases.CreateRequest{Name: "shop"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	refreshed, err := f.service.RefreshSize(ctx, "req", result.Database.ID)
	if err != nil {
		t.Fatalf("RefreshSize: %v", err)
	}
	if refreshed.SizeBytes == nil || *refreshed.SizeBytes != 65536 {
		t.Fatalf("size = %v, want 65536", refreshed.SizeBytes)
	}
	if refreshed.SizeCheckedAt == nil {
		t.Fatal("size_checked_at was not set; the panel cannot tell a stale size from a fresh one")
	}
}

// Deleting a website must not take its database with it: the data outlives the
// vhost, and dropping it has to be a separate, deliberate decision.
func TestDeletingAWebsiteLeavesItsDatabaseBehind(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	var websiteID string
	err := f.pool.QueryRow(ctx, `
		INSERT INTO websites (server_id, name, primary_domain, document_root, system_username, status)
		VALUES ($1::uuid, 'shop', 'shop.example', '/var/www/shop.example/public', 'web_shop', 'active')
		RETURNING id::text`, f.server).Scan(&websiteID)
	if err != nil {
		t.Fatalf("insert website: %v", err)
	}

	result, err := f.service.Create(ctx, databases.CreateRequest{
		Name: "shop", WebsiteID: websiteID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := f.pool.Exec(ctx, `DELETE FROM websites WHERE id = $1::uuid`, websiteID); err != nil {
		t.Fatalf("delete website: %v", err)
	}

	record, err := f.repo.Get(ctx, result.Database.ID)
	if err != nil {
		t.Fatalf("the database went with the website: %v", err)
	}
	if record.WebsiteID != nil {
		t.Errorf("website_id = %v, want nil after the website was deleted", record.WebsiteID)
	}
}

// An account already on the server keeps the password it had. The panel must
// not record — or hand back — the password it generated but never applied:
// that produces a credential that looks right and silently does not work.
func TestAnUnmanagedAccountThatAlreadyExistsIsRefused(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Something outside the panel made this account.
	f.agent.existingUsers["shop"] = true

	_, err := f.service.Create(ctx, databases.CreateRequest{Name: "shop", CreateUser: true})
	if !errors.Is(err, databases.ErrUserExistsUnmanaged) {
		t.Fatalf("Create = %v, want ErrUserExistsUnmanaged", err)
	}

	// Nothing was written for an account whose password nobody knows.
	var count int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM database_users WHERE username = 'shop'`).Scan(&count); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 0 {
		t.Fatalf("wrote %d rows for an account with an unknown password, want 0", count)
	}
}

// The same account reaching the panel twice is different: the panel already
// holds its password, so the second call reuses the record and only adds the
// grant, rather than claiming a password it did not set.
func TestAddingAKnownAccountToASecondDatabaseReusesIt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	first, err := f.service.Create(ctx, databases.CreateRequest{
		Name: "shop", CreateUser: true, Username: "app",
	})
	if err != nil {
		t.Fatalf("Create shop: %v", err)
	}

	second, err := f.service.Create(ctx, databases.CreateRequest{Name: "blog"})
	if err != nil {
		t.Fatalf("Create blog: %v", err)
	}

	user, password, err := f.service.AddUser(ctx, databases.AddUserRequest{
		DatabaseID: second.Database.ID, Username: "app", Privilege: "readonly",
	})
	if err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if user.ID != first.User.ID {
		t.Errorf("a second account row was created: %s then %s", first.User.ID, user.ID)
	}
	// No password is returned, because this call did not set one. The stored
	// one is still correct and is read through the audited endpoint.
	if password != "" {
		t.Errorf("password = %q, want empty for an account that already existed", password)
	}

	grants, err := f.repo.GrantsForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GrantsForUser: %v", err)
	}
	if len(grants) != 2 {
		t.Fatalf("grants = %+v, want one per database", grants)
	}
}
