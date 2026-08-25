package operations

import (
	"bytes"
	"context"
	"testing"

	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/protocol"
)

func newRegistry(t *testing.T) (*Registry, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	return NewRegistry(logger.New(logger.Options{Service: "agent", Output: &buf})), &buf
}

func TestDispatchPing(t *testing.T) {
	reg, _ := newRegistry(t)

	resp := reg.Dispatch(context.Background(), protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: "req_1",
	})

	if resp.Status != protocol.StatusSuccess {
		t.Fatalf("expected success, got %+v", resp)
	}
	if resp.RequestID != "req_1" {
		t.Fatalf("request id must be echoed, got %q", resp.RequestID)
	}
	if resp.Data["pong"] != true {
		t.Fatalf("unexpected ping payload: %+v", resp.Data)
	}
}

func TestDispatchRejectsUnknownOperations(t *testing.T) {
	reg, _ := newRegistry(t)

	// Command-injection shaped operation strings must be refused by the
	// allowlist before any handler lookup.
	for _, op := range []protocol.OperationType{
		"",
		"shell.exec",
		"agent.ping; rm -rf /",
		"$(cat /etc/shadow)",
		"../../bin/sh",
	} {
		resp := reg.Dispatch(context.Background(), protocol.Request{
			Operation: op,
			RequestID: "req_1",
		})
		if resp.Status != protocol.StatusFailed {
			t.Fatalf("operation %q must fail, got %+v", op, resp)
		}
		if resp.Error == nil || resp.Error.Code != protocol.CodeUnknownOperation {
			t.Fatalf("operation %q: expected UNKNOWN_OPERATION, got %+v", op, resp.Error)
		}
	}
}

func TestDispatchRejectsMissingRequestID(t *testing.T) {
	reg, _ := newRegistry(t)

	resp := reg.Dispatch(context.Background(), protocol.Request{Operation: protocol.OperationPing})
	if resp.Status != protocol.StatusFailed {
		t.Fatalf("missing request_id must fail, got %+v", resp)
	}
}

func TestErrorResponsesCarryNoInternalDetail(t *testing.T) {
	reg, _ := newRegistry(t)

	resp := reg.Dispatch(context.Background(), protocol.Request{
		Operation: "cat /etc/shadow",
		RequestID: "req_1",
	})
	if resp.Error == nil {
		t.Fatal("expected an error response")
	}
	if resp.Error.Message != "Operation is not permitted" {
		t.Fatalf("error message must be generic, got %q", resp.Error.Message)
	}
}

func TestRegisterRejectsNonAllowlistedOperation(t *testing.T) {
	reg, _ := newRegistry(t)

	defer func() {
		if recover() == nil {
			t.Fatal("registering a non-allowlisted operation must panic at startup")
		}
	}()
	reg.mustRegister("not.allowlisted", handlePing)
}

func TestRegisteredOperationsAreAllAllowlisted(t *testing.T) {
	reg, _ := newRegistry(t)

	for _, op := range reg.Operations() {
		if !protocol.IsAllowed(op) {
			t.Fatalf("registered operation %q is not on the allowlist", op)
		}
	}
}
