package server

import (
	"context"
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

// checkPostgres runs a trivial query against the pool.
//
// Phase 1 replaced the Phase 0 TCP dial with a real round trip: a database
// that accepts connections but rejects authentication now reports down, which
// a dial could not detect.
func (s *Server) checkPostgres(ctx context.Context) checkResult {
	ctx, cancel := context.WithTimeout(ctx, dependencyTimeout)
	defer cancel()

	var one int
	if err := s.pool.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
		// The driver error can embed the connection string, so only a generic
		// reason is reported.
		s.log.Warn("postgres readiness probe failed", "error", err.Error())
		return checkResult{Status: statusDown, Error: "query failed"}
	}
	return checkResult{Status: statusUp}
}

// checkDependencyHealth reports whether an API-side dependency is usable.
//
// The dashboard asks through this rather than reaching into the pools, so both
// the readiness endpoint and the services widget answer from the same probe
// and cannot disagree.
func (s *Server) checkDependencyHealth(ctx context.Context, name string) bool {
	switch name {
	case "postgres":
		return s.checkPostgres(ctx).Status == statusUp
	case "redis":
		return s.checkRedis(ctx).Status == statusUp
	default:
		return false
	}
}

// checkRedis pings the cache.
func (s *Server) checkRedis(ctx context.Context) checkResult {
	ctx, cancel := context.WithTimeout(ctx, dependencyTimeout)
	defer cancel()

	if err := s.redis.Ping(ctx).Err(); err != nil {
		s.log.Warn("redis readiness probe failed", "error", err.Error())
		return checkResult{Status: statusDown, Error: "ping failed"}
	}
	return checkResult{Status: statusUp}
}
