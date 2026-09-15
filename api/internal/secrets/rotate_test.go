package secrets_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"testing"

	"github.com/jothost/panel/api/internal/secrets"
	"github.com/jothost/panel/api/internal/testsupport"
)

// newUUID formats a random v4 UUID, so the test needs no uuid dependency the
// API itself does without.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

const (
	oldHexKey = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"
	newHexKey = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
)

// The safety net: an encrypted column the rotation list does not cover, holding
// data, stops the rotation before it writes anything — so a store added to the
// schema without being taught to rotation cannot be silently stranded. The
// dns_cloudflare table (schema laid down ahead of its code) is the real case,
// and its columns' own NOT NULL and CHECK constraints are all this needs.
func TestRotationRefusesAnUnknownEncryptedColumnWithData(t *testing.T) {
	deps := testsupport.Require(t)
	ctx := context.Background()

	// Foreign keys off for the insert: the row needs no real parent zone to
	// stand in for "an unwired encrypted column that holds data".
	conn, err := deps.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "SET session_replication_role = replica"); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx,
		`INSERT INTO dns_cloudflare (zone_id, cloudflare_zone_id, api_token_encrypted)
		 VALUES ($1::uuid, 'cf-zone-1', 'not-real-ciphertext')`, newUUID())
	_, _ = conn.Exec(ctx, "SET session_replication_role = origin")
	conn.Release()
	if err != nil {
		t.Fatalf("seed dns_cloudflare: %v", err)
	}

	_, err = secrets.RotateEncryptionKey(ctx, deps.Pool, oldHexKey, newHexKey)
	if !errors.Is(err, secrets.ErrUnknownEncryptedColumn) {
		t.Fatalf("rotation must refuse an unknown encrypted column with data, got %v", err)
	}
}

// An unknown encrypted column that is empty — a feature's schema ahead of its
// code — is reported and does not block a rotation.
func TestRotationSkipsAnEmptyUnknownColumn(t *testing.T) {
	deps := testsupport.Require(t)
	result, err := secrets.RotateEncryptionKey(context.Background(), deps.Pool, oldHexKey, newHexKey)
	if err != nil {
		t.Fatalf("rotate on an empty database: %v", err)
	}
	found := false
	for _, c := range result.SkippedEmpty {
		if c == "dns_cloudflare.api_token_encrypted" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected dns_cloudflare.api_token_encrypted among skipped-empty, got %v", result.SkippedEmpty)
	}
}
