package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Entry is one recorded action, as it is read back.
//
// It is not Event. Event is what a caller hands the recorder; this is what the
// database holds, which has an id, a timestamp, and the actor's username
// joined in — because "who" is the first question anybody asks of an audit
// trail and a UUID does not answer it.
type Entry struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	// UserID is null for an action by an unidentified caller: a failed login
	// against a username that does not exist, or an account deleted since.
	UserID *string `json:"user_id"`
	// Username is joined from users, and is null for the same reasons plus one
	// more — the account was deleted after acting. The trail keeps the row; it
	// cannot keep the name.
	Username     *string        `json:"username"`
	ResourceType *string        `json:"resource_type"`
	ResourceID   *string        `json:"resource_id"`
	IPAddress    *string        `json:"ip_address"`
	UserAgent    *string        `json:"user_agent"`
	Status       *string        `json:"status"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}

// ListParams filters a reading of the trail.
type ListParams struct {
	Action       string
	UserID       string
	ResourceType string
	ResourceID   string
	Status       string
	Since        *time.Time
	Until        *time.Time
	Limit        int
	Offset       int
}

// Paging bounds. A page is capped rather than trusted: this table grows for
// the life of the installation and never shrinks, so an unbounded read is a
// way to ask the panel to load its own history into memory.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Reader reads the audit trail.
//
// Deliberately separate from Recorder, and holding no method that writes. The
// table is append-only by trigger; this type is the same statement in Go, so a
// future change cannot casually add an update path through the reading side.
type Reader struct {
	pool *pgxpool.Pool
}

// NewReader builds a Reader.
func NewReader(pool *pgxpool.Pool) *Reader { return &Reader{pool: pool} }

// List returns entries newest first, with the total number that matched.
//
// The count is returned alongside the page because a trail is read to answer a
// question, and "42 of 9,318 refusals" is a different answer from "42".
func (r *Reader) List(ctx context.Context, params ListParams) ([]Entry, int, error) {
	where, args := buildFilter(params)

	limit := params.Limit
	switch {
	case limit <= 0:
		limit = DefaultLimit
	case limit > MaxLimit:
		limit = MaxLimit
	}
	offset := params.Offset
	if offset < 0 {
		offset = 0
	}

	var total int
	countSQL := "SELECT count(*) FROM audit_logs a" + where
	if err := r.pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count audit entries: %w", err)
	}

	// The placeholders continue the filter's numbering rather than being
	// hard-coded: the filter is built from whichever parameters were supplied,
	// so $1 and $2 mean something different on every call.
	listSQL := `
		SELECT a.id, a.action, a.user_id, u.username, a.resource_type, a.resource_id,
		       host(a.ip_address), a.user_agent, a.status, a.metadata, a.created_at
		FROM audit_logs a
		LEFT JOIN users u ON u.id = a.user_id` + where +
		fmt.Sprintf(" ORDER BY a.created_at DESC, a.id DESC LIMIT $%d OFFSET $%d",
			len(args)+1, len(args)+2)

	rows, err := r.pool.Query(ctx, listSQL, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list audit entries: %w", err)
	}
	defer rows.Close()

	entries := make([]Entry, 0, limit)
	for rows.Next() {
		var entry Entry
		var metadata []byte
		if err := rows.Scan(&entry.ID, &entry.Action, &entry.UserID, &entry.Username,
			&entry.ResourceType, &entry.ResourceID, &entry.IPAddress, &entry.UserAgent,
			&entry.Status, &metadata, &entry.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan audit entry: %w", err)
		}
		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &entry.Metadata); err != nil {
				// A row whose metadata will not parse is still a real event,
				// and dropping it would put a hole in the trail to protect a
				// field. The event is returned without it.
				entry.Metadata = nil
			}
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read audit entries: %w", err)
	}
	return entries, total, nil
}

// Actions returns the distinct action names present, for populating a filter.
//
// Read from the trail rather than from the constants above, because the
// constants are only the ones this package declares — every other package
// appends its own, and a filter offering names nothing ever recorded is worse
// than no filter.
func (r *Reader) Actions(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT DISTINCT action FROM audit_logs ORDER BY action`)
	if err != nil {
		return nil, fmt.Errorf("list audit actions: %w", err)
	}
	defer rows.Close()

	actions := make([]string, 0, 32)
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			return nil, fmt.Errorf("scan audit action: %w", err)
		}
		actions = append(actions, action)
	}
	return actions, rows.Err()
}

// buildFilter turns params into a WHERE clause and its arguments.
//
// Every value is a placeholder. The only strings concatenated into the SQL are
// column names this function writes itself, which is the rule the whole API
// follows: nothing a caller supplies is ever part of the statement.
func buildFilter(params ListParams) (string, []any) {
	var clauses []string
	var args []any

	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}

	if params.Action != "" {
		add("a.action = $%d", params.Action)
	}
	if params.UserID != "" {
		add("a.user_id = $%d", params.UserID)
	}
	if params.ResourceType != "" {
		add("a.resource_type = $%d", params.ResourceType)
	}
	if params.ResourceID != "" {
		add("a.resource_id = $%d", params.ResourceID)
	}
	if params.Status != "" {
		add("a.status = $%d", params.Status)
	}
	if params.Since != nil {
		add("a.created_at >= $%d", *params.Since)
	}
	if params.Until != nil {
		add("a.created_at <= $%d", *params.Until)
	}

	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}
