package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/shared/logger"
)

type claimsContextKey struct{}

// ContextWithClaims stores the authenticated identity on ctx.
func ContextWithClaims(ctx context.Context, claims AccessClaims) context.Context {
	return context.WithValue(ctx, claimsContextKey{}, claims)
}

// ClaimsFromContext returns the authenticated identity, if any.
func ClaimsFromContext(ctx context.Context) (AccessClaims, bool) {
	claims, ok := ctx.Value(claimsContextKey{}).(AccessClaims)
	return claims, ok
}

// bearerToken extracts a token from an Authorization header.
//
// Only the Bearer scheme is accepted, and only from this header: tokens are
// never read from query strings, where they would end up in access logs and
// browser history.
func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if header == "" {
		return ""
	}

	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// RequireAuth rejects requests without a valid access token and puts the
// caller's claims on the request context.
func (s *Service) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			httpx.Error(w, r, httpx.Unauthorized("Authentication required"))
			return
		}

		claims, err := s.Authenticate(r.Context(), token)
		if err != nil {
			if errors.Is(err, ErrInvalidToken) {
				httpx.Error(w, r, httpx.Unauthorized("Invalid or expired token"))
				return
			}
			// An infrastructure failure must not be reported as a rejected
			// credential: that would mask an outage as an auth problem.
			logger.FromContext(r.Context(), s.log).Error("authentication failed",
				logger.KeyError, err.Error())
			httpx.Error(w, r, httpx.Internal(err))
			return
		}

		ctx := ContextWithClaims(r.Context(), claims)
		// Bind the identity to the request's log lines.
		scoped := logger.FromContext(ctx, s.log).With(logger.KeyUserID, claims.UserID)
		ctx = logger.WithContext(ctx, scoped)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequirePermission rejects callers lacking the named permission.
//
// It must be used behind RequireAuth. A missing identity is treated as a
// server error rather than a 403, because reaching here unauthenticated means
// the route was wired wrongly.
func (s *Service) RequirePermission(permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := ClaimsFromContext(r.Context())
			if !ok {
				logger.FromContext(r.Context(), s.log).Error(
					"RequirePermission used without RequireAuth",
					"permission", permission,
					"path", r.URL.Path,
				)
				httpx.Error(w, r, httpx.Internal(errors.New("missing authentication middleware")))
				return
			}

			if !rbac.Has(claims.Permissions, permission) {
				logger.FromContext(r.Context(), s.log).Warn("permission denied",
					logger.KeyUserID, claims.UserID,
					"permission", permission,
					"path", r.URL.Path,
				)
				// The response names the required permission: the caller is
				// authenticated, and knowing what they lack is useful without
				// disclosing anything about other users or resources.
				httpx.Error(w, r, httpx.Forbidden("Permission denied: "+permission))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
