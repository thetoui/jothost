// Package logs serves the host's log files to the panel.
//
// The panel holds no logs of its own: they are files on the host, and the Agent
// is the only thing that may read them (CLAUDE.md section 7). This package is
// the boundary that decides who may look, records that they took a copy, and
// turns the Agent's answers into the panel's shapes.
//
// It never names a file. A request carries a *source key* from the Agent's
// catalogue and nothing else, which is what keeps a log viewer from being a
// file reader with a nicer name.
package logs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/audit"
)

// Audit actions. Reading a log on screen is not recorded — it is a read of
// something already visible to anyone with the permission, and an audit trail
// that fills with page views is one nobody reads. Taking a *copy* is recorded:
// a downloaded access log leaves the machine, and "who took a copy of the
// authentication log" is a question worth being able to answer.
const (
	ActionDownload     = "log.download"
	ResourceTypeServer = "server"
)

// Errors returned by this package.
var (
	// ErrUnavailable means the host cannot read logs at all.
	ErrUnavailable = errors.New("this host cannot read logs")
	// ErrInvalidOption means a caller asked for something outside the bounds.
	ErrInvalidOption = errors.New("invalid log option")
)

// Bounds enforced here as well as in the Agent.
//
// Not because the Agent's checks are in doubt, but because a rejection an
// operator can read — "at most 2000 lines" — belongs at the edge that has the
// request in front of it, and because the API should not spend a socket
// round-trip discovering that a query string was nonsense.
const (
	MaxLines        = 2000
	DefaultLines    = 200
	MaxSearchLength = 200
)

// Service reads the host's logs.
type Service struct {
	agent    *agentclient.Client
	audit    *audit.Recorder
	log      *slog.Logger
	serverID string
}

// ServiceOptions configure a Service.
type ServiceOptions struct {
	Agent    *agentclient.Client
	Audit    *audit.Recorder
	Log      *slog.Logger
	ServerID string
}

// NewService builds a Service.
func NewService(opts ServiceOptions) *Service {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		agent:    opts.Agent,
		audit:    opts.Audit,
		log:      log,
		serverID: opts.ServerID,
	}
}

// Actor identifies who asked, for the audit trail.
type Actor struct {
	UserID    string
	IPAddress string
	UserAgent string
}

// Sources reports which logs this host has.
func (s *Service) Sources(ctx context.Context, requestID string) (agentclient.LogSourceList, error) {
	return s.agent.LogList(ctx, requestID)
}

// Tail returns the end of one log, or what is new since an offset.
func (s *Service) Tail(ctx context.Context, requestID, source string,
	opts agentclient.LogTailOptions,
) (agentclient.LogTailResult, error) {
	if err := ValidateOptions(opts); err != nil {
		return agentclient.LogTailResult{}, err
	}
	return s.agent.LogTail(ctx, requestID, source, opts)
}

// ValidateOptions checks what a caller asked for before it costs a round trip.
func ValidateOptions(opts agentclient.LogTailOptions) error {
	if opts.Lines < 0 || opts.Lines > MaxLines {
		return fmt.Errorf("%w: lines must be between 1 and %d", ErrInvalidOption, MaxLines)
	}
	if len(opts.Search) > MaxSearchLength {
		return fmt.Errorf("%w: the search text may be at most %d characters",
			ErrInvalidOption, MaxSearchLength)
	}
	if opts.After < 0 {
		return fmt.Errorf("%w: the offset cannot be negative", ErrInvalidOption)
	}
	switch strings.ToLower(opts.Level) {
	case "", "error", "warn", "info", "debug":
	default:
		return fmt.Errorf("%w: %q is not a level", ErrInvalidOption, opts.Level)
	}
	return nil
}

// Download streams a whole log to w, in the bytes the daemon wrote.
//
// The audit record is written before the first byte moves rather than after the
// last one: a download that fails halfway still took a copy of everything up to
// the failure, and an audit trail that only records completed transfers is one
// that can be defeated by hanging up.
func (s *Service) Download(ctx context.Context, requestID, source string,
	actor Actor, w io.Writer,
) (int64, error) {
	s.record(ctx, actor, ActionDownload, audit.StatusSuccess, map[string]any{
		"source": source,
	})

	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			return offset, err
		}

		chunk, err := s.agent.LogRead(ctx, requestID, source, offset, agentclient.MaxChunkBytes)
		if err != nil {
			return offset, err
		}

		if len(chunk.Data) > 0 {
			if _, err := w.Write(chunk.Data); err != nil {
				// The client hung up. That is not a server error, and the loop
				// must stop rather than keep pulling chunks nobody will read.
				return offset, fmt.Errorf("write to client: %w", err)
			}
			offset += int64(len(chunk.Data))
		}

		if chunk.EOF {
			return offset, nil
		}
		if len(chunk.Data) == 0 {
			// No progress and no end would loop forever. This should be
			// unreachable; it is here so a bug in the Agent cannot pin a core
			// on the API.
			return offset, errors.New("the agent returned an empty chunk before the end of the log")
		}
	}
}

// record writes an audit event, logging rather than failing the request.
func (s *Service) record(ctx context.Context, actor Actor, action, status string,
	metadata map[string]any,
) {
	if s.audit == nil {
		return
	}
	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       actor.UserID,
		Action:       action,
		ResourceType: ResourceTypeServer,
		ResourceID:   s.serverID,
		IPAddress:    actor.IPAddress,
		UserAgent:    actor.UserAgent,
		Status:       status,
		Metadata:     metadata,
	})
}
