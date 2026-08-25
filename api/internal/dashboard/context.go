package dashboard

import (
	"context"
	"net/http"
	"time"
)

// contextWithTimeout bounds a request's own context.
//
// The request context is used as the parent so a client disconnect still
// cancels the work, rather than leaving agent calls running for a response
// nobody will read.
func contextWithTimeout(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), timeout)
}
