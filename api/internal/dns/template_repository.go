package dns

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Reading and writing DNS templates.
//
// A template and its records are written together, in one transaction: a
// template that ended up with half its records because the second insert
// failed would seed every zone made from it with a partial zone, and nothing
// would say so.

// Templates lists a server's templates, each with its records.
func (r *Repository) Templates(ctx context.Context, serverID string) ([]Template, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, server_id::text, name, description, is_default, builtin,
		       created_at, updated_at
		FROM dns_templates
		WHERE server_id = $1::uuid
		ORDER BY is_default DESC, lower(name)`, serverID)
	if err != nil {
		return nil, fmt.Errorf("list DNS templates: %w", err)
	}
	defer rows.Close()

	templates := make([]Template, 0, 4)
	for rows.Next() {
		var template Template
		if err := rows.Scan(&template.ID, &template.ServerID, &template.Name,
			&template.Description, &template.IsDefault, &template.Builtin,
			&template.CreatedAt, &template.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan DNS template: %w", err)
		}
		templates = append(templates, template)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list DNS templates: %w", err)
	}

	for i := range templates {
		records, err := r.templateRecords(ctx, templates[i].ID)
		if err != nil {
			return nil, err
		}
		templates[i].Records = records
	}
	return templates, nil
}

// templateRecords reads one template's records, in the order they were given.
func (r *Repository) templateRecords(ctx context.Context, templateID string) ([]TemplateRecord, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, name, type, ttl, value, priority, weight, port, position
		FROM dns_template_records
		WHERE template_id = $1::uuid
		ORDER BY position, created_at`, templateID)
	if err != nil {
		return nil, fmt.Errorf("list template records: %w", err)
	}
	defer rows.Close()

	records := make([]TemplateRecord, 0, 4)
	for rows.Next() {
		var record TemplateRecord
		if err := rows.Scan(&record.ID, &record.Name, &record.Type, &record.TTL,
			&record.Value, &record.Priority, &record.Weight, &record.Port,
			&record.Position); err != nil {
			return nil, fmt.Errorf("scan template record: %w", err)
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// DefaultTemplate returns the template new zones are seeded from.
//
// ErrTemplateNotFound where a server has none: seeding then writes nothing,
// which is a zone somebody adds a line to rather than a zone they cannot
// create.
func (r *Repository) DefaultTemplate(ctx context.Context, serverID string) (Template, error) {
	var template Template
	err := r.pool.QueryRow(ctx, `
		SELECT id::text, server_id::text, name, description, is_default, builtin,
		       created_at, updated_at
		FROM dns_templates
		WHERE server_id = $1::uuid AND is_default
		LIMIT 1`, serverID).Scan(&template.ID, &template.ServerID, &template.Name,
		&template.Description, &template.IsDefault, &template.Builtin,
		&template.CreatedAt, &template.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Template{}, ErrTemplateNotFound
	}
	if err != nil {
		return Template{}, fmt.Errorf("read the default DNS template: %w", err)
	}

	records, err := r.templateRecords(ctx, template.ID)
	if err != nil {
		return Template{}, err
	}
	template.Records = records
	return template, nil
}

// SaveTemplate creates a template, or replaces one whole.
//
// Replacing rather than patching: a template is a list, and an edit that
// reordered or removed lines would otherwise need its own vocabulary of
// operations for no benefit. The whole list arrives and the whole list is
// written, inside one transaction.
func (r *Repository) SaveTemplate(ctx context.Context, serverID, id string,
	in TemplateInput,
) (Template, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Template{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	name := strings.TrimSpace(in.Name)

	// Only one default per server, and the index enforces it — so the other
	// one is cleared first rather than relying on the write to fail.
	if in.IsDefault {
		if _, err := tx.Exec(ctx, `
			UPDATE dns_templates SET is_default = FALSE, updated_at = now()
			WHERE server_id = $1::uuid AND is_default
			  AND ($2 = '' OR id <> $2::uuid)`, serverID, id); err != nil {
			return Template{}, fmt.Errorf("clear the previous default: %w", err)
		}
	}

	var templateID string
	if id == "" {
		err = tx.QueryRow(ctx, `
			INSERT INTO dns_templates (server_id, name, description, is_default)
			VALUES ($1::uuid, $2, $3, $4)
			RETURNING id::text`,
			serverID, name, in.Description, in.IsDefault).Scan(&templateID)
	} else {
		templateID = id
		var found string
		err = tx.QueryRow(ctx, `
			UPDATE dns_templates
			SET name = $3, description = $4, is_default = $5, updated_at = now()
			WHERE id = $2::uuid AND server_id = $1::uuid
			RETURNING id::text`,
			serverID, id, name, in.Description, in.IsDefault).Scan(&found)
		if errors.Is(err, pgx.ErrNoRows) {
			return Template{}, ErrTemplateNotFound
		}
	}
	if err != nil {
		if isUniqueViolation(err) {
			return Template{}, ErrTemplateNameTaken
		}
		return Template{}, fmt.Errorf("write the DNS template: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM dns_template_records WHERE template_id = $1::uuid`, templateID); err != nil {
		return Template{}, fmt.Errorf("clear the template records: %w", err)
	}

	for position, record := range in.Records {
		if _, err := tx.Exec(ctx, `
			INSERT INTO dns_template_records
				(template_id, name, type, ttl, value, priority, weight, port, position)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9)`,
			templateID, strings.TrimSpace(record.Name), strings.ToUpper(strings.TrimSpace(record.Type)),
			record.TTL, strings.TrimSpace(record.Value),
			record.Priority, record.Weight, record.Port, position); err != nil {
			return Template{}, fmt.Errorf("write a template record: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Template{}, fmt.Errorf("commit: %w", err)
	}
	return r.Template(ctx, serverID, templateID)
}

// Template reads one template.
func (r *Repository) Template(ctx context.Context, serverID, id string) (Template, error) {
	var template Template
	err := r.pool.QueryRow(ctx, `
		SELECT id::text, server_id::text, name, description, is_default, builtin,
		       created_at, updated_at
		FROM dns_templates
		WHERE id = $2::uuid AND server_id = $1::uuid`, serverID, id).
		Scan(&template.ID, &template.ServerID, &template.Name, &template.Description,
			&template.IsDefault, &template.Builtin, &template.CreatedAt, &template.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Template{}, ErrTemplateNotFound
	}
	if err != nil {
		return Template{}, fmt.Errorf("read the DNS template: %w", err)
	}

	records, err := r.templateRecords(ctx, template.ID)
	if err != nil {
		return Template{}, err
	}
	template.Records = records
	return template, nil
}

// DeleteTemplate removes one.
//
// A built-in is refused: it can be edited, because an operator who wants
// different defaults should not have to make a second template, but deleting
// the only one leaves new zones starting with nothing at all.
func (r *Repository) DeleteTemplate(ctx context.Context, serverID, id string) error {
	var builtin bool
	err := r.pool.QueryRow(ctx,
		`SELECT builtin FROM dns_templates WHERE id = $2::uuid AND server_id = $1::uuid`,
		serverID, id).Scan(&builtin)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTemplateNotFound
	}
	if err != nil {
		return fmt.Errorf("read the DNS template: %w", err)
	}
	if builtin {
		return ErrTemplateBuiltin
	}

	if _, err := r.pool.Exec(ctx,
		`DELETE FROM dns_templates WHERE id = $2::uuid AND server_id = $1::uuid`,
		serverID, id); err != nil {
		return fmt.Errorf("delete the DNS template: %w", err)
	}
	return nil
}

// isUniqueViolation reports a duplicate-key failure from Postgres.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}
