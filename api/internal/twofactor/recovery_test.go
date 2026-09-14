package twofactor

import (
	"strings"
	"testing"
)

func TestGeneratedRecoveryCodesHaveTheDocumentedShape(t *testing.T) {
	codes, err := GenerateRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != RecoveryCodeCount {
		t.Fatalf("%d codes, want %d", len(codes), RecoveryCodeCount)
	}
	for _, code := range codes {
		// Four groups of four, from the alphabet with no look-alikes.
		groups := strings.Split(code, "-")
		if len(groups) != 4 {
			t.Fatalf("%q is not four groups", code)
		}
		for _, group := range groups {
			if len(group) != 4 {
				t.Fatalf("%q has a group that is not four characters", code)
			}
			for _, r := range group {
				if !strings.ContainsRune(recoveryAlphabet, r) {
					t.Fatalf("%q contains %q, outside the alphabet", code, r)
				}
			}
		}
		if normalized, ok := NormalizeRecoveryCode(code); !ok || len(normalized) != recoveryCodeLength {
			t.Fatalf("a generated code %q does not normalize", code)
		}
	}
}

func TestNormalizeRecoveryCode(t *testing.T) {
	for input, want := range map[string]string{
		"abcd-efgh-jkmn-pqrs":     "abcdefghjkmnpqrs",
		"ABCD EFGH JKMN PQRS":     "abcdefghjkmnpqrs",
		" abcdefghjkmnpqrs\t":     "abcdefghjkmnpqrs",
		"ab-cd-ef-gh-jk-mn-pq-rs": "abcdefghjkmnpqrs",
	} {
		if got, ok := NormalizeRecoveryCode(input); !ok || got != want {
			t.Errorf("NormalizeRecoveryCode(%q) = %q, %v; want %q", input, got, ok, want)
		}
	}
	for _, input := range []string{
		"", "abcd-efgh-jkmn", "abcd-efgh-jkmn-pqrst",
		"abcd-efgh-jkmn-pqr0", // 0 is not in the alphabet
		"abcd-efgh-jkmn-pqr'", "abcd;efgh-jkmn-pqrs",
	} {
		if got, ok := NormalizeRecoveryCode(input); ok {
			t.Errorf("NormalizeRecoveryCode(%q) accepted it as %q", input, got)
		}
	}
}

func TestARecoveryCodeHashIsBoundToItsUser(t *testing.T) {
	if hashRecoveryCode("user-a", "abcdefghjkmnpqrs") == hashRecoveryCode("user-b", "abcdefghjkmnpqrs") {
		t.Fatal("the same code hashes the same for two users")
	}
}
