package secrets

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Rotating ENCRYPTION_KEY.
//
// Every secret the panel stores — a 2FA secret, a database password, a
// provider token, a destination's credentials — is encrypted under
// ENCRYPTION_KEY. There is no key id in a ciphertext, so a new key cannot open
// what the old one sealed; rotating means reading every stored ciphertext with
// the old key and writing it back under the new one, in a single transaction
// so the database is never half-rotated.
//
// The danger is not a wrong key — a wrong key makes every decrypt fail and the
// transaction roll back, changing nothing. The danger is *missing a column*: a
// secret this code does not know to re-encrypt is left under the old key, and
// once the old key is gone it is unreadable for good. So the set of encrypted
// columns is checked against the database's own schema, and a column holding
// data that is not in the list below stops the rotation before it writes
// anything.

// encryptedColumn is one place a ciphertext is stored, and how to rebuild the
// associated data it was sealed with.
type encryptedColumn struct {
	table     string
	keyColumn string
	column    string
	// contextPrefix is joined to the row's key with a colon to form the
	// encryption context, matching the code that wrote the value. An empty
	// prefix means the context is the key alone (the git_repositories webhook
	// secret is sealed that way).
	contextPrefix string
}

func (c encryptedColumn) context(id string) string {
	if c.contextPrefix == "" {
		return id
	}
	return c.contextPrefix + ":" + id
}

// encryptedColumns is every column sealed with ENCRYPTION_KEY, with the context
// the writing code uses. Adding an encrypted store to the panel means adding it
// here; the schema check below is what makes forgetting to a build-breaking
// omission rather than a silent one.
var encryptedColumns = []encryptedColumn{
	{"two_factor_auth", "user_id", "secret_encrypted", "two_factor_auth"},
	{"database_users", "id", "password_encrypted", "database_user"},
	{"node_environment", "id", "value_encrypted", "node_environment"},
	{"dns_providers", "id", "api_token_encrypted", "dns_provider"},
	{"backup_destinations", "id", "credentials_encrypted", "backup_destination"},
	{"notification_channels", "id", "credentials_encrypted", "notification_channel"},
	{"git_repositories", "id", "webhook_secret_encrypted", ""},
}

// RotationResult reports what a rotation did, per table, for the operator to
// see that it touched what they expected.
type RotationResult struct {
	Reencrypted map[string]int
	// SkippedEmpty names encrypted columns the schema has that this build does
	// not know how to re-encrypt, but which hold no data — a feature whose
	// schema is laid down ahead of its code, such as dns_cloudflare. Empty is
	// safe to leave; the guard only fails when such a column has rows.
	SkippedEmpty []string
}

// ErrUnknownEncryptedColumn means the schema has an encrypted column holding
// data that this build does not know how to re-encrypt. Rotating anyway would
// strand that data under the old key, so the rotation refuses.
var ErrUnknownEncryptedColumn = errors.New("an encrypted column is not covered by key rotation")

// RotateEncryptionKey re-encrypts every stored secret from oldHexKey to
// newHexKey, in one transaction.
//
// Both keys are given explicitly rather than read from configuration, so the
// caller can run it in either direction — which is what makes a failed
// rotation reversible — without depending on which key api.env currently holds.
func RotateEncryptionKey(ctx context.Context, pool *pgxpool.Pool, oldHexKey, newHexKey string) (RotationResult, error) {
	oldEnc, err := NewEncrypter(oldHexKey)
	if err != nil {
		return RotationResult{}, fmt.Errorf("old key: %w", err)
	}
	newEnc, err := NewEncrypter(newHexKey)
	if err != nil {
		return RotationResult{}, fmt.Errorf("new key: %w", err)
	}

	skipped, err := checkSchemaCovered(ctx, pool)
	if err != nil {
		return RotationResult{}, err
	}

	result := RotationResult{Reencrypted: map[string]int{}, SkippedEmpty: skipped}

	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		for _, col := range encryptedColumns {
			n, err := reencryptColumn(ctx, tx, col, oldEnc, newEnc)
			if err != nil {
				return err
			}
			result.Reencrypted[col.table+"."+col.column] = n
		}
		return nil
	})
	if err != nil {
		return RotationResult{}, err
	}
	return result, nil
}

// reencryptColumn re-seals every non-null ciphertext in one column.
func reencryptColumn(ctx context.Context, tx pgx.Tx, col encryptedColumn, oldEnc, newEnc *Encrypter) (int, error) {
	//nolint:gosec // table, key and column names are compile-time constants from encryptedColumns, never input.
	query := fmt.Sprintf("SELECT %s::text, %s FROM %s WHERE %s IS NOT NULL",
		col.keyColumn, col.column, col.table, col.column)
	rows, err := tx.Query(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("read %s.%s: %w", col.table, col.column, err)
	}

	type item struct{ id, cipher string }
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.cipher); err != nil {
			rows.Close()
			return 0, fmt.Errorf("read %s.%s: %w", col.table, col.column, err)
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read %s.%s: %w", col.table, col.column, err)
	}

	//nolint:gosec // see above: identifiers are constants.
	update := fmt.Sprintf("UPDATE %s SET %s = $2 WHERE %s = $1", col.table, col.column, col.keyColumn)
	for _, it := range items {
		context := col.context(it.id)
		plaintext, err := oldEnc.Decrypt(it.cipher, context)
		if err != nil {
			// The old key does not open this value. Rotating the rest and
			// leaving this one would strand it, so the whole rotation stops
			// here with nothing written.
			return 0, fmt.Errorf("%s.%s for %s could not be read with the old key: %w",
				col.table, col.column, it.id, err)
		}
		resealed, err := newEnc.Encrypt(plaintext, context)
		if err != nil {
			return 0, fmt.Errorf("re-encrypt %s.%s for %s: %w", col.table, col.column, it.id, err)
		}
		if _, err := tx.Exec(ctx, update, it.id, resealed); err != nil {
			return 0, fmt.Errorf("write %s.%s for %s: %w", col.table, col.column, it.id, err)
		}
	}
	return len(items), nil
}

// checkSchemaCovered refuses to rotate if the database has an encrypted column
// holding data that encryptedColumns does not list.
//
// The naming convention is the net: every column that stores a ciphertext is
// named with an "_encrypted" suffix, so any such column the list does not cover
// is a store somebody added without teaching rotation about it. An empty one is
// reported and allowed (schema laid down before its feature); one with rows is
// fatal, because rotating around it would lose that data.
func checkSchemaCovered(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	known := map[string]bool{}
	for _, c := range encryptedColumns {
		known[c.table+"."+c.column] = true
	}

	rows, err := pool.Query(ctx, `
		SELECT table_name, column_name
		FROM information_schema.columns
		WHERE table_schema = 'public' AND column_name LIKE '%\_encrypted'`)
	if err != nil {
		return nil, fmt.Errorf("inspect the schema for encrypted columns: %w", err)
	}
	defer rows.Close()

	type col struct{ table, column string }
	var unknown []col
	for rows.Next() {
		var c col
		if err := rows.Scan(&c.table, &c.column); err != nil {
			return nil, err
		}
		if !known[c.table+"."+c.column] {
			unknown = append(unknown, c)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var skippedEmpty []string
	for _, c := range unknown {
		var count int
		//nolint:gosec // c.table and c.column come from information_schema, not a caller.
		q := fmt.Sprintf("SELECT count(*) FROM %s WHERE %s IS NOT NULL", c.table, c.column)
		if err := pool.QueryRow(ctx, q).Scan(&count); err != nil {
			return nil, fmt.Errorf("count %s.%s: %w", c.table, c.column, err)
		}
		if count > 0 {
			return nil, fmt.Errorf("%w: %s.%s holds %d value(s); add it to encryptedColumns before rotating",
				ErrUnknownEncryptedColumn, c.table, c.column, count)
		}
		skippedEmpty = append(skippedEmpty, c.table+"."+c.column)
	}
	sort.Strings(skippedEmpty)
	return skippedEmpty, nil
}
