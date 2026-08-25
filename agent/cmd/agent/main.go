// Command agent is the JotHost Host Agent: the only component permitted to
// perform privileged server operations (PRD.md section 10). It listens on a
// Unix domain socket and never exposes a network port.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jothost/panel/agent/internal/config"
	"github.com/jothost/panel/agent/internal/operations"
	"github.com/jothost/panel/agent/internal/socket"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/version"
)

func main() {
	// -ping runs the binary as a client instead of a daemon. Container and
	// systemd health checks use it so no extra tooling is required in the
	// runtime image.
	pingMode := flag.Bool("ping", false, "probe a running agent over its socket and exit")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: fatal: %v\n", err)
		os.Exit(1)
	}

	if *pingMode {
		if err := ping(cfg.SocketPath); err != nil {
			fmt.Fprintf(os.Stderr, "agent: ping failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("ok")
		return
	}

	if err := run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "agent: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(cfg config.Config) error {
	log := logger.New(logger.Options{
		Service: "agent",
		Level:   cfg.LogLevel,
		Output:  os.Stdout,
	})
	log.Info("starting jothost agent",
		"version", version.Current().Version,
		"socket", cfg.SocketPath,
	)

	srv := socket.New(cfg, log, operations.NewRegistry(log))
	if err := srv.Listen(); err != nil {
		log.Error("failed to bind agent socket", logger.KeyError, err.Error())
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Serve(ctx); err != nil {
		log.Error("agent terminated with error", logger.KeyError, err.Error())
		return err
	}
	return nil
}

// ping issues a single agent.ping over the socket and reports the outcome.
func ping(socketPath string) error {
	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}

	req, err := json.Marshal(protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: "req_healthcheck",
	})
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return err
	}

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil && n == 0 {
		return err
	}

	var resp protocol.Response
	if err := json.Unmarshal(trimNewline(buf[:n]), &resp); err != nil {
		return fmt.Errorf("invalid agent response: %w", err)
	}
	if resp.Status != protocol.StatusSuccess {
		return fmt.Errorf("agent reported status %s", resp.Status)
	}
	return nil
}

func trimNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
