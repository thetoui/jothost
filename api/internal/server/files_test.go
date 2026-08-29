package server

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/jothost/panel/api/internal/rbac"
)

// fileRoutes is every endpoint the file manager exposes, with the shape of a
// request that reaches its handler.
var fileRoutes = []struct {
	method string
	path   string
	body   map[string]any
	// write says the route mutates the filesystem and therefore needs
	// file.write rather than file.read.
	write bool
}{
	{http.MethodGet, "/api/v1/files?path=/var/www", nil, false},
	{http.MethodGet, "/api/v1/files/stat?path=/var/www", nil, false},
	{http.MethodGet, "/api/v1/files/download?path=/var/www/x.txt", nil, false},
	{http.MethodGet, "/api/v1/files/search?path=/var/www&query=x", nil, false},

	{http.MethodPost, "/api/v1/files/folder", map[string]any{"path": "/var/www", "name": "new"}, true},
	{http.MethodPost, "/api/v1/files/file", map[string]any{"path": "/var/www", "name": "new.txt"}, true},
	{http.MethodPost, "/api/v1/files/copy",
		map[string]any{"source": "/var/www/a", "destination": "/var/www/b"}, true},
	{http.MethodPost, "/api/v1/files/move",
		map[string]any{"source": "/var/www/a", "destination": "/var/www/b"}, true},
	{http.MethodPost, "/api/v1/files/zip",
		map[string]any{"sources": []string{"/var/www/a"}, "destination": "/var/www/a.zip"}, true},
	{http.MethodPost, "/api/v1/files/unzip",
		map[string]any{"path": "/var/www/a.zip", "destination": "/var/www/out"}, true},
	{http.MethodPatch, "/api/v1/files",
		map[string]any{"path": "/var/www/a", "mode": "0644"}, true},
	{http.MethodDelete, "/api/v1/files?path=/var/www/a", nil, true},
}

// TASKS.md Phase 7: unauthorized access test.
//
// A file manager reachable without a session is a remote filesystem browser on
// a host running the Agent as root.
func TestFileRoutesRejectAnonymousCallers(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	for _, route := range fileRoutes {
		rec := fixture.send(t, route.method, route.path, "", route.body)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: got %d, want 401 without a token",
				route.method, route.path, rec.Code)
		}
	}
}

// A token that is not a token must not be treated as one.
func TestFileRoutesRejectAForgedToken(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	for _, token := range []string{"not-a-token", "Bearer", "..", "null"} {
		rec := fixture.send(t, http.MethodGet, "/api/v1/files?path=/var/www", token, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: got %d, want 401", token, rec.Code)
		}
	}
}

// A viewer can look but not touch. That split is the difference between a
// support account and an administrator, and it is enforced per route.
func TestViewersCanReadFilesButNotChangeThem(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleViewer)

	for _, route := range fileRoutes {
		rec := fixture.send(t, route.method, route.path, fixture.token, route.body)

		if route.write {
			if rec.Code != http.StatusForbidden {
				t.Fatalf("viewer %s %s: got %d, want 403",
					route.method, route.path, rec.Code)
			}
			continue
		}
		// A read is permitted as far as authorisation goes. What it returns
		// depends on the Agent, which is not running in this test, so anything
		// but 401 or 403 means the permission check let it through.
		if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
			t.Fatalf("viewer %s %s: got %d, want the read to be allowed",
				route.method, route.path, rec.Code)
		}
	}
}

// TASKS.md Phase 7: path traversal test, at the API edge.
//
// The Agent re-validates everything through pathsec, which is the authority.
// This asserts the cheap edge check refuses the obvious shapes before they
// cost a round trip — and, more importantly, that it does not accept one.
func TestFileRoutesRefuseTraversalAndMalformedPaths(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	hostile := []string{
		"/var/www/../../etc/passwd",
		"/var/www/../..",
		"../etc/passwd",
		"etc/passwd",
		"",
		"/var/www/\x00/etc",
	}

	for _, path := range hostile {
		target := "/api/v1/files?path=" + url.QueryEscape(path)
		rec := fixture.send(t, http.MethodGet, target, fixture.token, nil)
		if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
			t.Fatalf("path %q: got %d, want a validation failure", path, rec.Code)
		}

		rec = fixture.send(t, http.MethodDelete,
			"/api/v1/files?path="+url.QueryEscape(path), fixture.token, nil)
		if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
			t.Fatalf("delete %q: got %d, want a validation failure", path, rec.Code)
		}
	}
}

// A name is a single segment. Accepting a path here would let a caller create a
// file anywhere the Agent's root allows, bypassing the directory they asked in.
func TestCreatingRefusesANameThatIsAPath(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	for _, name := range []string{
		"../escape.txt",
		"nested/child.txt",
		"/absolute.txt",
		"..",
		".",
		"",
	} {
		for _, endpoint := range []string{"/api/v1/files/folder", "/api/v1/files/file"} {
			rec := fixture.send(t, http.MethodPost, endpoint, fixture.token,
				map[string]any{"path": "/var/www", "name": name})
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s with name %q: got %d, want 422", endpoint, name, rec.Code)
			}
		}
	}
}

func TestFileRoutesRejectUnknownFields(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	rec := fixture.send(t, http.MethodPost, "/api/v1/files/folder", fixture.token,
		map[string]any{"path": "/var/www", "nmae": "typo"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 for an unknown field", rec.Code)
	}
}

// A PATCH that changes nothing is a caller mistake, not a no-op to accept
// silently.
func TestPatchNeedsSomethingToChange(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	rec := fixture.send(t, http.MethodPatch, "/api/v1/files", fixture.token,
		map[string]any{"path": "/var/www/a"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422: %s", rec.Code, rec.Body)
	}
}

func TestSearchNeedsAQuery(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	for _, target := range []string{
		"/api/v1/files/search?path=/var/www",
		"/api/v1/files/search?path=/var/www&query=",
		"/api/v1/files/search?path=/var/www&query=%20%20",
	} {
		rec := fixture.send(t, http.MethodGet, target, fixture.token, nil)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: got %d, want 422", target, rec.Code)
		}
	}
}

func TestZipNeedsSources(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	rec := fixture.send(t, http.MethodPost, "/api/v1/files/zip", fixture.token,
		map[string]any{"sources": []string{}, "destination": "/var/www/out.zip"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422: %s", rec.Code, rec.Body)
	}
}

// An upload has to be multipart. A JSON body here would mean file content
// travelling through a code path that was never sized for it.
func TestUploadRequiresMultipart(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	rec := fixture.send(t, http.MethodPost, "/api/v1/files/upload?path=/var/www",
		fixture.token, map[string]any{"file": "content"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", rec.Code, rec.Body)
	}
}
