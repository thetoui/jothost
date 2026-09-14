package operations

import (
	"context"
	"encoding/base64"
	"errors"

	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/logs"
	"github.com/jothost/panel/shared/protocol"
)

// The log operations' request boundary.
//
// A request names a **key** from the Agent's catalogue. The path it becomes is
// chosen in agent/internal/logs, from a table in this repository, so no value a
// client sends can name the file that gets read. It is the same rule the
// service manager follows, and it is load-bearing in the same way: the
// difference between a log viewer and an unrestricted file reader is entirely
// this decision.

// logPayload carries what every log operation may need.
type logPayload struct {
	// Source is a catalogue key, not a path.
	Source string `json:"source"`
	Lines  int    `json:"lines"`
	Search string `json:"search"`
	Level  string `json:"level"`
	After  int64  `json:"after"`

	// Offset and Length are for a download, which reads raw bytes rather than
	// parsed lines.
	Offset int64 `json:"offset"`
	Length int   `json:"length"`
}

// logProvider returns the provider, or the error a caller should see when log
// reading is not configured.
func (r *Registry) logProvider() (*logs.Provider, error) {
	if r.deps.Logs == nil || !r.deps.Logs.Available() {
		return nil, Fail(protocol.CodeUnsupported,
			"Log reading is not available on this host", nil)
	}
	return r.deps.Logs, nil
}

// logSources are the entries contributed at runtime.
//
// PHP versions and Node applications are not knowable when the catalogue is
// written, so they are gathered here, on each call, from what the host has now.
// A version uninstalled a minute ago should stop being offered a minute ago.
func (r *Registry) logSources(ctx context.Context) []logs.Source {
	var extra []logs.Source

	if r.deps.PHP != nil && r.deps.PHP.Available() {
		names := make([]string, 0, 4)
		for _, version := range r.deps.PHP.Detect(ctx) {
			if version.Installed {
				names = append(names, version.Version)
			}
		}
		extra = append(extra, logs.PHPSources(names)...)
	}

	extra = append(extra, logs.NodeSources()...)

	// Each scheduled job's output is a log source too. The names come from the
	// index the cron provider writes beside the files, because a picker listing
	// identifiers is a picker nobody can use.
	if r.deps.Cron != nil {
		names, err := r.deps.Cron.Names()
		if err != nil {
			r.log.Warn("the scheduled job names could not be read for the log catalogue",
				"error", err.Error())
		}
		extra = append(extra, logs.CronSources(names)...)
	}

	// Each website's own access and error logs. They are not in the static
	// catalogue: they belong to a site rather than to the host, and the API
	// serves them through the website routes so that seeing one site's logs
	// does not mean seeing every log on the machine.
	if r.deps.Sites != nil {
		domains, err := r.deps.Sites.Domains()
		if err != nil {
			r.log.Warn("the website list could not be read for the log catalogue",
				"error", err.Error())
		}
		extra = append(extra, logs.SiteSources(r.deps.Sites.Root(), domains)...)
	}

	return extra
}

// handleLogList reports the logs this host has.
func (r *Registry) handleLogList(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.logProvider()
	if err != nil {
		return nil, err
	}
	// No payload: which logs a host has is not something a caller influences.
	// An empty struct means a request carrying anything is refused rather than
	// silently ignored.
	var payload struct{}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	detected := provider.Detect(r.logSources(ctx))

	entries := make([]map[string]any, 0, len(detected))
	for _, entry := range detected {
		converted, err := structToMap(entry)
		if err != nil {
			return nil, err
		}
		entries = append(entries, converted)
	}

	return map[string]any{
		"sources": entries,
		"count":   len(entries),
		"levels":  logs.Levels(),
	}, nil
}

// handleLogTail returns the end of one log, or what is new since an offset.
func (r *Registry) handleLogTail(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.logProvider()
	if err != nil {
		return nil, err
	}
	var payload logPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	source, err := logs.Lookup(payload.Source, r.logSources(ctx))
	if err != nil {
		return nil, logError(err)
	}

	result, err := provider.Tail(source, logs.Options{
		Lines:  payload.Lines,
		Search: payload.Search,
		Level:  payload.Level,
		After:  payload.After,
	})
	if err != nil {
		return nil, logError(err)
	}
	return structToMap(result)
}

// handleLogRead returns one chunk of a log's raw bytes, base64-encoded.
//
// Raw, because a download is for feeding to something else — grep, an incident
// report, a colleague — and a file reformatted on the way out is not the file
// the daemon wrote.
func (r *Registry) handleLogRead(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.logProvider()
	if err != nil {
		return nil, err
	}
	var payload logPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	source, err := logs.Lookup(payload.Source, r.logSources(ctx))
	if err != nil {
		return nil, logError(err)
	}

	data, eof, size, err := provider.Chunk(source, payload.Offset, payload.Length)
	if err != nil {
		return nil, logError(err)
	}

	return map[string]any{
		"source": source.Key,
		"offset": payload.Offset,
		"size":   size,
		"eof":    eof,
		"length": len(data),
		"data":   base64.StdEncoding.EncodeToString(data),
	}, nil
}

// logError turns a package error into the code the caller should see.
//
// An unknown key and a log this host does not have are deliberately different
// answers: the first is a caller naming something that does not exist anywhere,
// the second is a fact about this machine that the panel should state plainly.
func logError(err error) error {
	switch {
	case errors.Is(err, logs.ErrUnknownSource):
		return Fail(protocol.CodeNotFound, "That log is not one this panel reads", err)
	case errors.Is(err, logs.ErrNotPresent):
		return Fail(protocol.CodeNotFound, "This host does not have that log", err)
	case errors.Is(err, logs.ErrInvalidOption):
		return Fail(protocol.CodeInvalidPayload, err.Error(), err)
	default:
		return err
	}
}
