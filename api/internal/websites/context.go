package websites

import (
	"context"
	"net/http"
)

// withTimeout bounds a request's own context.
//
// The request context is the parent, so a client disconnect cancels the work
// rather than leaving queries running for a response nobody will read.
func withTimeout(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), requestTimeout)
}
