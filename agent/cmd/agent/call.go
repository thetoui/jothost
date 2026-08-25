package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"time"

	"github.com/jothost/panel/agent/internal/config"
	"github.com/jothost/panel/shared/protocol"
)

// maxCallResponseBytes bounds what the diagnostic client will read back.
const maxCallResponseBytes = 4 << 20 // 4 MiB

// call issues one operation against a running Agent and prints the response.
//
// This is an operator tool: it lets someone debugging a host ask the Agent
// directly, without the API in the way. It is not a privilege bypass — the
// request goes through the same socket and the same authentication as any
// other caller, so an operator without the token or an allowlisted UID gets
// the same refusal the API would.
func call(cfg config.Config, operation, payloadJSON string, async bool) error {
	if operation == "" {
		return errors.New("-call requires an operation name")
	}

	var payload map[string]any
	if payloadJSON != "" {
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			return fmt.Errorf("-payload is not valid JSON: %w", err)
		}
	}

	req := protocol.Request{
		Operation: protocol.OperationType(operation),
		RequestID: "req_cli_" + fmt.Sprint(time.Now().UnixNano()),
		Token:     cfg.Token,
		Payload:   payload,
	}
	if async {
		req.Mode = protocol.ModeAsync
	}

	// Validating locally gives a clearer message than a generic refusal, and
	// avoids opening a connection for a request that cannot succeed.
	if err := req.Validate(); err != nil {
		return fmt.Errorf("%w\n\nallowlisted operations: %s", err, allowlistSummary())
	}

	conn, err := net.DialTimeout("unix", cfg.SocketPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("connect to agent at %s: %w", cfg.SocketPath, err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(cfg.OperationTimeout + 5*time.Second)); err != nil {
		return err
	}

	encoded, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(encoded, '\n')); err != nil {
		return err
	}

	reader := bufio.NewReader(io.LimitReader(conn, maxCallResponseBytes))
	line, err := reader.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return fmt.Errorf("read agent response: %w", err)
	}

	// Re-encode for readability rather than printing the wire form.
	var resp protocol.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("invalid agent response: %w", err)
	}

	pretty, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(pretty))

	if resp.Status == protocol.StatusFailed {
		// A non-zero exit lets a script branch on the outcome.
		os.Exit(1)
	}
	return nil
}

// allowlistSummary renders the operations a caller may ask for.
func allowlistSummary() string {
	ops := protocol.AllowedOperations()
	names := make([]string, 0, len(ops))
	for _, op := range ops {
		names = append(names, string(op))
	}
	sortStrings(names)

	out := ""
	for _, name := range names {
		out += "\n  " + name
	}
	return out
}

func sortStrings(values []string) { sort.Strings(values) }
