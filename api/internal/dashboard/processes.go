package dashboard

import (
	"context"
	"fmt"

	"github.com/jothost/panel/api/internal/agentclient"
)

// Process listing bounds.
//
// The host may be running thousands; nobody reads a table of thousands, and
// sending one costs the Agent a walk of all of /proc to serialise a page
// nobody scrolls. The default is what fits on a screen.
const (
	DefaultProcessLimit = 25
	MaxProcessLimit     = 200
)

// ProcessSort names an ordering the Agent understands.
const (
	SortByMemory = "memory"
	SortByCPU    = "cpu"
)

// ValidProcessSort reports whether an ordering is one the Agent implements.
//
// Checked here rather than passed through, because an unknown value would be
// silently ignored on the far side and the caller would get memory order back
// while believing they had asked for something else.
func ValidProcessSort(sort string) bool {
	return sort == SortByMemory || sort == SortByCPU
}

// Processes reports what is running on the host.
//
// The Agent already collected this and the API already had a client method for
// it; nothing had ever called it, so the PRD's "Processes" was the one part of
// its Server module with no way to reach it.
func (s *Service) Processes(ctx context.Context, requestID string, limit int, sortBy string) (agentclient.ProcessListResult, error) {
	switch {
	case limit <= 0:
		limit = DefaultProcessLimit
	case limit > MaxProcessLimit:
		limit = MaxProcessLimit
	}
	if !ValidProcessSort(sortBy) {
		sortBy = SortByMemory
	}

	result, err := s.agent.ProcessList(ctx, requestID, limit, sortBy)
	if err != nil {
		return agentclient.ProcessListResult{}, fmt.Errorf("list processes: %w", err)
	}
	return result, nil
}
