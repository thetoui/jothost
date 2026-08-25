// Command api is the JotHost Panel HTTP API. It is unprivileged: every
// privileged operation is delegated to the Host Agent over a Unix socket
// (PRD.md section 10, ARCHITECTURE.md section 10).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jothost/panel/api/internal/config"
	"github.com/jothost/panel/api/internal/server"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/version"
)

func main() {
	if err := run(); err != nil {
		// Configuration errors happen before the logger exists, so report to
		// stderr and exit non-zero for the supervisor to observe.
		fmt.Fprintf(os.Stderr, "api: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logger.New(logger.Options{
		Service: "api",
		Level:   cfg.LogLevel,
		Output:  os.Stdout,
	})
	log.Info("starting jothost api",
		"version", version.Current().Version,
		"environment", string(cfg.Environment),
	)

	// SIGINT/SIGTERM trigger graceful shutdown; systemd and Docker both use
	// SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := server.New(cfg, log).Run(ctx); err != nil {
		log.Error("api terminated with error", logger.KeyError, err.Error())
		return err
	}
	return nil
}
