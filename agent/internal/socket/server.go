// Package socket implements the Agent's Unix domain socket transport. The
// Agent never listens on TCP: the socket plus its file mode is the trust
// boundary between the unprivileged API and privileged operations
// (ARCHITECTURE.md section 10).
package socket

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/jothost/panel/agent/internal/config"
	"github.com/jothost/panel/agent/internal/operations"
	"github.com/jothost/panel/shared/protocol"
)

// maxRequestBytes bounds a single request so a client cannot exhaust Agent
// memory by streaming an unbounded line.
const maxRequestBytes = 1 << 20 // 1 MiB

// Server accepts typed operations over a Unix socket.
type Server struct {
	cfg      config.Config
	log      *slog.Logger
	registry *operations.Registry

	listener net.Listener
	sem      chan struct{}
	wg       sync.WaitGroup
}

// New builds a socket server.
func New(cfg config.Config, log *slog.Logger, registry *operations.Registry) *Server {
	return &Server{
		cfg:      cfg,
		log:      log,
		registry: registry,
		sem:      make(chan struct{}, cfg.MaxConcurrent),
	}
}

// Listen creates the socket directory and binds the socket with restrictive
// permissions. A stale socket from an unclean shutdown is removed first.
func (s *Server) Listen() error {
	dir := filepath.Dir(s.cfg.SocketPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create socket directory %s: %w", dir, err)
	}
	// MkdirAll leaves an existing directory's mode alone, and a bind-mounted
	// volume is typically created world-readable. Enforce 0750 explicitly.
	if err := os.Chmod(dir, 0o750); err != nil {
		return fmt.Errorf("chmod socket directory %s: %w", dir, err)
	}

	// Remove a stale socket left by a previous crash; anything that is not a
	// socket is refused rather than deleted, so a misconfigured path cannot
	// destroy a real file.
	if info, err := os.Lstat(s.cfg.SocketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refusing to remove non-socket file at %s", s.cfg.SocketPath)
		}
		if err := os.Remove(s.cfg.SocketPath); err != nil {
			return fmt.Errorf("remove stale socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat socket path: %w", err)
	}

	ln, err := net.Listen("unix", s.cfg.SocketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.SocketPath, err)
	}

	// Chmod after bind: the socket is created with the process umask applied,
	// which may be more permissive than intended.
	if err := os.Chmod(s.cfg.SocketPath, s.cfg.SocketMode); err != nil {
		if closeErr := ln.Close(); closeErr != nil {
			return errors.Join(fmt.Errorf("chmod socket: %w", err), closeErr)
		}
		return fmt.Errorf("chmod socket: %w", err)
	}

	if err := s.applyGroupOwnership(); err != nil {
		if closeErr := ln.Close(); closeErr != nil {
			return errors.Join(err, closeErr)
		}
		return err
	}

	s.listener = ln
	s.log.Info("agent socket listening",
		"socket", s.cfg.SocketPath,
		"mode", fmt.Sprintf("%#o", s.cfg.SocketMode),
		"max_concurrent", s.cfg.MaxConcurrent,
	)
	return nil
}

// applyGroupOwnership hands the socket's group to the unprivileged API user's
// group, which is what lets a 0660 socket be reached by the API without
// granting world access.
//
// A missing group is not fatal: the socket simply stays owned by the Agent's
// own group, which is more restrictive rather than less. That case is logged
// so a misconfigured install is visible.
func (s *Server) applyGroupOwnership() error {
	if s.cfg.SocketGroup == "" {
		return nil
	}

	grp, err := user.LookupGroup(s.cfg.SocketGroup)
	if err != nil {
		s.log.Warn("socket group not found; leaving default ownership",
			"group", s.cfg.SocketGroup,
			"error", err.Error(),
		)
		return nil
	}

	gid, err := strconv.Atoi(grp.Gid)
	if err != nil {
		return fmt.Errorf("parse gid for group %s: %w", s.cfg.SocketGroup, err)
	}
	// The API also needs search permission on the containing directory, which
	// is 0750 and owned by the Agent's user. -1 leaves the owning user
	// unchanged on both.
	dir := filepath.Dir(s.cfg.SocketPath)
	if err := os.Chown(dir, -1, gid); err != nil {
		return fmt.Errorf("chown socket directory to group %s: %w", s.cfg.SocketGroup, err)
	}
	if err := os.Chown(s.cfg.SocketPath, -1, gid); err != nil {
		return fmt.Errorf("chown socket to group %s: %w", s.cfg.SocketGroup, err)
	}
	return nil
}

// Addr returns the bound socket path, or "" before Listen succeeds.
func (s *Server) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Serve accepts connections until ctx is cancelled, then drains in-flight
// operations within the configured shutdown timeout.
func (s *Server) Serve(ctx context.Context) error {
	if s.listener == nil {
		return errors.New("Serve called before Listen")
	}

	acceptErr := make(chan error, 1)
	go func() { acceptErr <- s.acceptLoop(ctx) }()

	select {
	case err := <-acceptErr:
		return err
	case <-ctx.Done():
		s.log.Info("agent shutdown signal received")
	}

	if err := s.listener.Close(); err != nil {
		s.log.Error("failed to close agent listener", "error", err.Error())
	}

	drained := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(drained)
	}()

	select {
	case <-drained:
		s.log.Info("agent shutdown complete")
		return nil
	case <-time.After(s.cfg.ShutdownTimeout):
		return fmt.Errorf("agent shutdown timed out after %s with operations still running", s.cfg.ShutdownTimeout)
	}
}

func (s *Server) acceptLoop(ctx context.Context) error {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			// A closed listener during shutdown is expected, not an error.
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(ctx, conn)
		}()
	}
}

// handleConn serves exactly one request per connection. One-shot connections
// keep the state machine trivial, which matters for a root-privileged process.
func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer func() {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			s.log.Debug("closing agent connection", "error", err.Error())
		}
	}()

	// Bound concurrency so a flood of connections cannot exhaust host
	// resources; excess connections wait for a slot or for shutdown.
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return
	}

	opCtx, cancel := context.WithTimeout(ctx, s.cfg.OperationTimeout)
	defer cancel()

	deadline, ok := opCtx.Deadline()
	if ok {
		if err := conn.SetDeadline(deadline); err != nil {
			s.log.Error("failed to set connection deadline", "error", err.Error())
			return
		}
	}

	reader := bufio.NewReader(&limitedConn{conn: conn, remaining: maxRequestBytes})
	line, err := reader.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		s.log.Warn("failed to read agent request", "error", err.Error())
		s.write(conn, protocol.NewError("", protocol.CodeInvalidRequest, "Malformed request"))
		return
	}

	var req protocol.Request
	if err := json.Unmarshal(line, &req); err != nil {
		// The raw payload is never logged: it is untrusted input.
		s.log.Warn("failed to decode agent request", "error", "invalid json")
		s.write(conn, protocol.NewError("", protocol.CodeInvalidRequest, "Malformed request"))
		return
	}

	start := time.Now()
	resp := s.registry.Dispatch(opCtx, req)

	if errors.Is(opCtx.Err(), context.DeadlineExceeded) {
		resp = protocol.NewError(req.RequestID, protocol.CodeTimeout, "Operation timed out")
	}

	s.log.Info("agent_operation",
		"operation", string(req.Operation),
		"request_id", req.RequestID,
		"status", string(resp.Status),
		"duration_ms", time.Since(start).Milliseconds(),
	)
	s.write(conn, resp)
}

func (s *Server) write(conn net.Conn, resp protocol.Response) {
	payload, err := json.Marshal(resp)
	if err != nil {
		s.log.Error("failed to encode agent response", "error", err.Error())
		return
	}
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		s.log.Error("failed to write agent response", "error", err.Error())
	}
}

// limitedConn caps how many bytes are read from a connection.
type limitedConn struct {
	conn      net.Conn
	remaining int
}

func (l *limitedConn) Read(p []byte) (int, error) {
	if l.remaining <= 0 {
		return 0, errors.New("request exceeds size limit")
	}
	if len(p) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.conn.Read(p)
	l.remaining -= n
	return n, err
}
