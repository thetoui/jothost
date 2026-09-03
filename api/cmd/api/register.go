package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/config"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/servers"
	"github.com/jothost/panel/shared/logger"
)

// registerTimeout bounds startup registration so a slow Agent cannot delay
// the API from serving.
const registerTimeout = 10 * time.Second

// registerLocalServer creates or refreshes the record for the host this API
// manages, and returns its id.
//
// The Agent is asked who the host is rather than the API guessing, because the
// Agent runs where the websites and services actually live — in production on
// the host itself, where the API may be containerised.
//
// Registration failing is not fatal. The panel still serves: authentication,
// health, and the rest work without it, and the dashboard reports that no
// server is registered rather than the whole API refusing to start.
func registerLocalServer(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) string {
	ctx, cancel := context.WithTimeout(ctx, registerTimeout)
	defer cancel()

	repo := servers.NewRepository(pool)
	agent := agentclient.New(agentclient.Options{
		SocketPath: cfg.AgentSocket,
		Timeout:    cfg.AgentTimeout,
		Token:      cfg.AgentToken,
	})

	requestID := httpx.NewRequestID()

	params := servers.RegisterParams{Status: servers.StatusUnknown}

	info, err := agent.System(ctx, requestID)
	if err != nil {
		log.Warn("could not read host details from the agent",
			logger.KeyError, err.Error())
	} else {
		params.Hostname = info.Hostname
		params.OSName = info.OSName
		params.OSVersion = info.OSVersion
		params.Kernel = info.KernelVersion
		params.Architecture = info.Architecture
		params.IPv4 = info.IPv4
		params.IPv6 = info.IPv6
		params.Status = servers.StatusOnline
	}

	if agentInfo, err := agent.Info(ctx, requestID); err == nil {
		params.AgentVersion = agentInfo.Version
	}

	// Without the Agent there is still a host to record; the API's own view of
	// its hostname is a worse answer but better than no server at all, and the
	// record is refreshed with the real details once the Agent returns.
	if params.Hostname == "" {
		hostname, err := os.Hostname()
		if err != nil || hostname == "" {
			log.Error("cannot determine a hostname; the dashboard will report no server",
				logger.KeyError, errText(err))
			return ""
		}
		params.Hostname = hostname
		params.Status = servers.StatusOffline
	}

	server, err := repo.Register(ctx, params)
	if err != nil {
		log.Error("failed to register the local server",
			logger.KeyError, err.Error())
		return ""
	}

	log.Info("registered local server",
		"server_id", server.ID,
		"hostname", server.Hostname,
		"status", server.Status,
	)
	return server.ID
}

func errText(err error) string {
	if err == nil {
		return "no hostname available"
	}
	return err.Error()
}
