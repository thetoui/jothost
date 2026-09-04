// Package notifications delivers what the rest of the panel finds out.
//
// # The failure this phase is built around
//
// A notification system cannot report its own failure through itself. When
// email delivery is broken, the email saying "email delivery is broken" does not
// arrive — and the operator's experience is silence, which is indistinguishable
// from a machine with nothing wrong.
//
// So every attempt is a row. The panel shows what was delivered and what was
// not, a channel counts its consecutive failures, and one that has never
// succeeded is shown as untested rather than as working. A channel nobody has
// ever delivered through is the most dangerous object here for the same reason
// an unreached backup destination was in Phase 14: it looks like protection.
//
// # Why an outbox
//
// Each phase writes an event; a dispatcher delivers it. Two reasons, and the
// second is the one that matters: a phase calling a sender directly would block
// its own loop on somebody's slow SMTP server, and a process that died between
// "the alert opened" and "the mail was sent" would lose the notification with
// nothing recording it had existed.
//
// # Why the flooding is handled in an index
//
// One event per thing that happened, enforced by a unique index on the dedupe
// key. A disk sitting above its threshold for a week is one event or it is ten
// thousand emails, and which one it is depends on a constraint rather than on
// every caller remembering. Two API processes racing would each pass a check in
// Go; neither gets past the index.
package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Errors returned by the repository.
var (
	// ErrNotFound means no such channel, event or delivery.
	ErrNotFound = errors.New("not found")
	// ErrNameTaken means a channel of that name already exists.
	ErrNameTaken = errors.New("a channel with that name already exists")
	// ErrDuplicateEvent means this thing has already been raised. It is not a
	// failure: it is the deduplication working, and callers treat it as such.
	ErrDuplicateEvent = errors.New("this event has already been raised")
)

// Delivery statuses.
const (
	StatusPending = "pending"
	StatusSent    = "sent"
	StatusFailed  = "failed"
)

// Channel is somewhere notifications go.
//
// Credentials are deliberately absent: this struct is what handlers serialise,
// and a field that is only sometimes cleared is a field that will one day be
// returned. An SMTP password and a bot token are both full credentials.
type Channel struct {
	ID       string         `json:"id"`
	ServerID string         `json:"server_id"`
	Name     string         `json:"name"`
	Kind     string         `json:"kind"`
	Config   map[string]any `json:"config"`

	Enabled     bool     `json:"enabled"`
	MinSeverity string   `json:"min_severity"`
	Kinds       []string `json:"kinds"`

	LastSuccessAt *time.Time `json:"last_success_at"`
	LastFailureAt *time.Time `json:"last_failure_at"`
	LastError     *string    `json:"last_error"`
	FailureStreak int        `json:"failure_streak"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Healthy reports whether the panel has reason to believe this channel works.
//
// A channel that has never succeeded is *not* healthy, however few times it has
// failed. "We have never got a message through this" and "this is fine" are
// different facts, and only one of them should be shown in green.
func (c Channel) Healthy() bool {
	return c.LastSuccessAt != nil && c.FailureStreak == 0
}

// Event is something that happened which somebody may want to hear about.
type Event struct {
	ID       string `json:"id"`
	ServerID string `json:"server_id"`

	Source   string `json:"source"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`

	Title string `json:"title"`
	Body  string `json:"body"`
	Link  string `json:"link,omitempty"`

	DedupeKey string         `json:"dedupe_key"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// Delivery is one attempt to get one event to one channel.
type Delivery struct {
	ID        string `json:"id"`
	EventID   string `json:"event_id"`
	ChannelID string `json:"channel_id"`

	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	NextAttemptAt time.Time  `json:"next_attempt_at"`
	LastError     *string    `json:"last_error"`
	SentAt        *time.Time `json:"sent_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Joined for a listing, so the page can name what was sent where without a
	// query per row.
	ChannelName string `json:"channel_name,omitempty"`
	ChannelKind string `json:"channel_kind,omitempty"`
	EventTitle  string `json:"event_title,omitempty"`
	EventKind   string `json:"event_kind,omitempty"`
	Severity    string `json:"severity,omitempty"`
}

// Repository reads and writes the notification tables.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// ---------------------------------------------------------------- channels

const channelColumns = `
	c.id::text, c.server_id::text, c.name, c.kind, c.config,
	c.enabled, c.min_severity, c.kinds,
	c.last_success_at, c.last_failure_at, c.last_error, c.failure_streak,
	c.created_at, c.updated_at`

func scanChannel(row pgx.Row) (Channel, error) {
	var (
		channel Channel
		config  []byte
	)
	err := row.Scan(&channel.ID, &channel.ServerID, &channel.Name, &channel.Kind,
		&config, &channel.Enabled, &channel.MinSeverity, &channel.Kinds,
		&channel.LastSuccessAt, &channel.LastFailureAt, &channel.LastError,
		&channel.FailureStreak, &channel.CreatedAt, &channel.UpdatedAt)
	if err != nil {
		return Channel{}, err
	}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &channel.Config); err != nil {
			return Channel{}, fmt.Errorf("decode channel config: %w", err)
		}
	}
	if channel.Config == nil {
		channel.Config = map[string]any{}
	}
	if channel.Kinds == nil {
		channel.Kinds = []string{}
	}
	return channel, nil
}

// CreateChannelParams describes a new channel.
type CreateChannelParams struct {
	ServerID    string
	Name        string
	Kind        string
	Config      map[string]any
	Credentials string
	MinSeverity string
	Kinds       []string
	Enabled     bool
}

// CreateChannel stores a channel.
func (r *Repository) CreateChannel(ctx context.Context, params CreateChannelParams) (
	Channel, error,
) {
	config, err := json.Marshal(params.Config)
	if err != nil {
		return Channel{}, fmt.Errorf("encode channel config: %w", err)
	}
	kinds := params.Kinds
	if kinds == nil {
		kinds = []string{}
	}

	row := r.pool.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO notification_channels (
				server_id, name, kind, config, credentials_encrypted,
				min_severity, kinds, enabled
			) VALUES ($1::uuid, $2, $3, $4::jsonb, $5, $6, $7, $8)
			RETURNING *
		)
		SELECT `+channelColumns+` FROM inserted c`,
		params.ServerID, params.Name, params.Kind, config, params.Credentials,
		params.MinSeverity, kinds, params.Enabled)

	channel, err := scanChannel(row)
	if err != nil {
		if isUniqueViolation(err) {
			return Channel{}, ErrNameTaken
		}
		return Channel{}, fmt.Errorf("create channel: %w", err)
	}
	return channel, nil
}

// ListChannels returns a server's channels, oldest first.
func (r *Repository) ListChannels(ctx context.Context, serverID string) ([]Channel, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+channelColumns+`
		FROM notification_channels c
		WHERE c.server_id = $1::uuid
		ORDER BY c.created_at`, serverID)
	if err != nil {
		return nil, fmt.Errorf("select channels: %w", err)
	}
	defer rows.Close()

	channels := []Channel{}
	for rows.Next() {
		channel, err := scanChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		channels = append(channels, channel)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate channels: %w", err)
	}
	return channels, nil
}

// GetChannel returns one channel.
func (r *Repository) GetChannel(ctx context.Context, id string) (Channel, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+channelColumns+` FROM notification_channels c WHERE c.id = $1::uuid`, id)

	channel, err := scanChannel(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	if err != nil {
		return Channel{}, fmt.Errorf("select channel: %w", err)
	}
	return channel, nil
}

// ChannelSecret returns the encrypted credential for a channel.
//
// A separate query rather than a field on Channel, so that reading a channel for
// display and reading it to use are different acts with different call sites. A
// secret that comes back with every listing is a secret that ends up in a log.
func (r *Repository) ChannelSecret(ctx context.Context, id string) (string, error) {
	var secret *string
	err := r.pool.QueryRow(ctx,
		`SELECT credentials_encrypted FROM notification_channels WHERE id = $1::uuid`, id).
		Scan(&secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("select channel credentials: %w", err)
	}
	if secret == nil {
		return "", nil
	}
	return *secret, nil
}

// UpdateChannelParams describes a change to a channel.
//
// The kind cannot change. A channel that became a different kind would keep the
// delivery history of the old one, and "this channel has worked for months"
// would be a claim about somewhere else entirely.
type UpdateChannelParams struct {
	Name        *string
	Config      map[string]any
	Credentials *string
	MinSeverity *string
	Kinds       []string
	Enabled     *bool
}

// UpdateChannel changes a channel.
func (r *Repository) UpdateChannel(ctx context.Context, id string,
	params UpdateChannelParams,
) (Channel, error) {
	var config []byte
	if params.Config != nil {
		encoded, err := json.Marshal(params.Config)
		if err != nil {
			return Channel{}, fmt.Errorf("encode channel config: %w", err)
		}
		config = encoded
	}

	row := r.pool.QueryRow(ctx, `
		WITH updated AS (
			UPDATE notification_channels SET
				name = COALESCE($2, name),
				config = COALESCE($3::jsonb, config),
				credentials_encrypted = CASE
					WHEN $4::text IS NULL THEN credentials_encrypted
					ELSE $4::text
				END,
				min_severity = COALESCE($5, min_severity),
				kinds = COALESCE($6::text[], kinds),
				enabled = COALESCE($7, enabled),
				updated_at = now()
			WHERE id = $1::uuid
			RETURNING *
		)
		SELECT `+channelColumns+` FROM updated c`,
		id, params.Name, config, params.Credentials, params.MinSeverity,
		params.Kinds, params.Enabled)

	channel, err := scanChannel(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	if err != nil {
		if isUniqueViolation(err) {
			return Channel{}, ErrNameTaken
		}
		return Channel{}, fmt.Errorf("update channel: %w", err)
	}
	return channel, nil
}

// DeleteChannel removes a channel and its delivery history.
func (r *Repository) DeleteChannel(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM notification_channels WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete channel: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordChannelResult stores what happened the last time a channel was used.
//
// The failure streak is what the panel has instead of a way to tell somebody
// their notifications are broken: it cannot send a message about that through
// the thing that is broken, so it counts, and the page shows the count.
func (r *Repository) RecordChannelResult(ctx context.Context, id string,
	ok bool, detail string, now time.Time,
) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE notification_channels SET
			last_success_at = CASE WHEN $2 THEN $4 ELSE last_success_at END,
			last_failure_at = CASE WHEN $2 THEN last_failure_at ELSE $4 END,
			last_error = CASE WHEN $2 THEN NULL ELSE nullif($3, '') END,
			failure_streak = CASE WHEN $2 THEN 0 ELSE failure_streak + 1 END,
			updated_at = now()
		WHERE id = $1::uuid`, id, ok, detail, now)
	if err != nil {
		return fmt.Errorf("record channel result: %w", err)
	}
	return nil
}

// ------------------------------------------------------------------ events

const eventColumns = `
	e.id::text, e.server_id::text, e.source, e.kind, e.severity,
	e.title, e.body, COALESCE(e.link, ''), e.dedupe_key, e.metadata, e.created_at`

func scanEvent(row pgx.Row) (Event, error) {
	var (
		event    Event
		metadata []byte
	)
	err := row.Scan(&event.ID, &event.ServerID, &event.Source, &event.Kind,
		&event.Severity, &event.Title, &event.Body, &event.Link,
		&event.DedupeKey, &metadata, &event.CreatedAt)
	if err != nil {
		return Event{}, err
	}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &event.Metadata); err != nil {
			return Event{}, fmt.Errorf("decode event metadata: %w", err)
		}
	}
	return event, nil
}

// RecordEvent stores an event, refusing one that has already been raised.
//
// The duplicate is reported as ErrDuplicateEvent rather than swallowed, because
// the caller is usually a loop that runs every minute and wants to know whether
// this was the first time — not to treat a repeat as a failure.
func (r *Repository) RecordEvent(ctx context.Context, event Event) (Event, error) {
	var metadata []byte
	if len(event.Metadata) > 0 {
		encoded, err := json.Marshal(event.Metadata)
		if err != nil {
			return Event{}, fmt.Errorf("encode event metadata: %w", err)
		}
		metadata = encoded
	}

	row := r.pool.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO notification_events (
				server_id, source, kind, severity, title, body, link,
				dedupe_key, metadata
			) VALUES ($1::uuid, $2, $3, $4, $5, $6, nullif($7, ''), $8, $9::jsonb)
			ON CONFLICT (server_id, dedupe_key) DO NOTHING
			RETURNING *
		)
		SELECT `+eventColumns+` FROM inserted e`,
		event.ServerID, event.Source, event.Kind, event.Severity,
		event.Title, event.Body, event.Link, event.DedupeKey, metadata)

	stored, err := scanEvent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Event{}, ErrDuplicateEvent
	}
	if err != nil {
		return Event{}, fmt.Errorf("record event: %w", err)
	}
	return stored, nil
}

// GetEvent returns one event.
func (r *Repository) GetEvent(ctx context.Context, id string) (Event, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+eventColumns+` FROM notification_events e WHERE e.id = $1::uuid`, id)

	event, err := scanEvent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Event{}, ErrNotFound
	}
	if err != nil {
		return Event{}, fmt.Errorf("select event: %w", err)
	}
	return event, nil
}

// ListEvents returns recent events, newest first.
func (r *Repository) ListEvents(ctx context.Context, serverID string, limit int) (
	[]Event, error,
) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	rows, err := r.pool.Query(ctx, `
		SELECT `+eventColumns+`
		FROM notification_events e
		WHERE e.server_id = $1::uuid
		ORDER BY e.created_at DESC
		LIMIT $2`, serverID, limit)
	if err != nil {
		return nil, fmt.Errorf("select events: %w", err)
	}
	defer rows.Close()

	events := []Event{}
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return events, nil
}

// -------------------------------------------------------------- deliveries

// QueueDelivery creates the delivery for one event to one channel.
//
// A repeat is a no-op rather than an error: a dispatcher restarted mid-run must
// be able to queue the same event again without producing a second message.
func (r *Repository) QueueDelivery(ctx context.Context, eventID, channelID string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_deliveries (event_id, channel_id)
		VALUES ($1::uuid, $2::uuid)
		ON CONFLICT (event_id, channel_id) DO NOTHING`, eventID, channelID)
	if err != nil {
		return fmt.Errorf("queue delivery: %w", err)
	}
	return nil
}

// Due is one delivery ready to be attempted, with everything needed to send it.
type Due struct {
	Delivery Delivery
	Event    Event
	Channel  Channel
}

// ClaimDue takes up to limit deliveries that are ready to attempt.
//
// FOR UPDATE SKIP LOCKED, the same arrangement as the job queue: a row another
// process has taken is passed over rather than waited on, so two API processes
// never send the same message twice and neither blocks.
//
// The attempt counter is incremented as part of claiming. A dispatcher that
// crashed after sending but before recording would otherwise send again on
// every restart, forever — and duplicating an alert is a much better failure
// than an infinite loop of them.
func (r *Repository) ClaimDue(ctx context.Context, now time.Time, limit int) ([]Due, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := r.pool.Query(ctx, `
		WITH claimed AS (
			UPDATE notification_deliveries SET
				attempts = attempts + 1,
				updated_at = now()
			WHERE id IN (
				SELECT d.id FROM notification_deliveries d
				JOIN notification_channels ch ON ch.id = d.channel_id
				WHERE d.status = 'pending'
				  AND d.next_attempt_at <= $1
				  AND ch.enabled
				ORDER BY d.next_attempt_at
				FOR UPDATE SKIP LOCKED
				LIMIT $2
			)
			RETURNING *
		)
		SELECT
			d.id::text, d.event_id::text, d.channel_id::text, d.status,
			d.attempts, d.next_attempt_at, d.last_error, d.sent_at,
			d.created_at, d.updated_at,
			`+eventColumns+`,
			`+channelColumns+`
		FROM claimed d
		JOIN notification_events e ON e.id = d.event_id
		JOIN notification_channels c ON c.id = d.channel_id`,
		now, limit)
	if err != nil {
		return nil, fmt.Errorf("claim deliveries: %w", err)
	}
	defer rows.Close()

	due := []Due{}
	for rows.Next() {
		var (
			item     Due
			metadata []byte
			config   []byte
		)
		err := rows.Scan(
			&item.Delivery.ID, &item.Delivery.EventID, &item.Delivery.ChannelID,
			&item.Delivery.Status, &item.Delivery.Attempts,
			&item.Delivery.NextAttemptAt, &item.Delivery.LastError,
			&item.Delivery.SentAt, &item.Delivery.CreatedAt, &item.Delivery.UpdatedAt,

			&item.Event.ID, &item.Event.ServerID, &item.Event.Source, &item.Event.Kind,
			&item.Event.Severity, &item.Event.Title, &item.Event.Body,
			&item.Event.Link, &item.Event.DedupeKey, &metadata, &item.Event.CreatedAt,

			&item.Channel.ID, &item.Channel.ServerID, &item.Channel.Name,
			&item.Channel.Kind, &config, &item.Channel.Enabled,
			&item.Channel.MinSeverity, &item.Channel.Kinds,
			&item.Channel.LastSuccessAt, &item.Channel.LastFailureAt,
			&item.Channel.LastError, &item.Channel.FailureStreak,
			&item.Channel.CreatedAt, &item.Channel.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("scan due delivery: %w", err)
		}

		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &item.Event.Metadata); err != nil {
				return nil, fmt.Errorf("decode event metadata: %w", err)
			}
		}
		if len(config) > 0 {
			if err := json.Unmarshal(config, &item.Channel.Config); err != nil {
				return nil, fmt.Errorf("decode channel config: %w", err)
			}
		}
		if item.Channel.Config == nil {
			item.Channel.Config = map[string]any{}
		}
		due = append(due, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due deliveries: %w", err)
	}
	return due, nil
}

// MarkSent records a successful delivery.
func (r *Repository) MarkSent(ctx context.Context, id string, now time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE notification_deliveries
		SET status = 'sent', sent_at = $2, updated_at = now()
		WHERE id = $1::uuid`, id, now)
	if err != nil {
		return fmt.Errorf("mark delivery sent: %w", err)
	}
	return nil
}

// Reschedule puts a failed delivery back for another attempt.
func (r *Repository) Reschedule(ctx context.Context, id, reason string,
	nextAttempt time.Time,
) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE notification_deliveries
		SET last_error = $2, next_attempt_at = $3, updated_at = now()
		WHERE id = $1::uuid`, id, reason, nextAttempt)
	if err != nil {
		return fmt.Errorf("reschedule delivery: %w", err)
	}
	return nil
}

// MarkFailed gives up on a delivery.
//
// The row stays, with the reason. A notification that was never delivered is
// the single most important thing this table records: it is the only way an
// operator learns that the silence they have been enjoying was not good news.
func (r *Repository) MarkFailed(ctx context.Context, id, reason string) error {
	if reason == "" {
		reason = "the notification could not be delivered"
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE notification_deliveries
		SET status = 'failed', last_error = $2, updated_at = now()
		WHERE id = $1::uuid`, id, reason)
	if err != nil {
		return fmt.Errorf("mark delivery failed: %w", err)
	}
	return nil
}

// ListDeliveries returns recent deliveries, newest first.
func (r *Repository) ListDeliveries(ctx context.Context, serverID, status string,
	limit int,
) ([]Delivery, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	rows, err := r.pool.Query(ctx, `
		SELECT
			d.id::text, d.event_id::text, d.channel_id::text, d.status,
			d.attempts, d.next_attempt_at, d.last_error, d.sent_at,
			d.created_at, d.updated_at,
			c.name, c.kind, e.title, e.kind, e.severity
		FROM notification_deliveries d
		JOIN notification_channels c ON c.id = d.channel_id
		JOIN notification_events e ON e.id = d.event_id
		WHERE e.server_id = $1::uuid
		  AND ($2 = '' OR d.status = $2)
		ORDER BY d.created_at DESC
		LIMIT $3`, serverID, status, limit)
	if err != nil {
		return nil, fmt.Errorf("select deliveries: %w", err)
	}
	defer rows.Close()

	deliveries := []Delivery{}
	for rows.Next() {
		var delivery Delivery
		if err := rows.Scan(&delivery.ID, &delivery.EventID, &delivery.ChannelID,
			&delivery.Status, &delivery.Attempts, &delivery.NextAttemptAt,
			&delivery.LastError, &delivery.SentAt, &delivery.CreatedAt,
			&delivery.UpdatedAt, &delivery.ChannelName, &delivery.ChannelKind,
			&delivery.EventTitle, &delivery.EventKind, &delivery.Severity); err != nil {
			return nil, fmt.Errorf("scan delivery: %w", err)
		}
		deliveries = append(deliveries, delivery)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate deliveries: %w", err)
	}
	return deliveries, nil
}

// DeliveryStats summarises what has been getting through.
type DeliveryStats struct {
	Sent    int `json:"sent"`
	Failed  int `json:"failed"`
	Pending int `json:"pending"`
	// BrokenChannels is how many channels are currently failing. It is the
	// number the page leads with, because it is the only way the panel can tell
	// somebody their notifications are not arriving.
	BrokenChannels int `json:"broken_channels"`
	// UntestedChannels have never delivered anything successfully. A channel
	// nobody has ever got a message through looks like protection and is not.
	UntestedChannels int `json:"untested_channels"`
}

// Stats counts recent deliveries and unhealthy channels.
func (r *Repository) Stats(ctx context.Context, serverID string, since time.Time) (
	DeliveryStats, error,
) {
	var stats DeliveryStats
	err := r.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM notification_deliveries d
			 JOIN notification_events e ON e.id = d.event_id
			 WHERE e.server_id = $1::uuid AND d.status = 'sent' AND d.created_at >= $2),
			(SELECT count(*) FROM notification_deliveries d
			 JOIN notification_events e ON e.id = d.event_id
			 WHERE e.server_id = $1::uuid AND d.status = 'failed' AND d.created_at >= $2),
			(SELECT count(*) FROM notification_deliveries d
			 JOIN notification_events e ON e.id = d.event_id
			 WHERE e.server_id = $1::uuid AND d.status = 'pending'),
			(SELECT count(*) FROM notification_channels
			 WHERE server_id = $1::uuid AND enabled AND failure_streak > 0),
			(SELECT count(*) FROM notification_channels
			 WHERE server_id = $1::uuid AND enabled AND last_success_at IS NULL)`,
		serverID, since).
		Scan(&stats.Sent, &stats.Failed, &stats.Pending,
			&stats.BrokenChannels, &stats.UntestedChannels)
	if err != nil {
		return DeliveryStats{}, fmt.Errorf("count deliveries: %w", err)
	}
	return stats, nil
}

// PruneEvents removes events older than the retention window.
//
// Deliveries cascade with them, which is right: a delivery is only meaningful
// beside the event it delivered.
func (r *Repository) PruneEvents(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM notification_events WHERE created_at < $1`, olderThan)
	if err != nil {
		return 0, fmt.Errorf("prune events: %w", err)
	}
	return tag.RowsAffected(), nil
}

// isUniqueViolation reports whether an error is a duplicate-key failure.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}

// ResetStreak clears a channel's consecutive failure count.
//
// Used when a channel is reconfigured: the streak counts failures, and a change
// is not one. Without this a channel that was failing, then fixed, would keep
// showing as broken until it next delivered something — which for a panel with
// nothing wrong could be weeks.
func (r *Repository) ResetStreak(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE notification_channels SET failure_streak = 0 WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("reset failure streak: %w", err)
	}
	return nil
}
