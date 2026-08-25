package server

import (
	"context"
	"net"
	"net/url"
	"strings"
	"time"
)

// checkResult is the per-dependency readiness outcome.
type checkResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

const (
	statusUp   = "up"
	statusDown = "down"
)

// dependencyTimeout bounds each individual readiness probe so a hung
// dependency cannot stall the readiness endpoint.
const dependencyTimeout = 3 * time.Second

// checkTCP reports whether the host:port encoded in rawURL accepts a
// connection.
//
// Phase 0 deliberately probes at the TCP layer: the API has no database or
// Redis driver yet (no schema exists before Phase 1). Phase 1 replaces this
// with a real `SELECT 1` / `PING` once the pools are introduced.
func checkTCP(ctx context.Context, rawURL string, defaultPort string) checkResult {
	address, err := addressFromURL(rawURL, defaultPort)
	if err != nil {
		return checkResult{Status: statusDown, Error: "invalid connection URL"}
	}

	ctx, cancel := context.WithTimeout(ctx, dependencyTimeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		// The error text can embed credentials from the URL, so only a
		// generic reason is reported.
		return checkResult{Status: statusDown, Error: "connection refused"}
	}
	_ = conn.Close()
	return checkResult{Status: statusUp}
}

// addressFromURL extracts host:port from a connection URL without exposing the
// embedded credentials.
func addressFromURL(rawURL, defaultPort string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}

	host := parsed.Hostname()
	if host == "" {
		return "", &url.Error{Op: "parse", URL: "redacted", Err: errEmptyHost}
	}
	port := parsed.Port()
	if strings.TrimSpace(port) == "" {
		port = defaultPort
	}
	return net.JoinHostPort(host, port), nil
}
