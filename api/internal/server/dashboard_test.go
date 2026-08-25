package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/metrics"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/api/internal/secrets"
	"github.com/jothost/panel/api/internal/servers"
	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/api/internal/users"
	"github.com/jothost/panel/shared/logger"
)

const (
	dashboardPassword = "correct-horse-battery-staple"
	testHostname      = "dashboard-test-host"
)

// dashboardFixture is a server with a registered host and a signed-in user.
type dashboardFixture struct {
	server   *Server
	serverID string
	token    string
	ctx      context.Context
}

// newDashboardFixture builds a server whose local host is registered, and
// signs in a user holding the given role.
func newDashboardFixture(t *testing.T, role string) *dashboardFixture {
	t.Helper()

	deps := testsupport.Require(t)
	ctx := context.Background()

	server, err := servers.NewRepository(deps.Pool).Register(ctx, servers.RegisterParams{
		Hostname: testHostname,
	})
	if err != nil {
		t.Fatalf("register server: %v", err)
	}

	var buf bytes.Buffer
	srv, err := New(Options{
		Config:        baseConfig(),
		Log:           logger.New(logger.Options{Service: "api", Level: "error", Output: &buf}),
		Pool:          deps.Pool,
		Redis:         deps.Redis,
		LocalServerID: server.ID,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	fixture := &dashboardFixture{server: srv, serverID: server.ID, ctx: ctx}
	fixture.token = signIn(t, deps, srv, role)
	return fixture
}

// signIn creates a user with the given role and returns an access token.
func signIn(t *testing.T, deps *testsupport.Deps, srv *Server, role string) string {
	t.Helper()

	hash, err := secrets.HashPassword(dashboardPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	user, err := users.NewRepository(deps.Pool).Create(context.Background(), users.CreateParams{
		Username:     "dashboard_user",
		PasswordHash: hash,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	if role != "" {
		if err := rbac.NewRepository(deps.Pool).AssignRole(context.Background(), user.ID, role); err != nil {
			t.Fatalf("assign role: %v", err)
		}
	}

	body, err := json.Marshal(map[string]string{
		"username": "dashboard_user",
		"password": dashboardPassword,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	req.RemoteAddr = "192.0.2.10:44321"
	rec := httptest.NewRecorder()
	srv.Router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}

	var envelope struct {
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	if envelope.Data.AccessToken == "" {
		t.Fatal("login returned no access token")
	}
	return envelope.Data.AccessToken
}

// get issues an authenticated GET.
func (f *dashboardFixture) get(t *testing.T, path, token string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "192.0.2.10:44321"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	f.server.Router.ServeHTTP(rec, req)
	return rec
}

func decodeData(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()

	var envelope struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid envelope: %v (%s)", err, rec.Body.String())
	}
	if !envelope.Success {
		t.Fatalf("expected success, got %s", rec.Body.String())
	}
	if dst != nil {
		if err := json.Unmarshal(envelope.Data, dst); err != nil {
			t.Fatalf("decode data: %v", err)
		}
	}
}

// ------------------------------------------------------------- authorization

func TestDashboardRoutesRejectAnonymousCallers(t *testing.T) {
	f := newDashboardFixture(t, rbac.RoleAdmin)

	// Host metrics reveal what is running and how loaded it is, so every route
	// here must refuse an unauthenticated caller.
	paths := []string{
		"/api/v1/dashboard",
		"/api/v1/servers",
		"/api/v1/servers/" + f.serverID,
		"/api/v1/servers/" + f.serverID + "/metrics",
	}

	for _, path := range paths {
		rec := f.get(t, path, "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: expected 401 without a token, got %d", path, rec.Code)
		}
	}
}

func TestDashboardRoutesRequireServerView(t *testing.T) {
	// A user with no role is authenticated but holds no permissions.
	f := newDashboardFixture(t, "")

	rec := f.get(t, "/api/v1/dashboard", f.token)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without server.view, got %d (%s)", rec.Code, rec.Body.String())
	}

	var envelope httpx.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid envelope: %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != httpx.CodeForbidden {
		t.Fatalf("unexpected error: %+v", envelope.Error)
	}
}

func TestViewerCanReadTheDashboard(t *testing.T) {
	// The viewer role exists to grant exactly this: read-only visibility.
	f := newDashboardFixture(t, rbac.RoleViewer)

	rec := f.get(t, "/api/v1/dashboard", f.token)
	if rec.Code != http.StatusOK {
		t.Fatalf("a viewer must be able to read the dashboard, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------- dashboard

func TestDashboardRespondsWithoutAnAgent(t *testing.T) {
	// baseConfig points at a socket that does not exist, which is the
	// unreachable-agent case an operator hits when something is badly wrong.
	f := newDashboardFixture(t, rbac.RoleAdmin)

	rec := f.get(t, "/api/v1/dashboard", f.token)
	if rec.Code != http.StatusOK {
		t.Fatalf("the dashboard must still render, got %d (%s)", rec.Code, rec.Body.String())
	}

	var snapshot struct {
		Server struct {
			Hostname string `json:"hostname"`
		} `json:"server"`
		CPU struct {
			Available bool   `json:"available"`
			Error     string `json:"error"`
		} `json:"cpu"`
		Alerts []struct {
			Category string `json:"category"`
			Severity string `json:"severity"`
		} `json:"alerts"`
		GeneratedAt string `json:"generated_at"`
	}
	decodeData(t, rec, &snapshot)

	if snapshot.Server.Hostname != testHostname {
		t.Fatalf("the server record must come from the database: %+v", snapshot.Server)
	}
	if snapshot.CPU.Available {
		t.Fatal("the cpu widget cannot be available without an agent")
	}
	if snapshot.CPU.Error == "" {
		t.Fatal("an unavailable widget must say why")
	}
	if snapshot.GeneratedAt == "" {
		t.Fatal("the snapshot must carry its collection time")
	}

	found := false
	for _, alert := range snapshot.Alerts {
		if alert.Category == "agent" && alert.Severity == "critical" {
			found = true
		}
	}
	if !found {
		t.Fatalf("an unreachable agent must be stated plainly: %+v", snapshot.Alerts)
	}
}

func TestDashboardRejectsAMalformedServerID(t *testing.T) {
	f := newDashboardFixture(t, rbac.RoleAdmin)

	// A malformed id must be a 400 rather than reaching Postgres as a cast
	// error, which would surface as an internal error. Values are encoded so
	// the hostile ones travel as a query parameter rather than mangling the
	// request line.
	for _, id := range []string{"not-a-uuid", "1", "'; DROP TABLE servers;--", "../../etc/passwd"} {
		rec := f.get(t, "/api/v1/dashboard?server_id="+url.QueryEscape(id), f.token)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("server_id %q: expected 400, got %d", id, rec.Code)
		}
	}
}

func TestDashboardReportsAnUnknownServer(t *testing.T) {
	f := newDashboardFixture(t, rbac.RoleAdmin)

	rec := f.get(t, "/api/v1/dashboard?server_id=00000000-0000-0000-0000-000000000000", f.token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// ------------------------------------------------------------------ servers

func TestListServers(t *testing.T) {
	f := newDashboardFixture(t, rbac.RoleAdmin)

	rec := f.get(t, "/api/v1/servers", f.token)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result struct {
		Servers []struct {
			ID       string `json:"id"`
			Hostname string `json:"hostname"`
		} `json:"servers"`
		Count int `json:"count"`
	}
	decodeData(t, rec, &result)

	if result.Count != 1 || len(result.Servers) != 1 {
		t.Fatalf("expected one server, got %+v", result)
	}
	if result.Servers[0].Hostname != testHostname {
		t.Fatalf("unexpected server: %+v", result.Servers[0])
	}
}

func TestGetServer(t *testing.T) {
	f := newDashboardFixture(t, rbac.RoleAdmin)

	rec := f.get(t, "/api/v1/servers/"+f.serverID, f.token)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var server struct {
		ID       string `json:"id"`
		Hostname string `json:"hostname"`
	}
	decodeData(t, rec, &server)

	if server.ID != f.serverID {
		t.Fatalf("unexpected server id %q", server.ID)
	}
}

func TestGetServerRejectsBadIDs(t *testing.T) {
	f := newDashboardFixture(t, rbac.RoleAdmin)

	if rec := f.get(t, "/api/v1/servers/not-a-uuid", f.token); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if rec := f.get(t, "/api/v1/servers/00000000-0000-0000-0000-000000000000", f.token); rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// ------------------------------------------------------------------ metrics

func TestServerMetricsReturnsASeries(t *testing.T) {
	f := newDashboardFixture(t, rbac.RoleAdmin)

	deps := testsupport.Require(t)
	// Require resets the database, so the fixture's rows are gone; re-register
	// and seed within this test's own state.
	server, err := servers.NewRepository(deps.Pool).Register(f.ctx, servers.RegisterParams{
		Hostname: testHostname,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	cpu := 42.0
	if err := metrics.NewRepository(deps.Pool).Insert(f.ctx, server.ID, metrics.Sample{
		Timestamp:  time.Now().Add(-5 * time.Minute),
		CPUPercent: &cpu,
	}); err != nil {
		t.Fatalf("insert sample: %v", err)
	}

	token := signIn(t, deps, f.server, rbac.RoleAdmin)
	rec := f.get(t, "/api/v1/servers/"+server.ID+"/metrics?range=1h", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	var series struct {
		Range  string `json:"range"`
		Bucket string `json:"bucket"`
		Points []struct {
			CPUPercent *float64 `json:"cpu_percent"`
		} `json:"points"`
	}
	decodeData(t, rec, &series)

	if series.Range != "1h" {
		t.Fatalf("range = %q", series.Range)
	}
	if len(series.Points) != 1 {
		t.Fatalf("expected 1 point, got %d", len(series.Points))
	}
	if series.Points[0].CPUPercent == nil || *series.Points[0].CPUPercent != 42 {
		t.Fatalf("unexpected point: %+v", series.Points[0])
	}
}

func TestServerMetricsValidatesRange(t *testing.T) {
	f := newDashboardFixture(t, rbac.RoleAdmin)

	// A typo must be refused rather than silently returning an hour, which
	// would look like a working graph showing the wrong window.
	for _, rng := range []string{"2h", "forever", "1h; DROP TABLE system_metrics", "-1h"} {
		rec := f.get(t, "/api/v1/servers/"+f.serverID+"/metrics?range="+url.QueryEscape(rng), f.token)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("range %q: expected 400, got %d", rng, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "1h") {
			t.Fatalf("the error must name the supported ranges: %s", rec.Body.String())
		}
	}

	// Every documented range must be accepted.
	for _, rng := range []string{"", "1h", "24h", "7d", "30d"} {
		path := "/api/v1/servers/" + f.serverID + "/metrics"
		if rng != "" {
			path += "?range=" + rng
		}
		if rec := f.get(t, path, f.token); rec.Code != http.StatusOK {
			t.Fatalf("range %q: expected 200, got %d", rng, rec.Code)
		}
	}
}

func TestServerMetricsRejectsAnUnknownServer(t *testing.T) {
	f := newDashboardFixture(t, rbac.RoleAdmin)

	// An unknown id must be a 404, not an empty series that looks like a host
	// with no history.
	rec := f.get(t, "/api/v1/servers/00000000-0000-0000-0000-000000000000/metrics", f.token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
