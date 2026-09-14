package auth

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/twofactor"
	"github.com/jothost/panel/api/internal/users"
)

// maxBodyBytes bounds an auth request body. These payloads are a few hundred
// bytes; anything larger is malformed or hostile.
const maxBodyBytes = 4 << 10 // 4 KiB

// Handler serves the /auth endpoints.
type Handler struct {
	service *Service
}

// NewHandler builds a Handler.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// Routes registers the auth endpoints on mux.
//
// login, refresh, and 2fa/verify are public by necessity: they are how a
// caller obtains credentials. Everything else sits behind RequireAuth.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/login", h.login)
	mux.HandleFunc("POST /api/v1/auth/refresh", h.refresh)
	mux.HandleFunc("POST /api/v1/auth/2fa/verify", h.verifyTwoFactor)

	authed := h.service.RequireAuth
	mux.Handle("POST /api/v1/auth/logout", authed(http.HandlerFunc(h.logout)))
	mux.Handle("GET /api/v1/auth/me", authed(http.HandlerFunc(h.me)))
	mux.Handle("POST /api/v1/auth/2fa/setup", authed(http.HandlerFunc(h.setupTwoFactor)))
	mux.Handle("POST /api/v1/auth/2fa/enable", authed(http.HandlerFunc(h.enableTwoFactor)))
	mux.Handle("POST /api/v1/auth/2fa/disable", authed(http.HandlerFunc(h.disableTwoFactor)))
	mux.Handle("POST /api/v1/auth/2fa/recovery-codes", authed(http.HandlerFunc(h.regenerateRecoveryCodes)))
}

// decodeJSON reads a bounded, strict JSON body.
//
// Unknown fields are rejected so a typo in a client payload surfaces as an
// error instead of being silently ignored.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return httpx.BadRequest("Request body is too large")
		}
		return httpx.BadRequest("Request body is not valid JSON")
	}

	// Reject trailing content: a body containing two JSON documents is
	// ambiguous about which one was intended.
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return httpx.BadRequest("Request body must contain a single JSON object")
	}
	return nil
}

// requestContext captures the client attributes recorded on auth events.
func requestContext(r *http.Request) RequestContext {
	return RequestContext{
		IPAddress: clientIP(r),
		UserAgent: r.UserAgent(),
	}
}

// clientIP returns the peer address.
//
// X-Forwarded-For is deliberately ignored: it is client-controlled, and
// trusting it would let an attacker rotate the header to bypass the per-IP
// login limit. A future phase can honour it behind an explicit
// trusted-proxy configuration.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	MFARequired  bool   `json:"mfa_required,omitempty"`
	MFAToken     string `json:"mfa_token,omitempty"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}

	if strings.TrimSpace(req.Username) == "" || req.Password == "" {
		httpx.Error(w, r, httpx.ValidationFailed("Username and password are required"))
		return
	}

	result, err := h.service.Login(r.Context(), req.Username, req.Password, requestContext(r))
	if err != nil {
		httpx.Error(w, r, authError(err))
		return
	}

	if result.MFARequired {
		httpx.OK(w, r, loginResponse{MFARequired: true, MFAToken: result.MFAToken})
		return
	}

	httpx.OK(w, r, loginResponse{
		AccessToken:  result.Tokens.AccessToken,
		RefreshToken: result.Tokens.RefreshToken,
		TokenType:    result.Tokens.TokenType,
		ExpiresIn:    result.Tokens.ExpiresIn,
	})
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := decodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if req.RefreshToken == "" {
		httpx.Error(w, r, httpx.ValidationFailed("refresh_token is required"))
		return
	}

	pair, err := h.service.Refresh(r.Context(), req.RefreshToken, requestContext(r))
	if err != nil {
		httpx.Error(w, r, authError(err))
		return
	}
	httpx.OK(w, r, pair)
}

type verifyTwoFactorRequest struct {
	MFAToken string `json:"mfa_token"`
	Code     string `json:"code"`
	// RecoveryCode is sent instead of Code by somebody without their
	// authenticator. Exactly one of the two is accepted.
	RecoveryCode string `json:"recovery_code"`
}

func (h *Handler) verifyTwoFactor(w http.ResponseWriter, r *http.Request) {
	var req verifyTwoFactorRequest
	if err := decodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if req.MFAToken == "" || (req.Code == "") == (req.RecoveryCode == "") {
		httpx.Error(w, r, httpx.ValidationFailed("mfa_token and exactly one of code or recovery_code are required"))
		return
	}

	var (
		pair *TokenPair
		err  error
	)
	if req.RecoveryCode != "" {
		pair, err = h.service.VerifyRecoveryCode(r.Context(), req.MFAToken, req.RecoveryCode, requestContext(r))
	} else {
		pair, err = h.service.VerifyTwoFactor(r.Context(), req.MFAToken, req.Code, requestContext(r))
	}
	if err != nil {
		httpx.Error(w, r, authError(err))
		return
	}
	httpx.OK(w, r, pair)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	claims, ok := ClaimsFromContext(r.Context())
	if !ok {
		httpx.Error(w, r, httpx.Unauthorized("Authentication required"))
		return
	}

	if err := h.service.Logout(r.Context(), claims, requestContext(r)); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.OK(w, r, map[string]bool{"logged_out": true})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	claims, ok := ClaimsFromContext(r.Context())
	if !ok {
		httpx.Error(w, r, httpx.Unauthorized("Authentication required"))
		return
	}

	profile, err := h.service.Me(r.Context(), claims.UserID)
	if err != nil {
		if errors.Is(err, users.ErrNotFound) {
			httpx.Error(w, r, httpx.Unauthorized("Invalid or expired token"))
			return
		}
		httpx.Error(w, r, err)
		return
	}
	httpx.OK(w, r, profile)
}

func (h *Handler) setupTwoFactor(w http.ResponseWriter, r *http.Request) {
	claims, ok := ClaimsFromContext(r.Context())
	if !ok {
		httpx.Error(w, r, httpx.Unauthorized("Authentication required"))
		return
	}

	setup, err := h.service.SetupTwoFactor(r.Context(), claims.UserID, requestContext(r))
	if err != nil {
		httpx.Error(w, r, authError(err))
		return
	}
	httpx.OK(w, r, setup)
}

type twoFactorCodeRequest struct {
	Code string `json:"code"`
}

func (h *Handler) enableTwoFactor(w http.ResponseWriter, r *http.Request) {
	claims, ok := ClaimsFromContext(r.Context())
	if !ok {
		httpx.Error(w, r, httpx.Unauthorized("Authentication required"))
		return
	}

	var req twoFactorCodeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if req.Code == "" {
		httpx.Error(w, r, httpx.ValidationFailed("code is required"))
		return
	}

	codes, err := h.service.EnableTwoFactor(r.Context(), claims.UserID, req.Code, requestContext(r))
	if err != nil {
		httpx.Error(w, r, authError(err))
		return
	}
	httpx.OK(w, r, map[string]any{"two_factor_enabled": true, "recovery_codes": codes})
}

type recoveryCodesRequest struct {
	Password string `json:"password"`
}

func (h *Handler) regenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	claims, ok := ClaimsFromContext(r.Context())
	if !ok {
		httpx.Error(w, r, httpx.Unauthorized("Authentication required"))
		return
	}

	var req recoveryCodesRequest
	if err := decodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if req.Password == "" {
		httpx.Error(w, r, httpx.ValidationFailed("password is required"))
		return
	}

	codes, err := h.service.RegenerateRecoveryCodes(r.Context(), claims.UserID, req.Password, requestContext(r))
	if err != nil {
		httpx.Error(w, r, authError(err))
		return
	}
	httpx.OK(w, r, map[string]any{"recovery_codes": codes})
}

type disableTwoFactorRequest struct {
	Password string `json:"password"`
}

func (h *Handler) disableTwoFactor(w http.ResponseWriter, r *http.Request) {
	claims, ok := ClaimsFromContext(r.Context())
	if !ok {
		httpx.Error(w, r, httpx.Unauthorized("Authentication required"))
		return
	}

	var req disableTwoFactorRequest
	if err := decodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if req.Password == "" {
		httpx.Error(w, r, httpx.ValidationFailed("password is required"))
		return
	}

	if err := h.service.DisableTwoFactor(r.Context(), claims.UserID, req.Password, requestContext(r)); err != nil {
		httpx.Error(w, r, authError(err))
		return
	}
	httpx.OK(w, r, map[string]bool{"two_factor_enabled": false})
}

// authError maps service errors to the API error contract.
//
// Everything unmapped falls through to a generic internal error, so a new
// error type cannot accidentally leak its message to clients.
func authError(err error) error {
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		return httpx.Unauthorized("Invalid username or password")
	case errors.Is(err, ErrAccountInactive):
		return httpx.Forbidden("This account is not active")
	case errors.Is(err, ErrRateLimited):
		return httpx.New(http.StatusTooManyRequests, httpx.CodeRateLimited,
			"Too many attempts. Try again later.")
	case errors.Is(err, ErrInvalidToken):
		return httpx.Unauthorized("Invalid or expired token")
	case errors.Is(err, ErrInvalidTOTP):
		return httpx.Unauthorized("Invalid verification code")
	case errors.Is(err, ErrInvalidRecoveryCode):
		return httpx.Unauthorized("Invalid or already used recovery code")
	case errors.Is(err, twofactor.ErrNotEnabled):
		return httpx.BadRequest("Two-factor authentication is not enabled")
	case errors.Is(err, ErrTwoFactorEnabled):
		return httpx.Conflict("Two-factor authentication is already enabled")
	case errors.Is(err, twofactor.ErrNotEnrolled):
		return httpx.BadRequest("Two-factor authentication has not been set up")
	default:
		return err
	}
}
