// Command api is the JotHost Panel HTTP API. It is unprivileged: every
// privileged operation is delegated to the Host Agent over a Unix socket
// (PRD.md section 10, ARCHITECTURE.md section 10).
//
// Subcommands:
//
//	api                     start the HTTP server (default)
//	api migrate up          apply pending migrations
//	api migrate down        roll back the most recent migration
//	api migrate status      show migration state
//	api create-admin        create the first administrator
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/jothost/panel/api/internal/cache"
	"github.com/jothost/panel/api/internal/config"
	"github.com/jothost/panel/api/internal/db"
	"github.com/jothost/panel/api/internal/db/migrate"
	"github.com/jothost/panel/api/internal/server"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		// Configuration errors happen before the logger exists, so report to
		// stderr and exit non-zero for the supervisor to observe.
		fmt.Fprintf(os.Stderr, "api: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// Before the configuration is loaded, deliberately. An operator asking a
	// binary they have just downloaded what it is has no configuration yet,
	// and refusing to answer until they write one is refusing the one question
	// worth asking before installing anything.
	if len(args) > 0 && (args[0] == "version" || args[0] == "-version" || args[0] == "--version") {
		printVersion("jothost-api")
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logger.New(logger.Options{
		Service: "api",
		Level:   cfg.LogLevel,
		Output:  os.Stdout,
	})
	// Packages that cannot reasonably take a logger — httpx, which is called
	// from every handler — write through the default, so it has to be the
	// configured one rather than Go's unconfigured stderr logger.
	slog.SetDefault(log)

	command := "serve"
	if len(args) > 0 {
		command = args[0]
	}

	switch command {
	case "serve":
		return serve(cfg, log)
	case "migrate":
		return migrateCommand(cfg, log, args[1:])
	case "create-admin":
		return createAdmin(cfg, log)
	case "reset-two-factor":
		return resetTwoFactor(cfg, log, args[1:])
	case "rotate-encryption-key":
		return rotateEncryptionKey(cfg, log)
	case "version":
		printVersion("jothost-api")
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", command)
	}
}

// printVersion writes the build stamp in one line.
//
// The version first, so `jothost-api version | cut -d' ' -f2` is a stable way
// to read it from a script, and the commit and build date after it, because
// "0.1.0" alone does not identify a build when something has been rebuilt.
func printVersion(name string) {
	info := version.Current()
	fmt.Printf("%s %s (commit %s, built %s)\n", name, info.Version, info.Commit, info.BuildDate)
}

func usage() {
	fmt.Fprint(os.Stderr, `jothost-api — JotHost Panel API

Usage:
  jothost-api                    start the HTTP server
  jothost-api migrate up         apply pending migrations
  jothost-api migrate down       roll back the most recent migration
  jothost-api migrate status     show migration state
  jothost-api create-admin       create the first administrator
  jothost-api reset-two-factor USERNAME
                                 remove two-factor authentication from an account
                                 that has lost its authenticator and recovery codes
  jothost-api rotate-encryption-key
                                 re-encrypt every stored secret from
                                 JOTHOST_OLD_ENCRYPTION_KEY to JOTHOST_NEW_ENCRYPTION_KEY

create-admin reads credentials from the environment so they never appear in
the process list or shell history:

  JOTHOST_ADMIN_USERNAME   required
  JOTHOST_ADMIN_PASSWORD   required
  JOTHOST_ADMIN_EMAIL      optional
`)
}

// connect opens Postgres and Redis, returning a cleanup function.
func connect(ctx context.Context, cfg config.Config) (*pgxpool.Pool, *redis.Client, func(), error) {
	pool, err := db.Connect(ctx, db.Options{
		URL:            cfg.DatabaseURL,
		MaxConns:       cfg.DBMaxConns,
		ConnectTimeout: cfg.ConnectTimeout,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	redisClient, err := cache.Connect(ctx, cache.Options{
		URL:            cfg.RedisURL,
		ConnectTimeout: cfg.ConnectTimeout,
	})
	if err != nil {
		pool.Close()
		return nil, nil, nil, err
	}

	cleanup := func() {
		if err := redisClient.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "api: closing redis: %v\n", err)
		}
		pool.Close()
	}
	return pool, redisClient, cleanup, nil
}

func serve(cfg config.Config, log *slog.Logger) error {
	log.Info("starting jothost api",
		"version", version.Current().Version,
		"environment", string(cfg.Environment),
	)

	// SIGINT/SIGTERM trigger graceful shutdown; systemd and Docker both use
	// SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, redisClient, cleanup, err := connect(ctx, cfg)
	if err != nil {
		log.Error("failed to connect to dependencies", logger.KeyError, err.Error())
		return err
	}
	defer cleanup()

	runner := migrate.NewRunner(pool, cfg.MigrationsDir)
	if cfg.AutoMigrate {
		applied, err := runner.Up(ctx)
		if err != nil {
			log.Error("migrations failed", logger.KeyError, err.Error())
			return err
		}
		if len(applied) > 0 {
			log.Info("applied migrations", "migrations", applied)
		}
	} else if err := runner.Verify(ctx); err != nil {
		// With auto-migration off, refuse to serve against a schema that does
		// not match the code rather than failing later at query time.
		log.Error("schema verification failed", logger.KeyError, err.Error())
		return err
	}

	// Registration runs after migrations so the servers table exists, and
	// before the server is built so the dashboard and sampler know which host
	// they are reporting on.
	localServerID := registerLocalServer(ctx, cfg, pool, log)

	srv, err := server.New(server.Options{
		Config:        cfg,
		Log:           log,
		Pool:          pool,
		Redis:         redisClient,
		LocalServerID: localServerID,
	})
	if err != nil {
		log.Error("failed to build server", logger.KeyError, err.Error())
		return err
	}

	if err := srv.Run(ctx); err != nil {
		log.Error("api terminated with error", logger.KeyError, err.Error())
		return err
	}
	return nil
}

func migrateCommand(cfg config.Config, log *slog.Logger, args []string) error {
	action := "up"
	if len(args) > 0 {
		action = args[0]
	}

	ctx := context.Background()

	pool, err := db.Connect(ctx, db.Options{
		URL:            cfg.DatabaseURL,
		MaxConns:       cfg.DBMaxConns,
		ConnectTimeout: cfg.ConnectTimeout,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	runner := migrate.NewRunner(pool, cfg.MigrationsDir)

	switch action {
	case "up":
		applied, err := runner.Up(ctx)
		if err != nil {
			return err
		}
		if len(applied) == 0 {
			log.Info("no pending migrations")
			return nil
		}
		for _, id := range applied {
			log.Info("applied migration", "migration", id)
		}
		return nil

	case "down":
		reverted, err := runner.Down(ctx)
		if err != nil {
			return err
		}
		if reverted == "" {
			log.Info("no migrations to roll back")
			return nil
		}
		log.Info("rolled back migration", "migration", reverted)
		return nil

	case "status":
		statuses, err := runner.Status(ctx)
		if err != nil {
			return err
		}
		for _, s := range statuses {
			state := "pending"
			if s.Applied {
				state = "applied"
			}
			fmt.Printf("%-40s %s\n", s.ID(), state)
		}
		return nil

	default:
		return fmt.Errorf("unknown migrate action %q (expected up, down, or status)", action)
	}
}
