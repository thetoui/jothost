// Package agentclient is the API's only channel to the privileged Host Agent.
// It speaks the typed protocol over a Unix domain socket; the API never
// executes privileged commands itself (PRD.md section 10).
package agentclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/jothost/panel/shared/protocol"
)

// maxResponseBytes bounds how much the API will read from the agent so a
// misbehaving agent cannot exhaust API memory.
const maxResponseBytes = 1 << 20 // 1 MiB

// Client sends typed operations to the Host Agent.
type Client struct {
	socketPath string
	timeout    time.Duration
	dialer     net.Dialer
}

// New returns a Client bound to a Unix socket path. timeout bounds the whole
// request/response exchange (CLAUDE.md section 6).
func New(socketPath string, timeout time.Duration) *Client {
	return &Client{socketPath: socketPath, timeout: timeout}
}

// ErrUnavailable indicates the agent socket could not be reached.
var ErrUnavailable = errors.New("agent unavailable")

// Do validates and sends one operation, returning the agent's response.
// Validation happens on the API side as well as the agent side so an
// unknown operation never even reaches the privileged process.
func (c *Client) Do(ctx context.Context, req protocol.Request) (protocol.Response, error) {
	if err := req.Validate(); err != nil {
		return protocol.Response{}, fmt.Errorf("invalid agent request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	conn, err := c.dialer.DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		return protocol.Response{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer func() {
		// Close errors on a already-completed exchange carry no actionable
		// information for the caller, but must not be silently discarded.
		_ = conn.Close()
	}()

	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return protocol.Response{}, fmt.Errorf("set agent deadline: %w", err)
		}
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return protocol.Response{}, fmt.Errorf("encode agent request: %w", err)
	}
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		return protocol.Response{}, fmt.Errorf("write agent request: %w", err)
	}

	reader := bufio.NewReader(&limitedReader{conn: conn, remaining: maxResponseBytes})
	line, err := reader.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return protocol.Response{}, fmt.Errorf("read agent response: %w", err)
	}

	var resp protocol.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return protocol.Response{}, fmt.Errorf("decode agent response: %w", err)
	}
	return resp, nil
}

// Ping performs a liveness check against the agent.
func (c *Client) Ping(ctx context.Context, requestID string) error {
	resp, err := c.Do(ctx, protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: requestID,
	})
	if err != nil {
		return err
	}
	if resp.Status != protocol.StatusSuccess {
		if resp.Error != nil {
			return fmt.Errorf("agent ping failed: %s", resp.Error.Code)
		}
		return errors.New("agent ping failed")
	}
	return nil
}

// limitedReader caps the number of bytes read from a connection.
type limitedReader struct {
	conn      net.Conn
	remaining int
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.remaining <= 0 {
		return 0, errors.New("agent response exceeds size limit")
	}
	if len(p) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.conn.Read(p)
	l.remaining -= n
	return n, err
}
