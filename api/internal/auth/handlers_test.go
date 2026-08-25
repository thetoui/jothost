package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/api/internal/secrets"
)

// router builds a mux with the auth routes registered.
func (f *fixture) router() http.Handler {
	mux := http.NewServeMux()
	NewHandler(f.svc).Routes(mux)
	return mux
}

// call issues a request and returns the recorder.
func (f *fixture) call(t *testing.T, method, path string, body any, token string) *httptest.ResponseRecorder {
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
	f.router().ServeHTTP(rec, req)
	return rec
}

// decodeData unmarshals the envelope's data field.
func decodeData(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()

	var env struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("invalid envelope: %v (%s)", err, rec.Body.String())
	}
	if !env.Success {
		t.Fatalf("expected a success envelope, got %s", rec.Body.String())
	}
	if dst != nil {
		if err := json.Unmarshal(env.Data, dst); err != nil {
			t.Fatalf("invalid data payload: %v", err)
		}
	}
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) httpx.ErrorDetail {
	t.Helper()

	var env httpx.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("invalid envelope: %v (%s)", err, rec.Body.String())
	}
	if env.Success {
		t.Fatalf("expected an error envelope, got %s", rec.Body.String())
	}
	if env.Error == nil {
		t.Fatal("error envelope must carry an error detail")
	}
	return *env.Error
}

// login performs a full login through HTTP and returns the tokens.
func (f *fixture) login(t *testing.T) loginResponse {
	t.Helper()

	rec := f.call(t, http.MethodPost, "/api/v1/auth/login",
		loginRequest{Username: testUsername, Password: testPassword}, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}

	var body loginResponse
	decodeData(t, rec, &body)
	return body
}

// ------------------------------------------------------------------ login

func TestLoginEndpointReturnsTokens(t *testing.T) {
	f := newFixture(t)
	body := f.login(t)

	if body.AccessToken == "" || body.RefreshToken == "" {
		t.Fatal("login must return both tokens")
	}
	if body.TokenType != "Bearer" {
		t.Fatalf("unexpected token type %q", body.TokenType)
	}
	if body.ExpiresIn != 900 {
		t.Fatalf("expires_in = %d, want 900", body.ExpiresIn)
	}
}

func TestLoginEndpointRejectsBadCredentials(t *testing.T) {
	f := newFixture(t)

	rec := f.call(t, http.MethodPost, "/api/v1/auth/login",
		loginRequest{Username: testUsername, Password: "wrong-password-entirely"}, "")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	detail := decodeError(t, rec)
	if detail.Code != httpx.CodeUnauthorized {
		t.Fatalf("unexpected error code %q", detail.Code)
	}
}

func TestLoginErrorIsIdenticalForUnknownUsers(t *testing.T) {
	f := newFixture(t)

	wrongPassword := f.call(t, http.MethodPost, "/api/v1/auth/login",
		loginRequest{Username: testUsername, Password: "wrong-password-entirely"}, "")
	unknownUser := f.call(t, http.MethodPost, "/api/v1/auth/login",
		loginRequest{Username: "ghost", Password: "wrong-password-entirely"}, "")

	if wrongPassword.Code != unknownUser.Code {
		t.Fatalf("status codes differ: %d vs %d", wrongPassword.Code, unknownUser.Code)
	}

	// The bodies must be byte-identical apart from the request id, or the
	// endpoint reveals which usernames exist.
	a := decodeError(t, wrongPassword)
	b := decodeError(t, unknownUser)
	if a != b {
		t.Fatalf("error bodies differ: %+v vs %+v", a, b)
	}
}

func TestLoginValidatesInput(t *testing.T) {
	f := newFixture(t)

	cases := []struct {
		name string
		body any
		want int
	}{
		{"empty username", loginRequest{Username: "", Password: testPassword}, http.StatusUnprocessableEntity},
		{"empty password", loginRequest{Username: testUsername, Password: ""}, http.StatusUnprocessableEntity},
		{"blank username", loginRequest{Username: "   ", Password: testPassword}, http.StatusUnprocessableEntity},
	}

	for _, tc := range cases {
		rec := f.call(t, http.MethodPost, "/api/v1/auth/login", tc.body, "")
		if rec.Code != tc.want {
			t.Fatalf("%s: expected %d, got %d", tc.name, tc.want, rec.Code)
		}
	}
}

func TestLoginRejectsMalformedBodies(t *testing.T) {
	f := newFixture(t)

	malformed := []string{
		"",
		"not json",
		`{"username": "admin"`,
		`{"username": "admin", "password": "x", "is_admin": true}`,       // unknown field
		`{"username":"a","password":"b"}{"username":"c","password":"d"}`, // two documents
	}

	for _, body := range malformed {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
		req.RemoteAddr = "192.0.2.10:44321"
		rec := httptest.NewRecorder()
		f.router().ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q: expected 400, got %d (%s)", body, rec.Code, rec.Body.String())
		}
	}
}

func TestLoginRejectsOversizedBody(t *testing.T) {
	f := newFixture(t)

	// A megabyte of padding must be refused before it is parsed.
	huge := `{"username":"admin","password":"` + strings.Repeat("a", 1<<20) + `"}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(huge))
	req.RemoteAddr = "192.0.2.10:44321"
	rec := httptest.NewRecorder()
	f.router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an oversized body, got %d", rec.Code)
	}
}

func TestLoginRateLimitReturns429(t *testing.T) {
	f := newFixture(t)

	for i := 0; i < 5; i++ {
		f.call(t, http.MethodPost, "/api/v1/auth/login",
			loginRequest{Username: testUsername, Password: "wrong-password-entirely"}, "")
	}

	rec := f.call(t, http.MethodPost, "/api/v1/auth/login",
		loginRequest{Username: testUsername, Password: testPassword}, "")

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
	if detail := decodeError(t, rec); detail.Code != httpx.CodeRateLimited {
		t.Fatalf("unexpected error code %q", detail.Code)
	}
}

// --------------------------------------------------------------------- me

func TestMeEndpoint(t *testing.T) {
	f := newFixture(t)
	tokens := f.login(t)

	rec := f.call(t, http.MethodGet, "/api/v1/auth/me", nil, tokens.AccessToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	var profile Profile
	decodeData(t, rec, &profile)

	if profile.Username != testUsername {
		t.Fatalf("unexpected username %q", profile.Username)
	}
	if !rbac.Has(profile.Permissions, rbac.PermUserManage) {
		t.Fatalf("admin permissions missing: %v", profile.Permissions)
	}
	// The response must never carry credential material.
	if strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("profile must not mention a password: %s", rec.Body.String())
	}
}

func TestProtectedEndpointRejectsBadAuthorizationHeaders(t *testing.T) {
	f := newFixture(t)
	tokens := f.login(t)

	cases := []struct {
		name, header string
	}{
		{"missing", ""},
		{"no scheme", tokens.AccessToken},
		{"wrong scheme", "Basic " + tokens.AccessToken},
		{"empty bearer", "Bearer "},
		{"garbage token", "Bearer not-a-token"},
		{"scheme only", "Bearer"},
	}

	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
		req.RemoteAddr = "192.0.2.10:44321"
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}

		rec := httptest.NewRecorder()
		f.router().ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: expected 401, got %d", tc.name, rec.Code)
		}
	}
}

func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	f := newFixture(t)
	tokens := f.login(t)

	// RFC 7235 makes the scheme case-insensitive; clients do vary.
	for _, scheme := range []string{"Bearer", "bearer", "BEARER"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
		req.RemoteAddr = "192.0.2.10:44321"
		req.Header.Set("Authorization", scheme+" "+tokens.AccessToken)

		rec := httptest.NewRecorder()
		f.router().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("scheme %q: expected 200, got %d", scheme, rec.Code)
		}
	}
}

func TestTokenIsNotAcceptedFromQueryString(t *testing.T) {
	f := newFixture(t)
	tokens := f.login(t)

	// Tokens in URLs end up in access logs and browser history.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me?access_token="+tokens.AccessToken, nil)
	req.RemoteAddr = "192.0.2.10:44321"

	rec := httptest.NewRecorder()
	f.router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a query-string token must not authenticate, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------- refresh

func TestRefreshEndpoint(t *testing.T) {
	f := newFixture(t)
	tokens := f.login(t)

	rec := f.call(t, http.MethodPost, "/api/v1/auth/refresh",
		refreshRequest{RefreshToken: tokens.RefreshToken}, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	var pair TokenPair
	decodeData(t, rec, &pair)
	if pair.RefreshToken == tokens.RefreshToken {
		t.Fatal("the refresh token must be rotated")
	}
}

func TestRefreshEndpointValidatesInput(t *testing.T) {
	f := newFixture(t)

	rec := f.call(t, http.MethodPost, "/api/v1/auth/refresh", refreshRequest{}, "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rec.Code)
	}

	rec = f.call(t, http.MethodPost, "/api/v1/auth/refresh",
		refreshRequest{RefreshToken: "never-issued"}, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// ----------------------------------------------------------------- logout

func TestLogoutEndpoint(t *testing.T) {
	f := newFixture(t)
	tokens := f.login(t)

	rec := f.call(t, http.MethodPost, "/api/v1/auth/logout", nil, tokens.AccessToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	// The token must be dead immediately.
	rec = f.call(t, http.MethodGet, "/api/v1/auth/me", nil, tokens.AccessToken)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("the token must be revoked after logout, got %d", rec.Code)
	}
}

// ------------------------------------------------------------- two-factor

func TestTwoFactorEndpointFlow(t *testing.T) {
	f := newFixture(t)
	tokens := f.login(t)

	// Setup returns a secret and provisioning URI.
	rec := f.call(t, http.MethodPost, "/api/v1/auth/2fa/setup", nil, tokens.AccessToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup: expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var setup TwoFactorSetup
	decodeData(t, rec, &setup)
	if setup.Secret == "" || !strings.HasPrefix(setup.URI, "otpauth://totp/") {
		t.Fatalf("unexpected setup payload: %+v", setup)
	}

	// A wrong code must not enable it.
	rec = f.call(t, http.MethodPost, "/api/v1/auth/2fa/enable",
		twoFactorCodeRequest{Code: "000000"}, tokens.AccessToken)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("enable with a bad code: expected 401, got %d", rec.Code)
	}

	// The right code enables it.
	code, err := secrets.TOTPCode(setup.Secret, f.svc.now())
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	rec = f.call(t, http.MethodPost, "/api/v1/auth/2fa/enable",
		twoFactorCodeRequest{Code: code}, tokens.AccessToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable: expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	// Login now requires the second factor.
	rec = f.call(t, http.MethodPost, "/api/v1/auth/login",
		loginRequest{Username: testUsername, Password: testPassword}, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("login: expected 200, got %d", rec.Code)
	}
	var challenge loginResponse
	decodeData(t, rec, &challenge)
	if !challenge.MFARequired || challenge.MFAToken == "" {
		t.Fatalf("expected an MFA challenge, got %+v", challenge)
	}
	if challenge.AccessToken != "" || challenge.RefreshToken != "" {
		t.Fatal("no tokens may be issued before the second factor is verified")
	}

	// Verifying the code completes the login.
	code, err = secrets.TOTPCode(setup.Secret, f.svc.now())
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	rec = f.call(t, http.MethodPost, "/api/v1/auth/2fa/verify",
		verifyTwoFactorRequest{MFAToken: challenge.MFAToken, Code: code}, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("verify: expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var pair TokenPair
	decodeData(t, rec, &pair)
	if pair.AccessToken == "" {
		t.Fatal("verification must issue an access token")
	}
}

func TestTwoFactorVerifyValidatesInput(t *testing.T) {
	f := newFixture(t)

	cases := []struct {
		name string
		body verifyTwoFactorRequest
		want int
	}{
		{"missing both", verifyTwoFactorRequest{}, http.StatusUnprocessableEntity},
		{"missing code", verifyTwoFactorRequest{MFAToken: "x"}, http.StatusUnprocessableEntity},
		{"missing token", verifyTwoFactorRequest{Code: "123456"}, http.StatusUnprocessableEntity},
		{"unknown token", verifyTwoFactorRequest{MFAToken: "nope", Code: "123456"}, http.StatusUnauthorized},
	}

	for _, tc := range cases {
		rec := f.call(t, http.MethodPost, "/api/v1/auth/2fa/verify", tc.body, "")
		if rec.Code != tc.want {
			t.Fatalf("%s: expected %d, got %d", tc.name, tc.want, rec.Code)
		}
	}
}

func TestTwoFactorDisableRequiresPassword(t *testing.T) {
	f := newFixture(t)
	tokens := f.login(t)

	rec := f.call(t, http.MethodPost, "/api/v1/auth/2fa/disable",
		disableTwoFactorRequest{}, tokens.AccessToken)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 without a password, got %d", rec.Code)
	}

	rec = f.call(t, http.MethodPost, "/api/v1/auth/2fa/disable",
		disableTwoFactorRequest{Password: "wrong-password-entirely"}, tokens.AccessToken)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with a wrong password, got %d", rec.Code)
	}
}

// ------------------------------------------------------------ permissions

func TestRequirePermissionAllowsAndDenies(t *testing.T) {
	f := newFixture(t)

	// A viewer holds server.view but not user.manage.
	viewerID := f.createUser(t, "viewer", testPassword, rbac.RoleViewer)
	_ = viewerID

	mux := http.NewServeMux()
	mux.Handle("GET /guarded", f.svc.RequireAuth(
		f.svc.RequirePermission(rbac.PermUserManage)(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				httpx.OK(w, r, map[string]bool{"ok": true})
			}))))

	// The admin passes.
	adminTokens := f.login(t)
	req := httptest.NewRequest(http.MethodGet, "/guarded", nil)
	req.Header.Set("Authorization", "Bearer "+adminTokens.AccessToken)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin must be allowed, got %d", rec.Code)
	}

	// The viewer is refused with 403, not 401: they are authenticated.
	viewerRec := f.call(t, http.MethodPost, "/api/v1/auth/login",
		loginRequest{Username: "viewer", Password: testPassword}, "")
	var viewerTokens loginResponse
	decodeData(t, viewerRec, &viewerTokens)

	req = httptest.NewRequest(http.MethodGet, "/guarded", nil)
	req.Header.Set("Authorization", "Bearer "+viewerTokens.AccessToken)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer must be forbidden, got %d (%s)", rec.Code, rec.Body.String())
	}
	if detail := decodeError(t, rec); detail.Code != httpx.CodeForbidden {
		t.Fatalf("unexpected error code %q", detail.Code)
	}
}

func TestRequirePermissionWithoutAuthIsAServerError(t *testing.T) {
	f := newFixture(t)

	// Wiring RequirePermission without RequireAuth is a programming error and
	// must fail loudly rather than silently allowing the request.
	mux := http.NewServeMux()
	mux.Handle("GET /misconfigured", f.svc.RequirePermission(rbac.PermUserManage)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httpx.OK(w, r, map[string]bool{"ok": true})
		})))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/misconfigured", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "middleware") {
		t.Fatalf("internal detail must not reach the client: %s", rec.Body.String())
	}
}

func TestClientIPIgnoresForwardedHeaders(t *testing.T) {
	// X-Forwarded-For is client-controlled; honouring it would let an attacker
	// rotate the header to evade the per-IP login limit.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{}`))
	req.RemoteAddr = "192.0.2.10:44321"
	req.Header.Set("X-Forwarded-For", "203.0.113.99")

	if got := clientIP(req); got != "192.0.2.10" {
		t.Fatalf("clientIP = %q, want the peer address 192.0.2.10", got)
	}
}
