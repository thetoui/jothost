package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/jothost/panel/api/internal/php"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/api/internal/websites"
)

// installVersion records a PHP version as present on the host, standing in for
// what the Agent's detection would have reported.
//
// The pool comes from the fixture rather than testsupport.Require: calling
// Require again mid-test resets every table, which would truncate the session
// the fixture just signed in with.
func (f *dashboardFixture) installVersion(t *testing.T, version string) {
	t.Helper()

	repo := php.NewRepository(f.server.pool)
	err := repo.SyncVersions(context.Background(), []php.DetectedVersion{{
		Version:    version,
		BinaryPath: "/usr/sbin/php-fpm" + version,
		FPMService: "php-fpm" + version,
	}})
	if err != nil {
		t.Fatalf("sync versions: %v", err)
	}
}

// createActiveSite creates a website through the API and marks it active.
func (f *dashboardFixture) createActiveSite(t *testing.T, domain string) string {
	t.Helper()

	rec := f.send(t, http.MethodPost, "/api/v1/websites", f.token,
		map[string]any{"domain": domain})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create website: got %d: %s", rec.Code, rec.Body)
	}

	var created struct {
		Website struct {
			ID string `json:"id"`
		} `json:"website"`
	}
	decodeData(t, rec, &created)

	repo := websites.NewRepository(f.server.pool)
	if err := repo.SetStatus(context.Background(), created.Website.ID,
		websites.StatusActive); err != nil {
		t.Fatalf("activate website: %v", err)
	}
	return created.Website.ID
}

// Every PHP route must refuse an anonymous caller.
func TestPHPRoutesRejectAnonymousCallers(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	const id = "11111111-2222-3333-4444-555555555555"
	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/php/versions"},
		{http.MethodGet, "/api/v1/php/versions/8.3"},
		{http.MethodPost, "/api/v1/php/versions/install"},
		{http.MethodDelete, "/api/v1/php/versions/8.3"},
		{http.MethodGet, "/api/v1/websites/" + id + "/php"},
		{http.MethodPatch, "/api/v1/websites/" + id + "/php"},
		{http.MethodGet, "/api/v1/websites/" + id + "/php/config"},
		{http.MethodPatch, "/api/v1/websites/" + id + "/php/config"},
	}

	for _, route := range routes {
		rec := fixture.send(t, route.method, route.path, "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: got %d, want 401 without a token",
				route.method, route.path, rec.Code)
		}
	}
}

// Installing a PHP version changes the whole server, so it needs server.manage
// rather than a website permission.
func TestInstallingPHPNeedsServerManage(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleViewer)

	rec := fixture.send(t, http.MethodGet, "/api/v1/php/versions", fixture.token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("viewer listing versions: got %d, want 200: %s", rec.Code, rec.Body)
	}

	rec = fixture.send(t, http.MethodPost, "/api/v1/php/versions/install",
		fixture.token, map[string]any{"version": "8.3"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer installing PHP: got %d, want 403", rec.Code)
	}

	rec = fixture.send(t, http.MethodDelete, "/api/v1/php/versions/8.3", fixture.token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer removing PHP: got %d, want 403", rec.Code)
	}
}

func TestListVersionsReturnsWhatWasDetected(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)
	fixture.installVersion(t, "8.3")

	rec := fixture.send(t, http.MethodGet, "/api/v1/php/versions", fixture.token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body)
	}

	var payload struct {
		Versions []struct {
			Version   string `json:"version"`
			Installed bool   `json:"installed"`
			InUse     int    `json:"in_use"`
		} `json:"versions"`
		Count int `json:"count"`
	}
	decodeData(t, rec, &payload)

	if payload.Count != 1 || payload.Versions[0].Version != "8.3" {
		t.Fatalf("versions = %+v, want one 8.3", payload.Versions)
	}
	if !payload.Versions[0].Installed {
		t.Fatal("the detected version is not marked installed")
	}
}

// The version reaches a package name and a path, so anything that is not a
// bare major.minor must be refused at the edge.
func TestPHPVersionPathIsValidated(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	for _, version := range []string{
		"latest",
		"8.3.19",
		"8",
		"..%2F..%2Fetc",
		"8.3%3Brm%20-rf%20%2F",
	} {
		rec := fixture.send(t, http.MethodGet, "/api/v1/php/versions/"+version,
			fixture.token, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("version %q: got %d, want 400", version, rec.Code)
		}
	}
}

func TestInstallRejectsAMalformedVersion(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	rec := fixture.send(t, http.MethodPost, "/api/v1/php/versions/install",
		fixture.token, map[string]any{"version": "latest"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422: %s", rec.Code, rec.Body)
	}
}

func TestInstallRejectsUnknownFields(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	rec := fixture.send(t, http.MethodPost, "/api/v1/php/versions/install",
		fixture.token, map[string]any{"verison": "8.3"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestInstallIsAccepted(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	rec := fixture.send(t, http.MethodPost, "/api/v1/php/versions/install",
		fixture.token, map[string]any{"version": "8.3"})
	// 202: the version is not on the host yet, only scheduled to be.
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got %d, want 202: %s", rec.Code, rec.Body)
	}

	var payload struct {
		Job struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"job"`
	}
	decodeData(t, rec, &payload)
	if payload.Job.Type != "php.install" || payload.Job.Status != "PENDING" {
		t.Fatalf("job = %+v, want a pending php.install", payload.Job)
	}
}

// ------------------------------------------------------- per-site selection

func TestWebsitePHPStartsDisabled(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)
	siteID := fixture.createActiveSite(t, "example.test")

	rec := fixture.send(t, http.MethodGet, "/api/v1/websites/"+siteID+"/php",
		fixture.token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body)
	}

	// A static site is a valid configuration, so this is 200 with enabled
	// false rather than a 404 suggesting the website is missing.
	var payload struct {
		Enabled bool `json:"enabled"`
	}
	decodeData(t, rec, &payload)
	if payload.Enabled {
		t.Fatal("a new website should not run PHP")
	}
}

func TestSettingWebsitePHPIsAccepted(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)
	fixture.installVersion(t, "8.3")
	siteID := fixture.createActiveSite(t, "example.test")

	rec := fixture.send(t, http.MethodPatch, "/api/v1/websites/"+siteID+"/php",
		fixture.token, map[string]any{"version": "8.3"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got %d, want 202: %s", rec.Code, rec.Body)
	}

	var payload struct {
		Job struct {
			Type string `json:"type"`
		} `json:"job"`
	}
	decodeData(t, rec, &payload)
	if payload.Job.Type != "website.php.set" {
		t.Fatalf("job type = %q", payload.Job.Type)
	}

	// The pool is recorded immediately so the panel can show what the site is
	// becoming while the work runs.
	rec = fixture.send(t, http.MethodGet, "/api/v1/websites/"+siteID+"/php",
		fixture.token, nil)
	var enabled struct {
		Enabled bool `json:"enabled"`
		Pool    struct {
			PHPVersion string `json:"php_version"`
		} `json:"pool"`
	}
	decodeData(t, rec, &enabled)
	if !enabled.Enabled || enabled.Pool.PHPVersion != "8.3" {
		t.Fatalf("pool = %+v, want 8.3 enabled", enabled)
	}
}

func TestSettingAnUninstalledVersionIsAConflict(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)
	siteID := fixture.createActiveSite(t, "example.test")

	rec := fixture.send(t, http.MethodPatch, "/api/v1/websites/"+siteID+"/php",
		fixture.token, map[string]any{"version": "8.3"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409: %s", rec.Code, rec.Body)
	}
}

func TestSettingPHPRequiresAnExplicitVersionField(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)
	siteID := fixture.createActiveSite(t, "example.test")

	// Omitting the field is ambiguous: it could mean "leave it alone" or
	// "turn it off", so it is refused rather than guessed at.
	rec := fixture.send(t, http.MethodPatch, "/api/v1/websites/"+siteID+"/php",
		fixture.token, map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", rec.Code, rec.Body)
	}
}

// An explicit null and an absent field mean different things — "make this
// static" and "leave PHP alone" — and a *string cannot tell them apart,
// because JSON null and a missing key both decode to nil.
func TestNullVersionTurnsPHPOffRatherThanBeingTreatedAsAbsent(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)
	fixture.installVersion(t, "8.3")
	siteID := fixture.createActiveSite(t, "example.test")

	if rec := fixture.send(t, http.MethodPatch, "/api/v1/websites/"+siteID+"/php",
		fixture.token, map[string]any{"version": "8.3"}); rec.Code != http.StatusAccepted {
		t.Fatalf("enable php: got %d: %s", rec.Code, rec.Body)
	}

	// nil in a map[string]any marshals to JSON null.
	rec := fixture.send(t, http.MethodPatch, "/api/v1/websites/"+siteID+"/php",
		fixture.token, map[string]any{"version": nil})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("null version: got %d, want 202: %s", rec.Code, rec.Body)
	}

	var payload struct {
		Job struct {
			Type string `json:"type"`
		} `json:"job"`
	}
	decodeData(t, rec, &payload)
	if payload.Job.Type != "website.php.unset" {
		t.Fatalf("job type = %q, want website.php.unset", payload.Job.Type)
	}
}

func TestANonStringVersionIsRejected(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)
	siteID := fixture.createActiveSite(t, "example.test")

	for _, value := range []any{42, true, []string{"8.3"}} {
		rec := fixture.send(t, http.MethodPatch, "/api/v1/websites/"+siteID+"/php",
			fixture.token, map[string]any{"version": value})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("version %v: got %d, want 400", value, rec.Code)
		}
	}
}

func TestPHPSettingsAreValidatedAtTheEdge(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)
	fixture.installVersion(t, "8.3")
	siteID := fixture.createActiveSite(t, "example.test")

	// These strings reach an FPM pool file, so a rejected value must never be
	// stored, let alone written.
	for _, body := range []map[string]any{
		{"version": "8.3", "memory_limit": "512M\nuser = root"},
		{"version": "8.3", "memory_limit": "unlimited"},
		{"version": "8.3", "upload_max_filesize": "9999G"},
		{"version": "8.3", "max_execution_time": 100000},
		{"version": "8.3", "max_execution_time": -5},
	} {
		rec := fixture.send(t, http.MethodPatch, "/api/v1/websites/"+siteID+"/php",
			fixture.token, body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("body %v: got %d, want 422: %s", body, rec.Code, rec.Body)
		}
	}
}

func TestPHPConfigNeedsAPoolToConfigure(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)
	siteID := fixture.createActiveSite(t, "example.test")

	rec := fixture.send(t, http.MethodGet, "/api/v1/websites/"+siteID+"/php/config",
		fixture.token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404: %s", rec.Code, rec.Body)
	}
}

func TestPHPConfigRoundTrips(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)
	fixture.installVersion(t, "8.3")
	siteID := fixture.createActiveSite(t, "example.test")

	if rec := fixture.send(t, http.MethodPatch, "/api/v1/websites/"+siteID+"/php",
		fixture.token, map[string]any{"version": "8.3"}); rec.Code != http.StatusAccepted {
		t.Fatalf("enable php: got %d: %s", rec.Code, rec.Body)
	}

	rec := fixture.send(t, http.MethodPatch, "/api/v1/websites/"+siteID+"/php/config",
		fixture.token, map[string]any{"memory_limit": "512M", "opcache": false})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("set config: got %d, want 202: %s", rec.Code, rec.Body)
	}

	rec = fixture.send(t, http.MethodGet, "/api/v1/websites/"+siteID+"/php/config",
		fixture.token, nil)
	var config struct {
		MemoryLimit string `json:"memory_limit"`
		OPcache     bool   `json:"opcache"`
		Version     string `json:"version"`
	}
	decodeData(t, rec, &config)

	if config.MemoryLimit != "512M" {
		t.Fatalf("memory_limit = %q, want 512M", config.MemoryLimit)
	}
	if config.OPcache {
		t.Fatal("opcache should be off")
	}
	// Changing configuration must not change the version.
	if config.Version != "8.3" {
		t.Fatalf("version = %q, want 8.3", config.Version)
	}
}

func TestPHPWebsiteIDsMustBeUUIDs(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	for _, id := range []string{"not-a-uuid", "%27%20OR%201%3D1%20--"} {
		rec := fixture.send(t, http.MethodGet, "/api/v1/websites/"+id+"/php",
			fixture.token, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("id %q: got %d, want 400", id, rec.Code)
		}
	}
}
