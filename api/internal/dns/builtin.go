package dns

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// builtinTemplateDescription is the built-in template's description. It is
// the text migration 0028 wrote, so a host seeded here cannot be told apart
// from one that was seeded there.
const builtinTemplateDescription = "The records a new domain starts with: the apex and www, both pointing at this host."

// EnsureBuiltinTemplate gives a server the built-in template if it has no
// templates at all, and reports whether it wrote one.
//
// Migration 0028 seeded one built-in template per row in servers. That is
// right for an existing installation and wrong for a new one: on a fresh
// database the migrations run before the API registers the host it runs on,
// the servers table is still empty, and the seed writes nothing. Every
// installation made since then started without the template a new zone is
// seeded from - and nothing said so, because a zone with no template is
// created empty rather than refused.
//
// So the guarantee lives here, where a server comes into existence, rather
// than in a migration that can only see the servers that already exist. It
// runs on every start and is idempotent: a server with any template, built-in
// or its own, is left exactly as it is. An operator who replaced the default
// with one of their own has not lost anything that needs putting back.
func EnsureBuiltinTemplate(ctx context.Context, pool *pgxpool.Pool, serverID string) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin seeding the built-in DNS template: %w", err)
	}
	defer func() {
		// Rollback after Commit is a no-op; on any earlier return it undoes a
		// template written without its records.
		_ = tx.Rollback(ctx)
	}()

	// ON CONFLICT covers two APIs starting against one database at once: both
	// can see no template, and the unique index on the name decides which of
	// them writes it. The loser inserts nothing and returns no row.
	var templateID string
	err = tx.QueryRow(ctx, `
		INSERT INTO dns_templates (server_id, name, description, is_default, builtin)
		SELECT $1::uuid, 'Default', $2, TRUE, TRUE
		WHERE NOT EXISTS (SELECT 1 FROM dns_templates WHERE server_id = $1::uuid)
		ON CONFLICT DO NOTHING
		RETURNING id::text`, serverID, builtinTemplateDescription).Scan(&templateID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("write the built-in DNS template: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO dns_template_records (template_id, name, type, value, position)
		VALUES ($1::uuid, '@', 'A', $2, 0),
		       ($1::uuid, 'www', 'A', $2, 1)`, templateID, PlaceholderIP)
	if err != nil {
		return false, fmt.Errorf("write the built-in DNS template's records: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit the built-in DNS template: %w", err)
	}
	return true, nil
}
