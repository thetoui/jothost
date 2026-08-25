package secrets

import (
	"strings"
	"testing"
)

func TestNewTokenIsUniqueAndOpaque(t *testing.T) {
	seen := make(map[string]struct{}, 500)

	for i := 0; i < 500; i++ {
		token, err := NewToken()
		if err != nil {
			t.Fatalf("NewToken: %v", err)
		}
		if _, dup := seen[token]; dup {
			t.Fatalf("duplicate token generated: %q", token)
		}
		seen[token] = struct{}{}

		// 32 random bytes in unpadded base64url is 43 characters.
		if len(token) != 43 {
			t.Fatalf("token %q has length %d, want 43", token, len(token))
		}
		if strings.ContainsAny(token, "+/=") {
			t.Fatalf("token %q must be URL-safe and unpadded", token)
		}
	}
}

func TestHashTokenIsStableAndHidesTheToken(t *testing.T) {
	token, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}

	hash := HashToken(token)
	if hash != HashToken(token) {
		t.Fatal("hashing the same token twice must produce the same value")
	}
	if len(hash) != 64 {
		t.Fatalf("expected a 64-character hex digest, got %d", len(hash))
	}
	if strings.Contains(hash, token) {
		t.Fatal("the hash must not contain the token")
	}
}

func TestHashTokenDistinguishesTokens(t *testing.T) {
	a, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	b, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}

	if HashToken(a) == HashToken(b) {
		t.Fatal("different tokens must hash differently")
	}
}

func TestEqualTokenHash(t *testing.T) {
	hash := HashToken("token")

	if !EqualTokenHash(hash, HashToken("token")) {
		t.Fatal("identical hashes must compare equal")
	}
	if EqualTokenHash(hash, HashToken("other")) {
		t.Fatal("different hashes must not compare equal")
	}
	if EqualTokenHash(hash, "") {
		t.Fatal("an empty hash must not match")
	}
}
