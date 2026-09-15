package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"

	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/config"
	"github.com/jothost/panel/api/internal/secrets"
	"github.com/jothost/panel/shared/logger"
)

// rotateEncryptionKey re-encrypts every stored secret from the old key to the
// new one.
//
// Both keys come from the environment, not from api.env, so the installer can
// run it in either direction: forward to rotate, and — if bringing the API up
// on the new key fails — backward to put every secret back under the old one.
// The command changes only the database; swapping the key in api.env is the
// installer's, done after this succeeds.
//
//	JOTHOST_OLD_ENCRYPTION_KEY   the key the secrets are sealed with now
//	JOTHOST_NEW_ENCRYPTION_KEY   the key to re-seal them under
//
// The API must be stopped while this runs: a request that writes a new secret
// against the old key midway through would be re-encrypted or missed depending
// on timing. The installer stops it; run by hand, stop it first.
func rotateEncryptionKey(cfg config.Config, log *slog.Logger) error {
	oldKey := os.Getenv("JOTHOST_OLD_ENCRYPTION_KEY")
	newKey := os.Getenv("JOTHOST_NEW_ENCRYPTION_KEY")
	if oldKey == "" || newKey == "" {
		return errors.New("JOTHOST_OLD_ENCRYPTION_KEY and JOTHOST_NEW_ENCRYPTION_KEY must both be set")
	}
	if oldKey == newKey {
		return errors.New("the old and new keys are the same; nothing to rotate")
	}

	ctx := context.Background()
	pool, _, cleanup, err := connect(ctx, cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	result, err := secrets.RotateEncryptionKey(ctx, pool, oldKey, newKey)
	if err != nil {
		// Nothing was written: RotateEncryptionKey does all its work in one
		// transaction, so a failure leaves every secret under the old key.
		return fmt.Errorf("nothing was changed: %w", err)
	}

	total := 0
	tables := make([]string, 0, len(result.Reencrypted))
	for table := range result.Reencrypted {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		n := result.Reencrypted[table]
		total += n
		if n > 0 {
			fmt.Printf("  %-40s %d\n", table, n)
		}
	}
	for _, empty := range result.SkippedEmpty {
		fmt.Printf("  %-40s empty, skipped\n", empty)
	}
	fmt.Printf("Re-encrypted %d secret(s) under the new key.\n", total)

	// The key itself is never audited; that it was rotated, and how much moved,
	// is what an administrator reading the trail needs.
	if err := audit.NewRecorder(pool, log).Record(ctx, audit.Event{
		Action:   audit.ActionEncryptionKeyRotated,
		Status:   audit.StatusSuccess,
		Metadata: map[string]any{"secrets_reencrypted": total},
	}); err != nil {
		log.Error("failed to audit the key rotation", logger.KeyError, err.Error())
	}
	return nil
}
