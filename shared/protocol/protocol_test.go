package protocol

import (
	"errors"
	"testing"
)

func TestValidateAcceptsAllowlistedOperation(t *testing.T) {
	req := Request{Operation: OperationPing, RequestID: "req_1"}
	if err := req.Validate(); err != nil {
		t.Fatalf("expected allowlisted operation to validate, got %v", err)
	}
}

func TestValidateRejectsUnknownOperation(t *testing.T) {
	// Anything outside the allowlist must be refused, including strings that
	// look like shell payloads.
	for _, op := range []OperationType{
		// A plausible-looking name that is not registered. "website.create"
		// used to stand here and became a real operation in Phase 4, so this
		// deliberately names something no phase is going to implement.
		"website.reticulate",
		"; rm -rf /",
		"agent.ping; cat /etc/shadow",
		"AGENT.PING",
	} {
		req := Request{Operation: op, RequestID: "req_1"}
		err := req.Validate()
		if err == nil {
			t.Fatalf("operation %q must be rejected", op)
		}
		if !errors.Is(err, ErrUnknownOperation) {
			t.Fatalf("operation %q: expected ErrUnknownOperation, got %v", op, err)
		}
	}
}

func TestValidateRequiresEnvelopeFields(t *testing.T) {
	if err := (Request{RequestID: "req_1"}).Validate(); err == nil {
		t.Fatal("empty operation must be rejected")
	}
	if err := (Request{Operation: OperationPing}).Validate(); err == nil {
		t.Fatal("empty request_id must be rejected")
	}
}

func TestIsAllowed(t *testing.T) {
	if !IsAllowed(OperationPing) {
		t.Fatal("agent.ping must be allowlisted")
	}
	if IsAllowed("nope") {
		t.Fatal("unregistered operation must not be allowlisted")
	}
}
