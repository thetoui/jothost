package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
)

// send issues an authenticated request with an optional JSON body.
func (f *dashboardFixture) send(t *testing.T, method, path, token string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()

	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = "192.0.2.10:44321"
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	f.server.Router.ServeHTTP(rec, req)
	return rec
}

// Every website and job route must refuse an anonymous caller. A route added
// later without RequireAuth makes this fail.
func TestWebsiteRoutesRejectAnonymousCallers(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	const id = "11111111-2222-3333-4444-555555555555"
	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/websites"},
		{http.MethodPost, "/api/v1/websites"},
		{http.MethodGet, "/api/v1/websites/" + id},
		{http.MethodPatch, "/api/v1/websites/" + id},
		{http.MethodDelete, "/api/v1/websites/" + id},
		{http.MethodGet, "/api/v1/websites/" + id + "/domains"},
		{http.MethodPost, "/api/v1/websites/" + id + "/domains"},
		{http.MethodDelete, "/api/v1/domains/" + id},
		{http.MethodGet, "/api/v1/jobs"},
		{http.MethodGet, "/api/v1/jobs/" + id},
		{http.MethodPost, "/api/v1/jobs/" + id + "/cancel"},
	}

	for _, route := range routes {
		rec := fixture.send(t, route.method, route.path, "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: got %d, want 401 without a token",
				route.method, route.path, rec.Code)
		}
	}
}

// A viewer may look at websites but must not change the host.
func TestViewerCannotCreateOrDeleteWebsites(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleViewer)

	rec := fixture.send(t, http.MethodGet, "/api/v1/websites", fixture.token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("viewer listing websites: got %d, want 200: %s", rec.Code, rec.Body)
	}

	rec = fixture.send(t, http.MethodPost, "/api/v1/websites", fixture.token,
		map[string]any{"domain": "example.test"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer creating a website: got %d, want 403", rec.Code)
	}

	const id = "11111111-2222-3333-4444-555555555555"
	rec = fixture.send(t, http.MethodDelete, "/api/v1/websites/"+id, fixture.token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer deleting a website: got %d, want 403", rec.Code)
	}
}

func TestCreateWebsiteReturnsTheSiteAndItsJob(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	rec := fixture.send(t, http.MethodPost, "/api/v1/websites", fixture.token,
		map[string]any{"domain": "example.test", "name": "Example"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201: %s", rec.Code, rec.Body)
	}

	var payload struct {
		Website struct {
			ID            string `json:"id"`
			PrimaryDomain string `json:"primary_domain"`
			Status        string `json:"status"`
			SystemUser    string `json:"system_user"`
			DocumentRoot  string `json:"document_root"`
		} `json:"website"`
		Job struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"job"`
	}
	decodeData(t, rec, &payload)

	if payload.Website.PrimaryDomain != "example.test" {
		t.Fatalf("primary_domain = %q", payload.Website.PrimaryDomain)
	}
	if payload.Website.Status != "creating" {
		t.Fatalf("status = %q, want creating", payload.Website.Status)
	}
	// The JSON contract keeps the name system_user even though the column had
	// to be renamed around a PostgreSQL 16 reserved word.
	if payload.Website.SystemUser == "" {
		t.Fatal("response carries no system_user")
	}
	if payload.Job.Type != "website.create" || payload.Job.Status != "PENDING" {
		t.Fatalf("job = %+v, want a pending website.create", payload.Job)
	}

	// The job is retrievable by id, which is how the UI follows progress.
	rec = fixture.send(t, http.MethodGet, "/api/v1/jobs/"+payload.Job.ID, fixture.token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get job: got %d, want 200: %s", rec.Code, rec.Body)
	}
}

func TestCreateWebsiteRejectsABadDomain(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	rec := fixture.send(t, http.MethodPost, "/api/v1/websites", fixture.token,
		map[string]any{"domain": "not a domain"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid domain: got %d, want 422: %s", rec.Code, rec.Body)
	}
}

// SSL is Phase 6. The request is refused rather than silently succeeding
// without encryption.
func TestCreateWebsiteRefusesSSL(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	rec := fixture.send(t, http.MethodPost, "/api/v1/websites", fixture.token,
		map[string]any{"domain": "secure.test", "ssl_enabled": true})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ssl_enabled: got %d, want 400: %s", rec.Code, rec.Body)
	}
}

// A misspelled field must be reported, not ignored: a website created with an
// empty domain because "domian" was silently dropped is worse than an error.
func TestCreateWebsiteRejectsUnknownFields(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	rec := fixture.send(t, http.MethodPost, "/api/v1/websites", fixture.token,
		map[string]any{"domian": "example.test"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: got %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestWebsiteIDsMustBeUUIDs(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	// A path segment reaching a query as a raw string is how injection starts,
	// so the shape is checked before anything touches the database.
	// Percent-encoded so the values survive as a single path segment; the
	// router decodes them back before the handler sees them.
	for _, id := range []string{
		"not-a-uuid",
		"%27%20OR%201%3D1%20--",
		"..%2F..%2Fetc%2Fpasswd",
	} {
		rec := fixture.send(t, http.MethodGet, "/api/v1/websites/"+id, fixture.token, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("id %q: got %d, want 400", id, rec.Code)
		}
	}
}

func TestUnknownWebsiteIsNotFound(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	rec := fixture.send(t, http.MethodGet,
		"/api/v1/websites/11111111-2222-3333-4444-555555555555", fixture.token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404: %s", rec.Code, rec.Body)
	}

	var env httpx.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("invalid envelope: %v", err)
	}
	if env.Error == nil {
		t.Fatal("404 carried no structured error")
	}
}

func TestDuplicateDomainIsAConflict(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	body := map[string]any{"domain": "example.test"}
	if rec := fixture.send(t, http.MethodPost, "/api/v1/websites",
		fixture.token, body); rec.Code != http.StatusCreated {
		t.Fatalf("first create: got %d: %s", rec.Code, rec.Body)
	}

	rec := fixture.send(t, http.MethodPost, "/api/v1/websites", fixture.token, body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate: got %d, want 409: %s", rec.Code, rec.Body)
	}
}

func TestDeleteWebsiteIsAccepted(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	rec := fixture.send(t, http.MethodPost, "/api/v1/websites", fixture.token,
		map[string]any{"domain": "example.test"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d: %s", rec.Code, rec.Body)
	}

	var created struct {
		Website struct {
			ID string `json:"id"`
		} `json:"website"`
	}
	decodeData(t, rec, &created)

	// 202, not 204: the site is scheduled for removal, not yet gone.
	rec = fixture.send(t, http.MethodDelete,
		"/api/v1/websites/"+created.Website.ID, fixture.token, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("delete: got %d, want 202: %s", rec.Code, rec.Body)
	}

	// The site is still listed while the work is pending, in "deleting".
	rec = fixture.send(t, http.MethodGet,
		"/api/v1/websites/"+created.Website.ID, fixture.token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get after delete: got %d, want 200", rec.Code)
	}

	var site struct {
		Status string `json:"status"`
	}
	decodeData(t, rec, &site)
	if site.Status != "deleting" {
		t.Fatalf("status = %q, want deleting", site.Status)
	}
}

// https_redirect without a certificate takes a site offline, so it is refused
// rather than obeyed.
func TestHTTPSRedirectIsRefusedBeforeSSLExists(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	rec := fixture.send(t, http.MethodPost, "/api/v1/websites", fixture.token,
		map[string]any{"domain": "example.test"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d: %s", rec.Code, rec.Body)
	}
	var created struct {
		Website struct {
			ID string `json:"id"`
		} `json:"website"`
	}
	decodeData(t, rec, &created)

	rec = fixture.send(t, http.MethodPatch, "/api/v1/websites/"+created.Website.ID,
		fixture.token, map[string]any{"https_redirect": true})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("https_redirect: got %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestJobListFiltersAreValidated(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	rec := fixture.send(t, http.MethodGet, "/api/v1/jobs?status=BOGUS", fixture.token, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status filter: got %d, want 400", rec.Code)
	}

	rec = fixture.send(t, http.MethodGet, "/api/v1/jobs?resource_id=nope", fixture.token, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad resource_id: got %d, want 400", rec.Code)
	}

	rec = fixture.send(t, http.MethodGet, "/api/v1/jobs?status=PENDING", fixture.token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid filter: got %d, want 200: %s", rec.Code, rec.Body)
	}
}
