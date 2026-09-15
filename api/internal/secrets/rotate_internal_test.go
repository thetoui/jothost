package secrets

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/jothost/panel/api/internal/testsupport"
)

// notYetWired are encrypted columns whose schema exists ahead of the code that
// will use them, so there is nothing yet to re-encrypt and no context to seal
// with. They are allowed to be absent from encryptedColumns until their feature
// ships — at which point that feature's change adds them here and to the
// rotation list together. checkSchemaCovered enforces the runtime half: an
// unwired column with data still stops a rotation.
var notYetWired = map[string]bool{
	"dns_cloudflare.api_token_encrypted": true,
}

// TestRegistryColumnsExistInTheSchema catches a column that was renamed, moved
// or removed out from under the rotation list — the mistake that first put the
// webhook secret on the wrong table here.
func TestRegistryColumnsExistInTheSchema(t *testing.T) {
	deps := testsupport.Require(t)
	for _, c := range encryptedColumns {
		var exists bool
		err := deps.Pool.QueryRow(context.Background(), `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2)`,
			c.table, c.column).Scan(&exists)
		if err != nil {
			t.Fatalf("check %s.%s: %v", c.table, c.column, err)
		}
		if !exists {
			t.Errorf("encryptedColumns lists %s.%s, which is not in the schema", c.table, c.column)
		}
	}
}

// TestSchemaEncryptedColumnsAreAllCovered is the catastrophe guard as a unit
// test: every column the schema names with the "_encrypted" convention must be
// in the rotation list, or explicitly marked not-yet-wired. A new encrypted
// store added without updating encryptedColumns fails here rather than silently
// surviving a rotation with its secrets stranded under the old key.
func TestSchemaEncryptedColumnsAreAllCovered(t *testing.T) {
	deps := testsupport.Require(t)
	known := map[string]bool{}
	for _, c := range encryptedColumns {
		known[c.table+"."+c.column] = true
	}

	rows, err := deps.Pool.Query(context.Background(), `
		SELECT table_name, column_name
		FROM information_schema.columns
		WHERE table_schema = 'public' AND column_name LIKE '%\_encrypted'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatal(err)
		}
		name := table + "." + column
		if !known[name] && !notYetWired[name] {
			t.Errorf("schema has encrypted column %s that key rotation does not cover; "+
				"add it to encryptedColumns (or to notYetWired if its feature is not built yet)", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestReencryptColumnRoundTrip exercises the re-encryption engine directly,
// against a temporary table so no real table's constraints get in the way. It
// covers both context shapes: a prefixed context and the bare-key context the
// webhook secret uses.
func TestReencryptColumnRoundTrip(t *testing.T) {
	deps := testsupport.Require(t)
	ctx := context.Background()

	oldEnc, err := NewEncrypter("0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0")
	if err != nil {
		t.Fatal(err)
	}
	newEnc, err := NewEncrypter("a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90")
	if err != nil {
		t.Fatal(err)
	}

	for _, prefix := range []string{"demo", ""} {
		t.Run("prefix="+prefix, func(t *testing.T) {
			err := pgx.BeginFunc(ctx, deps.Pool, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `CREATE TEMP TABLE rot_roundtrip (
					id text PRIMARY KEY, secret_encrypted text) ON COMMIT DROP`); err != nil {
					return err
				}
				col := encryptedColumn{
					table: "rot_roundtrip", keyColumn: "id",
					column: "secret_encrypted", contextPrefix: prefix,
				}
				want := map[string]string{"row-a": "secret-A", "row-b": "secret-B"}
				for id, secret := range want {
					cipher, err := oldEnc.Encrypt([]byte(secret), col.context(id))
					if err != nil {
						return err
					}
					if _, err := tx.Exec(ctx,
						`INSERT INTO rot_roundtrip (id, secret_encrypted) VALUES ($1, $2)`, id, cipher); err != nil {
						return err
					}
				}
				// A null ciphertext must be left alone, not re-encrypted.
				if _, err := tx.Exec(ctx,
					`INSERT INTO rot_roundtrip (id, secret_encrypted) VALUES ('row-null', NULL)`); err != nil {
					return err
				}

				n, err := reencryptColumn(ctx, tx, col, oldEnc, newEnc)
				if err != nil {
					return err
				}
				if n != len(want) {
					t.Fatalf("re-encrypted %d rows, want %d (the null row must be skipped)", n, len(want))
				}

				for id, secret := range want {
					var cipher string
					if err := tx.QueryRow(ctx,
						`SELECT secret_encrypted FROM rot_roundtrip WHERE id = $1`, id).Scan(&cipher); err != nil {
						return err
					}
					plain, err := newEnc.Decrypt(cipher, col.context(id))
					if err != nil {
						t.Fatalf("%s did not open with the new key: %v", id, err)
					}
					if string(plain) != secret {
						t.Fatalf("%s changed value: %q", id, plain)
					}
					if _, err := oldEnc.Decrypt(cipher, col.context(id)); err == nil {
						t.Fatalf("%s still opens with the old key", id)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatalf("round trip: %v", err)
			}
		})
	}
}

func TestEncryptedColumnContext(t *testing.T) {
	prefixed := encryptedColumn{contextPrefix: "two_factor_auth"}
	if got := prefixed.context("abc"); got != "two_factor_auth:abc" {
		t.Fatalf("prefixed context = %q", got)
	}
	bare := encryptedColumn{contextPrefix: ""}
	if got := bare.context("abc"); got != "abc" {
		t.Fatalf("bare context = %q", got)
	}
}
