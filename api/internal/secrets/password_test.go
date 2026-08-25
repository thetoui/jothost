package secrets

import (
	"strings"
	"testing"
)

const validPassword = "correct-horse-battery-staple"

func TestHashAndVerify(t *testing.T) {
	hash, err := HashPassword(validPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	ok, err := VerifyPassword(validPassword, hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !ok {
		t.Fatal("the correct password must verify")
	}
}

func TestVerifyRejectsWrongPassword(t *testing.T) {
	hash, err := HashPassword(validPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	for _, candidate := range []string{
		"correct-horse-battery-stapl",   // one character short
		"correct-horse-battery-staple ", // trailing space
		"Correct-horse-battery-staple",  // different case
		"",
	} {
		ok, err := VerifyPassword(candidate, hash)
		if err != nil {
			t.Fatalf("VerifyPassword(%q): %v", candidate, err)
		}
		if ok {
			t.Fatalf("password %q must not verify", candidate)
		}
	}
}

func TestHashIsSaltedPerCall(t *testing.T) {
	first, err := HashPassword(validPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	second, err := HashPassword(validPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	// Identical passwords must produce different hashes, or a stolen database
	// would reveal which accounts share a password.
	if first == second {
		t.Fatal("two hashes of the same password must differ")
	}
}

func TestHashNeverContainsThePassword(t *testing.T) {
	hash, err := HashPassword(validPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if strings.Contains(hash, validPassword) {
		t.Fatalf("hash must not embed the password: %s", hash)
	}
}

func TestHashFormatIsPHC(t *testing.T) {
	hash, err := HashPassword(validPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if !strings.HasPrefix(hash, "$argon2id$v=19$") {
		t.Fatalf("unexpected hash format: %s", hash)
	}
	if parts := strings.Split(hash, "$"); len(parts) != 6 {
		t.Fatalf("PHC string must have 6 segments, got %d: %s", len(parts), hash)
	}
}

func TestPasswordPolicy(t *testing.T) {
	if err := ValidatePassword(strings.Repeat("a", MinPasswordLength-1)); err == nil {
		t.Fatal("a short password must be rejected")
	}
	if err := ValidatePassword(strings.Repeat("a", MinPasswordLength)); err != nil {
		t.Fatalf("a password at the minimum length must be accepted: %v", err)
	}
	if err := ValidatePassword(strings.Repeat("a", MaxPasswordLength+1)); err == nil {
		t.Fatal("an oversized password must be rejected")
	}
}

func TestHashRejectsPolicyViolations(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("HashPassword must enforce the password policy")
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	// A corrupted or attacker-supplied hash must produce an error, never a
	// silent match.
	malformed := []string{
		"",
		"not-a-hash",
		"$argon2id$v=19$m=47104,t=1,p=1$onlyfoursegments",
		"$bcrypt$v=19$m=47104,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=18$m=47104,t=1,p=1$c2FsdA$aGFzaA", // wrong version
		"$argon2id$v=19$m=0,t=0,p=0$c2FsdA$aGFzaA",     // zero parameters
		"$argon2id$v=19$m=47104,t=1,p=1$!!!$aGFzaA",    // bad base64 salt
	}

	for _, hash := range malformed {
		ok, err := VerifyPassword(validPassword, hash)
		if ok {
			t.Fatalf("malformed hash %q must never verify", hash)
		}
		if err == nil {
			t.Fatalf("malformed hash %q must report an error", hash)
		}
	}
}

func TestNeedsRehash(t *testing.T) {
	current, err := HashPassword(validPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if NeedsRehash(current) {
		t.Fatal("a hash at current parameters must not need rehashing")
	}

	// A hash produced with weaker parameters must be flagged for upgrade.
	weak := "$argon2id$v=19$m=1024,t=1,p=1$c2FsdHNhbHRzYWx0c2E$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaA"
	if !NeedsRehash(weak) {
		t.Fatal("a weaker hash must be flagged for rehashing")
	}
	if !NeedsRehash("garbage") {
		t.Fatal("an unparsable hash must be flagged for rehashing")
	}
}

func TestVerifyDoesNotHashOversizedCandidates(t *testing.T) {
	hash, err := HashPassword(validPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	// An enormous candidate must be rejected without spending Argon2 work,
	// otherwise the login endpoint is a memory-amplification vector.
	ok, err := VerifyPassword(strings.Repeat("a", MaxPasswordLength+1), hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if ok {
		t.Fatal("an oversized candidate must not verify")
	}
}

func BenchmarkHashPassword(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := HashPassword(validPassword); err != nil {
			b.Fatal(err)
		}
	}
}
