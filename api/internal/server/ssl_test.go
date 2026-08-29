package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/api/internal/ssl"
)

// issuedCertificate records a certificate as the Agent would have reported it.
func (f *dashboardFixture) issuedCertificate(t *testing.T, websiteID string, expiresIn time.Duration) {
	t.Helper()
	ctx := context.Background()

	repo := ssl.NewRepository(f.server.pool)
	if _, err := repo.Upsert(ctx, ssl.UpsertParams{
		WebsiteID: websiteID,
		Provider:  ssl.ProviderSelfSigned,
		Domains:   []string{"example.test"},
		AutoRenew: true,
		Status:    ssl.StatusIssuing,
	}); err != nil {
		t.Fatalf("upsert certificate: %v", err)
	}

	now := time.Now()
	err := repo.MarkIssued(ctx, ssl.IssuedParams{
		WebsiteID:       websiteID,
		Provider:        ssl.ProviderSelfSigned,
		Domains:         []string{"example.test"},
		CertificatePath: "/etc/jothost/ssl/example.test/fullchain.pem",
		PrivateKeyPath:  "/etc/jothost/ssl/example.test/privkey.pem",
		Issuer:          "example.test",
		IssuedAt:        now,
		ExpiresAt:       now.Add(expiresIn),
	}, now)
	if err != nil {
		t.Fatalf("mark issued: %v", err)
	}
}

// Every certificate route must refuse an anonymous caller.
func TestSSLRoutesRejectAnonymousCallers(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	const id = "11111111-2222-3333-4444-555555555555"
	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/ssl"},
		{http.MethodGet, "/api/v1/ssl/providers"},
		{http.MethodGet, "/api/v1/websites/" + id + "/ssl"},
		{http.MethodPost, "/api/v1/websites/" + id + "/ssl/issue"},
		{http.MethodPost, "/api/v1/websites/" + id + "/ssl/renew"},
		{http.MethodPost, "/api/v1/websites/" + id + "/ssl/revoke"},
		{http.MethodPatch, "/api/v1/websites/" + id + "/ssl"},
	}

	for _, route := range routes {
		rec := fixture.send(t, route.method, route.path, "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: got %d, want 401 without a token",
				route.method, route.path, rec.Code)
		}
	}
}

// A certificate is the site's identity, so issuing or revoking one is gated
// separately from editing the site's content.
func TestIssuingACertificateNeedsSSLManage(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleViewer)

	rec := fixture.send(t, http.MethodGet, "/api/v1/ssl", fixture.token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("viewer listing certificates: got %d, want 200: %s", rec.Code, rec.Body)
	}

	const id = "11111111-2222-3333-4444-555555555555"
	for _, path := range []string{"/ssl/issue", "/ssl/renew", "/ssl/revoke"} {
		rec = fixture.send(t, http.MethodPost, "/api/v1/websites/"+id+path,
			fixture.token, map[string]any{})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("viewer posting %s: got %d, want 403", path, rec.Code)
		}
	}
}

func TestWebsiteStartsWithoutACertificate(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)
	siteID := fixture.createActiveSite(t, "example.test")

	rec := fixture.send(t, http.MethodGet, "/api/v1/websites/"+siteID+"/ssl",
		fixture.token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body)
	}

	// Plain HTTP is a valid configuration, so this is 200 with enabled false
	// rather than a 404 suggesting the website is missing.
	var payload struct {
		Enabled bool `json:"enabled"`
	}
	decodeData(t, rec, &payload)
	if payload.Enabled {
		t.Fatal("a new website should not have a certificate")
	}
}

func TestIssuingIsAccepted(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)
	siteID := fixture.createActiveSite(t, "example.test")

	rec := fixture.send(t, http.MethodPost, "/api/v1/websites/"+siteID+"/ssl/issue",
		fixture.token, map[string]any{"provider": "selfsigned"})
	// 202: the certificate does not exist yet, only the intent to obtain one.
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
	if payload.Job.Type != "ssl.issue" || payload.Job.Status != "PENDING" {
		t.Fatalf("job = %+v, want a pending ssl.issue", payload.Job)
	}
}

func TestIssuingRejectsAnUnknownProvider(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)
	siteID := fixture.createActiveSite(t, "example.test")

	rec := fixture.send(t, http.MethodPost, "/api/v1/websites/"+siteID+"/ssl/issue",
		fixture.token, map[string]any{"provider": "acme-corp"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422: %s", rec.Code, rec.Body)
	}
}

func TestIssuingRejectsUnknownFields(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)
	siteID := fixture.createActiveSite(t, "example.test")

	rec := fixture.send(t, http.MethodPost, "/api/v1/websites/"+siteID+"/ssl/issue",
		fixture.token, map[string]any{"provdier": "selfsigned"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestRenewingWithoutACertificateIsNotFound(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)
	siteID := fixture.createActiveSite(t, "example.test")

	rec := fixture.send(t, http.MethodPost, "/api/v1/websites/"+siteID+"/ssl/renew",
		fixture.token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404: %s", rec.Code, rec.Body)
	}
}

func TestCertificateListLeadsWithWhatNeedsAttention(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)
	siteID := fixture.createActiveSite(t, "example.test")
	// Inside the warning window.
	fixture.issuedCertificate(t, siteID, 5*24*time.Hour)

	rec := fixture.send(t, http.MethodGet, "/api/v1/ssl", fixture.token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body)
	}

	var payload struct {
		Certificates []struct {
			Status        string `json:"status"`
			DaysRemaining *int   `json:"days_remaining"`
			PrimaryDomain string `json:"primary_domain"`
		} `json:"certificates"`
		Count            int `json:"count"`
		NeedingAttention int `json:"needing_attention"`
	}
	decodeData(t, rec, &payload)

	if payload.Count != 1 {
		t.Fatalf("listed %d certificates, want 1", payload.Count)
	}
	if payload.NeedingAttention != 1 {
		t.Fatalf("needing attention = %d, want 1", payload.NeedingAttention)
	}
	if payload.Certificates[0].Status != "expiring" {
		t.Fatalf("status = %q, want expiring", payload.Certificates[0].Status)
	}
	// Computed by the API so every client agrees on what "5 days" means.
	if payload.Certificates[0].DaysRemaining == nil {
		t.Fatal("days_remaining was not computed")
	}
	if payload.Certificates[0].PrimaryDomain != "example.test" {
		t.Fatalf("primary domain = %q", payload.Certificates[0].PrimaryDomain)
	}
}

func TestTurningOffAutoRenewalNeedsNoJob(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)
	siteID := fixture.createActiveSite(t, "example.test")
	fixture.issuedCertificate(t, siteID, 60*24*time.Hour)

	// Nothing on the host reads auto_renew, so changing it rewrites no vhost
	// and produces no job to poll.
	rec := fixture.send(t, http.MethodPatch, "/api/v1/websites/"+siteID+"/ssl",
		fixture.token, map[string]any{"auto_renew": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body)
	}

	rec = fixture.send(t, http.MethodGet, "/api/v1/websites/"+siteID+"/ssl",
		fixture.token, nil)
	var state struct {
		Certificate struct {
			AutoRenew bool `json:"auto_renew"`
		} `json:"certificate"`
	}
	decodeData(t, rec, &state)
	if state.Certificate.AutoRenew {
		t.Fatal("auto renewal is still on")
	}
}

// The redirect *is* the vhost, so changing it has to rewrite the host's
// configuration and therefore produces a job.
func TestChangingTheRedirectQueuesAJob(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)
	siteID := fixture.createActiveSite(t, "example.test")
	fixture.issuedCertificate(t, siteID, 60*24*time.Hour)

	rec := fixture.send(t, http.MethodPatch, "/api/v1/websites/"+siteID+"/ssl",
		fixture.token, map[string]any{"https_redirect": true})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got %d, want 202: %s", rec.Code, rec.Body)
	}
}

func TestSSLWebsiteIDsMustBeUUIDs(t *testing.T) {
	fixture := newDashboardFixture(t, rbac.RoleAdmin)

	for _, id := range []string{"not-a-uuid", "%27%20OR%201%3D1%20--"} {
		rec := fixture.send(t, http.MethodGet, "/api/v1/websites/"+id+"/ssl",
			fixture.token, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("id %q: got %d, want 400", id, rec.Code)
		}
	}
}
