package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jothost/panel/shared/logger"
)

// LongRequest is context.WithTimeout for a handler that is expected to outlive
// the server's own read and write timeouts.
//
// The API's http.Server closes a connection API_READ_TIMEOUT after it starts
// reading a request body and API_WRITE_TIMEOUT after it starts on a request,
// whatever the handler is doing. Those limits are short on purpose — they are
// what stops a slow client from holding a connection open — but they sit on
// the connection, not the context. So a handler that wrapped r.Context() in a
// thirty-minute timeout for a database export still had its response cut off
// at thirty seconds: the dump finished, and there was no connection left to
// send it down. An import had its body stop mid-stream after fifteen, part of
// a SQL file applied and the rest never read.
//
// LongRequest extends both deadlines on this one connection to match the
// context it returns, so the work, the upload, and the response all get the
// same budget. Every other request keeps the short server-wide limits.
func LongRequest(w http.ResponseWriter, r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(d)

	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(deadline); err != nil {
		logDeadlineError(r, "write", err)
	}
	if err := rc.SetReadDeadline(deadline); err != nil {
		logDeadlineError(r, "read", err)
	}

	return context.WithDeadline(r.Context(), deadline)
}

// logDeadlineError records a deadline that could not be extended.
//
// It is not returned to the caller because the request can still succeed if it
// happens to finish inside the server's limits — but it is logged loudly,
// because the usual cause is a ResponseWriter wrapper that forgot Unwrap, and
// that silently puts every long handler back under the short limits.
func logDeadlineError(r *http.Request, which string, err error) {
	logger.FromContext(r.Context(), slog.Default()).Error(
		"could not extend the connection deadline for a long request",
		"deadline", which,
		"path", r.URL.Path,
		logger.KeyError, err.Error(),
		logger.KeyRequestID, RequestIDFromContext(r.Context()),
	)
}
