package node_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/node"
	"github.com/jothost/panel/api/internal/secrets"
	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/api/internal/websites"
)

// fakeAgent stands in for the host.
//
// It records what it was asked to do, so the tests can assert the panel's half
// of the contract — what reaches the host, and in what order — without needing
// a Node.js runtime beside the suite. The live behaviour is covered by the
// Phase 9 integration script, which runs a real Express application.
type fakeAgent struct {
	calls []string
	// versions the fake host has installed.
	versions []agentclient.NodeVersion
	// listening is what a started application reports.
	listening bool
	// failStart makes the host refuse to start an application.
	failStart error
	// lastEnv is the environment of the most recent deploy.
	lastEnv map[string]string
	// websitePorts records what the vhost was pointed at, in order.
	websitePorts []int
}

func newFakeAgent() *fakeAgent {
	return &fakeAgent{
		versions:  []agentclient.NodeVersion{{Version: "22", Full: "22.11.0", BinaryPath: "/usr/bin/node"}},
		listening: true,
	}
}

func (f *fakeAgent) NodeVersions(context.Context, string) (agentclient.NodeVersionsResult, error) {
	return agentclient.NodeVersionsResult{
		Versions:  f.versions,
		Count:     len(f.versions),
		Available: len(f.versions) > 0,
		Offers:    []agentclient.NodeOffer{{Package: "nodejs", Label: "Long-term support"}},
		ManagedBy: "agent",
	}, nil
}

func (f *fakeAgent) NodeDeploy(_ context.Context, _ string, req agentclient.NodeAppRequest) error {
	f.calls = append(f.calls, "deploy:"+req.Name)
	f.lastEnv = req.Environment
	return nil
}

func (f *fakeAgent) NodeRemove(_ context.Context, _ string, req agentclient.NodeAppRequest) error {
	f.calls = append(f.calls, "remove:"+req.Name)
	return nil
}

func (f *fakeAgent) NodeStart(_ context.Context, _ string, req agentclient.NodeAppRequest) (agentclient.NodeStatus, error) {
	f.calls = append(f.calls, "start:"+req.Name)
	if f.failStart != nil {
		return agentclient.NodeStatus{}, f.failStart
	}
	return agentclient.NodeStatus{
		Name: req.Name, State: "running", PID: 4242,
		Port: req.Port, Listening: f.listening, ManagedBy: "agent",
	}, nil
}

func (f *fakeAgent) NodeStop(_ context.Context, _ string, req agentclient.NodeAppRequest) (agentclient.NodeStatus, error) {
	f.calls = append(f.calls, "stop:"+req.Name)
	return agentclient.NodeStatus{Name: req.Name, State: "stopped", Port: req.Port}, nil
}

func (f *fakeAgent) NodeRestart(_ context.Context, _ string, req agentclient.NodeAppRequest) (agentclient.NodeStatus, error) {
	f.calls = append(f.calls, "restart:"+req.Name)
	return agentclient.NodeStatus{
		Name: req.Name, State: "running", PID: 4343,
		Port: req.Port, Listening: f.listening,
	}, nil
}

func (f *fakeAgent) NodeStatusOf(_ context.Context, _ string, req agentclient.NodeAppRequest) (agentclient.NodeStatus, error) {
	return agentclient.NodeStatus{Name: req.Name, State: "stopped", Port: req.Port}, nil
}

func (f *fakeAgent) NodeAppLogs(context.Context, string, agentclient.NodeAppRequest, int) (agentclient.NodeLogs, error) {
	return agentclient.NodeLogs{Source: "files", Lines: []string{"listening"}}, nil
}

func (f *fakeAgent) NodeInstallDependencies(_ context.Context, _ string, req agentclient.NodeAppRequest) error {
	f.calls = append(f.calls, "npm:"+req.Name)
	return nil
}

func (f *fakeAgent) UpdateWebsite(_ context.Context, _ string, payload map[string]any) error {
	port, _ := payload["proxy_port"].(int)
	f.calls = append(f.calls, "vhost:"+itoa(port))
	f.websitePorts = append(f.websitePorts, port)
	return nil
}

func itoa(value int) string {
	if value == 0 {
		return "files"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// ------------------------------------------------------------------ fixture

type fixture struct {
	service *node.Service
	repo    *node.Repository
	agent   *fakeAgent
	pool    *pgxpool.Pool
	server  string
	website string
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
	websiteID := registerWebsite(t, deps.Pool, serverID)

	repo := node.NewRepository(deps.Pool, encrypter)
	agent := newFakeAgent()

	return &fixture{
		repo:    repo,
		agent:   agent,
		pool:    deps.Pool,
		server:  serverID,
		website: websiteID,
		service: node.NewService(node.ServiceOptions{
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
		INSERT INTO servers (hostname, status) VALUES ('node-test-host', 'online')
		RETURNING id::text`).Scan(&id)
	if err != nil {
		t.Fatalf("register server: %v", err)
	}
	return id
}

func registerWebsite(t *testing.T, pool *pgxpool.Pool, serverID string) string {
	t.Helper()

	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO websites (server_id, primary_domain, document_root, system_username, status)
		VALUES ($1::uuid, 'app.example', '/var/www/app.example/public', 'web_app', 'active')
		RETURNING id::text`, serverID).Scan(&id)
	if err != nil {
		t.Fatalf("register website: %v", err)
	}
	return id
}

// -------------------------------------------------------------------- tests

func TestCreatePreparesTheApplicationWithoutStartingIt(t *testing.T) {
	f := newFixture(t)

	app, err := f.service.Create(context.Background(), node.CreateRequest{
		WebsiteID: f.website, Port: 3000,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if app.Status != node.StatusStopped {
		t.Errorf("status = %q, want stopped: creating and starting are separate", app.Status)
	}
	if app.Port != 3000 {
		t.Errorf("port = %d", app.Port)
	}
	// The name is derived from the domain, and must be a valid unit stem.
	if !strings.HasPrefix(app.Name, "app-example") {
		t.Errorf("name = %q, want one derived from the domain", app.Name)
	}
	if !contains(f.agent.calls, "deploy:"+app.Name) {
		t.Errorf("calls = %v, want a deploy", f.agent.calls)
	}
	// Nothing was pointed at it: it is not running.
	if len(f.agent.websitePorts) != 0 {
		t.Errorf("the vhost was changed before the application ran: %v", f.agent.websitePorts)
	}
}

// The site was serving something before this was asked for. Pointing nginx at
// a port nothing is listening on would take it down between the two steps.
func TestStartPointsTheWebsiteAtTheApplicationOnlyOnceItListens(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	app, err := f.service.Create(ctx, node.CreateRequest{WebsiteID: f.website, Port: 3000})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := f.service.Start(ctx, "req", app.ID, node.Actor{}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if len(f.agent.websitePorts) != 1 || f.agent.websitePorts[0] != 3000 {
		t.Fatalf("vhost ports = %v, want the application's port once", f.agent.websitePorts)
	}
	// The order matters: start, then repoint.
	startAt, vhostAt := indexOf(f.agent.calls, "start:"+app.Name), indexOf(f.agent.calls, "vhost:3000")
	if startAt < 0 || vhostAt < 0 || vhostAt < startAt {
		t.Fatalf("calls = %v, want the vhost changed after the start", f.agent.calls)
	}
}

func TestStartLeavesTheWebsiteAloneWhenNothingIsListening(t *testing.T) {
	f := newFixture(t)
	f.agent.listening = false
	ctx := context.Background()

	app, err := f.service.Create(ctx, node.CreateRequest{WebsiteID: f.website, Port: 3000})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.service.Start(ctx, "req", app.ID, node.Actor{}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Replacing a working site with a 502 is worse than leaving it as it was.
	if len(f.agent.websitePorts) != 0 {
		t.Fatalf("the vhost was pointed at an application that was not listening: %v",
			f.agent.websitePorts)
	}
}

// A deliberate stop must not look like an outage.
func TestStopReturnsTheWebsiteToFilesBeforeStopping(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	app, err := f.service.Create(ctx, node.CreateRequest{WebsiteID: f.website, Port: 3000})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.service.Start(ctx, "req", app.ID, node.Actor{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	f.agent.calls = nil

	if _, err := f.service.Stop(ctx, "req", app.ID, node.Actor{}); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	vhostAt, stopAt := indexOf(f.agent.calls, "vhost:files"), indexOf(f.agent.calls, "stop:"+app.Name)
	if vhostAt < 0 || stopAt < 0 || vhostAt > stopAt {
		t.Fatalf("calls = %v, want the vhost returned to files before the stop", f.agent.calls)
	}
}

func TestOneApplicationPerWebsite(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.service.Create(ctx, node.CreateRequest{WebsiteID: f.website, Port: 3000}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err := f.service.Create(ctx, node.CreateRequest{
		WebsiteID: f.website, Name: "other", Port: 3001,
	})
	if !errors.Is(err, node.ErrDuplicateWebsite) {
		t.Fatalf("second Create = %v, want ErrDuplicateWebsite", err)
	}
}

// Two applications on one port means one of them is failing to bind, and the
// panel would not know which.
func TestOnePortPerServer(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	second := registerSecondWebsite(t, f.pool, f.server)

	if _, err := f.service.Create(ctx, node.CreateRequest{WebsiteID: f.website, Port: 3000}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err := f.service.Create(ctx, node.CreateRequest{WebsiteID: second, Port: 3000})
	if !errors.Is(err, node.ErrPortTaken) {
		t.Fatalf("second Create = %v, want ErrPortTaken", err)
	}
}

func TestCreateRefusesAPortTheHostCannotUse(t *testing.T) {
	f := newFixture(t)

	// Privileged, and in the ephemeral range: neither is a port an
	// unprivileged application can reliably listen on.
	for _, port := range []int{80, 1023, 40000} {
		_, err := f.service.Create(context.Background(), node.CreateRequest{
			WebsiteID: f.website, Port: port,
		})
		if err == nil {
			t.Errorf("Create accepted port %d", port)
		}
	}
}

// A site is served by an application or by files. Allowing both would write a
// vhost that sends some URLs to PHP and the rest to Node.
func TestCreateRefusesASiteThatServesPHP(t *testing.T) {
	f := newFixture(t)

	_, err := f.pool.Exec(context.Background(),
		`UPDATE websites SET php_version = '8.3' WHERE id = $1::uuid`, f.website)
	if err != nil {
		t.Fatalf("set php: %v", err)
	}

	_, err = f.service.Create(context.Background(), node.CreateRequest{
		WebsiteID: f.website, Port: 3000,
	})
	if !errors.Is(err, node.ErrWebsiteHasPHP) {
		t.Fatalf("Create = %v, want ErrWebsiteHasPHP", err)
	}
}

// An application the panel lists but never prepared is worse than one that was
// not created.
func TestAFailedDeployLeavesNoRecord(t *testing.T) {
	f := newFixture(t)
	f.agent.failStart = errors.New("unused")

	// Make the deploy itself fail by removing the runtime.
	f.agent.versions = nil

	_, err := f.service.Create(context.Background(), node.CreateRequest{
		WebsiteID: f.website, Port: 3000,
	})
	if err == nil {
		t.Fatal("Create succeeded with no runtime installed")
	}

	apps, listErr := f.repo.List(context.Background())
	if listErr != nil {
		t.Fatalf("List: %v", listErr)
	}
	if len(apps) != 0 {
		t.Fatalf("records = %+v, want none", apps)
	}
}

// ------------------------------------------------------------- environment

func TestEnvironmentIsEncryptedAtRestAndRecoverable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	app, err := f.service.Create(ctx, node.CreateRequest{WebsiteID: f.website, Port: 3000})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const secret = "postgres://app:s3cret@localhost/app"
	if err := f.service.SetEnv(ctx, "req", app.ID, "DATABASE_URL", secret, node.Actor{}); err != nil {
		t.Fatalf("SetEnv: %v", err)
	}

	var stored string
	err = f.pool.QueryRow(ctx,
		`SELECT value_encrypted FROM node_environment WHERE node_app_id = $1::uuid AND key = 'DATABASE_URL'`,
		app.ID).Scan(&stored)
	if err != nil {
		t.Fatalf("read stored value: %v", err)
	}
	if strings.Contains(stored, "s3cret") {
		t.Fatalf("the value is not encrypted at rest: %q", stored)
	}

	env, err := f.repo.Environment(ctx, app.ID)
	if err != nil {
		t.Fatalf("Environment: %v", err)
	}
	if env["DATABASE_URL"] != secret {
		t.Fatalf("decrypted %q, want %q", env["DATABASE_URL"], secret)
	}

	// And it reached the host.
	if f.agent.lastEnv["DATABASE_URL"] != secret {
		t.Errorf("the host was not given the new value: %v", f.agent.lastEnv)
	}
}

// A listing must say what is configured without saying what it is configured
// to: the values are credentials.
func TestListingReportsKeysNotValues(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	app, err := f.service.Create(ctx, node.CreateRequest{WebsiteID: f.website, Port: 3000})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.service.SetEnv(ctx, "req", app.ID, "API_KEY", "sk-live-1234", node.Actor{}); err != nil {
		t.Fatalf("SetEnv: %v", err)
	}

	loaded, err := f.service.Get(ctx, "req", app.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(loaded.Environment) != 1 || loaded.Environment[0] != "API_KEY" {
		t.Fatalf("environment = %v, want just the key", loaded.Environment)
	}
}

func TestSetEnvRefusesTheNamesThatChangeWhatRuns(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	app, err := f.service.Create(ctx, node.CreateRequest{WebsiteID: f.website, Port: 3000})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, key := range []string{"LD_PRELOAD", "NODE_OPTIONS", "PATH", "PORT"} {
		if err := f.service.SetEnv(ctx, "req", app.ID, key, "x", node.Actor{}); err == nil {
			t.Errorf("SetEnv accepted %s", key)
		}
	}
}

func TestEnvironmentValueCannotCloseALineInAUnitFile(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	app, err := f.service.Create(ctx, node.CreateRequest{WebsiteID: f.website, Port: 3000})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	err = f.service.SetEnv(ctx, "req", app.ID, "CONFIG",
		"value\nExecStart=/bin/sh -c 'id'", node.Actor{})
	if err == nil {
		t.Fatal("SetEnv accepted a value containing a newline")
	}
}

// ------------------------------------------------------------------ helpers

func registerSecondWebsite(t *testing.T, pool *pgxpool.Pool, serverID string) string {
	t.Helper()

	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO websites (server_id, primary_domain, document_root, system_username, status)
		VALUES ($1::uuid, 'other.example', '/var/www/other.example/public', 'web_other', 'active')
		RETURNING id::text`, serverID).Scan(&id)
	if err != nil {
		t.Fatalf("register website: %v", err)
	}
	return id
}

func registerThirdWebsite(t *testing.T, pool *pgxpool.Pool, serverID string) string {
	t.Helper()

	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO websites (server_id, primary_domain, document_root, system_username, status)
		VALUES ($1::uuid, 'third.example', '/var/www/third.example/public', 'web_third', 'active')
		RETURNING id::text`, serverID).Scan(&id)
	if err != nil {
		t.Fatalf("register website: %v", err)
	}
	return id
}

func contains(haystack []string, needle string) bool {
	return indexOf(haystack, needle) >= 0
}

func indexOf(haystack []string, needle string) int {
	for i, value := range haystack {
		if value == needle {
			return i
		}
	}
	return -1
}

func (f *fakeAgent) NodeInstall(_ context.Context, _, pkg string) (agentclient.NodeVersion, error) {
	f.calls = append(f.calls, "install:"+pkg)
	version := agentclient.NodeVersion{Version: "22", Full: "22.11.0", BinaryPath: "/usr/bin/node"}
	f.versions = []agentclient.NodeVersion{version}
	return version, nil
}

func (f *fakeAgent) NodeUninstall(_ context.Context, _, pkg string) error {
	f.calls = append(f.calls, "uninstall:"+pkg)
	f.versions = nil
	return nil
}

// The Agent keeps the only table of package names. A request naming something
// else must never reach a package manager running as root.
func TestInstallRuntimeRefusesAPackageTheHostDidNotOffer(t *testing.T) {
	f := newFixture(t)

	_, err := f.service.InstallRuntime(context.Background(), "req", "nodejs; rm -rf /", node.Actor{})
	if !errors.Is(err, node.ErrRuntimeUnavailable) {
		t.Fatalf("InstallRuntime = %v, want ErrRuntimeUnavailable", err)
	}
	if contains(f.agent.calls, "install:nodejs; rm -rf /") {
		t.Fatal("a package the host did not offer reached the Agent")
	}
}

// Removing the runtime under a running application would stop it at its next
// restart, which is not a consequence anybody asked for.
func TestRemoveRuntimeIsRefusedWhileAnApplicationUsesIt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.service.Create(ctx, node.CreateRequest{WebsiteID: f.website, Port: 3000}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	err := f.service.RemoveRuntime(ctx, "req", "nodejs", node.Actor{})
	if !errors.Is(err, node.ErrRuntimeInUse) {
		t.Fatalf("RemoveRuntime = %v, want ErrRuntimeInUse", err)
	}
}

// A backend port is a host-wide resource. Apache's range sits inside the range
// an application may ask for, and two tables cannot be constrained against each
// other in SQL without a trigger on both — so the rule lives in Go, and this is
// what proves it is actually applied.
func TestCreateRefusesAPortHeldByTheApacheBackend(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	websiteRepo := websites.NewRepository(f.pool)
	site, err := websiteRepo.Get(ctx, f.website)
	if err != nil {
		t.Fatalf("get website: %v", err)
	}

	port, err := websiteRepo.AssignBackendPort(ctx, site)
	if err != nil {
		t.Fatalf("assign backend port: %v", err)
	}

	second := registerSecondWebsite(t, f.pool, f.server)
	_, err = f.service.Create(ctx, node.CreateRequest{WebsiteID: second, Port: port})
	if !errors.Is(err, node.ErrPortTaken) {
		t.Fatalf("Create on the Apache backend port = %v, want ErrPortTaken", err)
	}
	// The message names what holds it: "port in use" on a host the user
	// believes is idle is not an answer.
	if !strings.Contains(err.Error(), "Apache backend") {
		t.Fatalf("the refusal does not say what holds the port: %v", err)
	}

	// And the other direction: a port an application already holds is not
	// handed to Apache.
	if _, err := f.service.Create(ctx, node.CreateRequest{
		WebsiteID: second, Port: 3100,
	}); err != nil {
		t.Fatalf("create application: %v", err)
	}
	third := registerThirdWebsite(t, f.pool, f.server)
	thirdSite, err := websiteRepo.Get(ctx, third)
	if err != nil {
		t.Fatalf("get third website: %v", err)
	}
	assigned, err := websiteRepo.AssignBackendPort(ctx, thirdSite)
	if err != nil {
		t.Fatalf("assign backend port: %v", err)
	}
	if assigned == 3100 {
		t.Fatal("Apache was given a port an application already holds")
	}
}
