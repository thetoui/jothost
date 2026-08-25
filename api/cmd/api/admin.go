package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/config"
	"github.com/jothost/panel/api/internal/db/migrate"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/api/internal/secrets"
	"github.com/jothost/panel/api/internal/users"
	"github.com/jothost/panel/shared/logger"
)

// createAdmin provisions the first administrator account.
//
// Credentials come from the environment rather than command-line flags: flags
// are visible in the process list to every local user and land in shell
// history. The installer (Phase 23) sets these variables for a single run.
func createAdmin(cfg config.Config, log *slog.Logger) error {
	username := os.Getenv("JOTHOST_ADMIN_USERNAME")
	password := os.Getenv("JOTHOST_ADMIN_PASSWORD")
	email := os.Getenv("JOTHOST_ADMIN_EMAIL")

	if username == "" || password == "" {
		return errors.New("JOTHOST_ADMIN_USERNAME and JOTHOST_ADMIN_PASSWORD must be set")
	}
	if err := secrets.ValidatePassword(password); err != nil {
		return err
	}

	ctx := context.Background()

	pool, redisClient, cleanup, err := connect(ctx, cfg)
	if err != nil {
		return err
	}
	defer cleanup()
	_ = redisClient // create-admin touches only Postgres; the connection proves Redis is configured.

	// The schema must exist before a user can be inserted, so migrations run
	// first. This makes create-admin safe as the very first command against a
	// brand-new database.
	if _, err := migrate.NewRunner(pool, cfg.MigrationsDir).Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	userRepo := users.NewRepository(pool)
	rbacRepo := rbac.NewRepository(pool)

	hash, err := secrets.HashPassword(password)
	if err != nil {
		return err
	}

	user, err := userRepo.Create(ctx, users.CreateParams{
		Username:     username,
		Email:        email,
		PasswordHash: hash,
		Status:       users.StatusActive,
	})
	if err != nil {
		if errors.Is(err, users.ErrUsernameTaken) {
			return fmt.Errorf("a user named %q already exists", users.NormalizeUsername(username))
		}
		return err
	}

	if err := rbacRepo.AssignRole(ctx, user.ID, rbac.RoleAdmin); err != nil {
		return fmt.Errorf("assign admin role: %w", err)
	}

	if err := audit.NewRecorder(pool, log).Record(ctx, audit.Event{
		UserID:       user.ID,
		Action:       audit.ActionUserCreated,
		ResourceType: "user",
		ResourceID:   user.ID,
		Status:       audit.StatusSuccess,
		Metadata:     map[string]any{"username": user.Username, "role": rbac.RoleAdmin, "via": "cli"},
	}); err != nil {
		// The account exists; a missing audit row is worth surfacing but does
		// not undo the creation.
		log.Error("failed to audit admin creation", logger.KeyError, err.Error())
	}

	// The username is safe to print; the password is never echoed.
	log.Info("administrator created", "username", user.Username, "user_id", user.ID)
	fmt.Printf("Administrator %q created.\n", user.Username)
	return nil
}
