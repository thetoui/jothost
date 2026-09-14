package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/config"
	"github.com/jothost/panel/api/internal/twofactor"
	"github.com/jothost/panel/api/internal/users"
	"github.com/jothost/panel/shared/logger"
)

// resetTwoFactor removes two-factor authentication from one account.
//
// It is the last way back in: for somebody who has lost their authenticator
// and their recovery codes, on a panel where no other administrator can help.
// It runs on the host, as the API's account, because being able to do that is
// already proof of more than a second factor would prove. The account signs in
// with its password afterwards and can turn two-factor on again.
//
// The username is an argument, not a secret: it is visible in the process list
// and that is fine.
func resetTwoFactor(cfg config.Config, log *slog.Logger, args []string) error {
	if len(args) != 1 || args[0] == "" {
		return errors.New("usage: jothost-api reset-two-factor USERNAME")
	}

	ctx := context.Background()
	pool, _, cleanup, err := connect(ctx, cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	user, err := users.NewRepository(pool).GetByUsername(ctx, args[0])
	if err != nil {
		if errors.Is(err, users.ErrNotFound) {
			return fmt.Errorf("no account named %q", users.NormalizeUsername(args[0]))
		}
		return err
	}

	// The enrolment is only deleted, so no key is needed to do it.
	repo := twofactor.NewRepository(pool, nil)
	enabled, err := repo.IsEnabled(ctx, user.ID)
	if err != nil {
		return err
	}
	if err := repo.Disable(ctx, user.ID); err != nil {
		return err
	}

	if err := audit.NewRecorder(pool, log).Record(ctx, audit.Event{
		UserID:       user.ID,
		Action:       audit.ActionTwoFactorReset,
		ResourceType: "user",
		ResourceID:   user.ID,
		Status:       audit.StatusSuccess,
		Metadata:     map[string]any{"username": user.Username, "was_enabled": enabled, "via": "cli"},
	}); err != nil {
		log.Error("failed to audit the two-factor reset", logger.KeyError, err.Error())
	}

	if enabled {
		fmt.Printf("Two-factor authentication removed from %q. They sign in with their password alone until they turn it on again.\n", user.Username)
	} else {
		fmt.Printf("%q did not have two-factor authentication enabled; nothing to remove.\n", user.Username)
	}
	return nil
}
