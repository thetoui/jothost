package crypt

import (
	"strings"
	"testing"
)

// The specification's own vectors.
//
// These are the point of this file. An implementation of SHA-512 crypt is a
// sequence of digest operations with no structure that would make a mistake
// obvious: a misplaced step produces a hash that is the right length, stable,
// and wrong — and a panel with a wrong one writes mailboxes nobody can log in
// to, for a password that is correct.
//
// So the check is against known-good output rather than against itself. The
// integration suite then does the other half, which is the half that really
// matters: it asks Dovecot to verify a hash this code produced.
func TestTheSpecificationsVectors(t *testing.T) {
	cases := []struct {
		name     string
		password string
		salt     string
		rounds   int
		want     string
	}{
		{
			name:     "the default round count",
			password: "Hello world!",
			salt:     "saltstring",
			rounds:   5000,
			want: "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQ" +
				"JuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1",
		},
		{
			name:     "an explicit round count",
			password: "Hello world!",
			salt:     "saltstringsaltstring",
			rounds:   10000,
			want: "$6$rounds=10000$saltstringsaltst$OW1/O6BYHV6BcXZu8QVeXbDWra3Oeqh0sbHb" +
				"bMCVNSnCM/UrjmM0Dp8vOuZeHBy/YTBmSK6H9qs/y3RnOaw5v.",
		},
		{
			// The salt is longer than the format allows and is truncated to 16
			// characters. Worth a case of its own: a caller that generated a
			// long salt and an implementation that did not truncate would
			// produce hashes crypt(3) cannot verify.
			//
			// The specification prints this vector with "rounds=5000$" in it,
			// because its input named the count explicitly. This package omits
			// the field at the format's default, which is what the format says
			// to do — the digest is the same one, and crypt(3) verifies a
			// password against either spelling.
			name:     "a salt longer than the format allows",
			password: "This is just a test",
			salt:     "toolongsaltstring",
			rounds:   5000,
			want: "$6$toolongsaltstrin$lQ8jolhgVRVhY4b5pZKaysCLi0QBxGoNeKQz" +
				"Q3glMhwllF7oGDZxUhx1yxdYcz/e1JSbq3y6JMxxl8audkUEm0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := HashWith(tc.password, tc.salt, tc.rounds)
			if err != nil {
				t.Fatalf("HashWith: %v", err)
			}
			if got != tc.want {
				t.Errorf("hash mismatch\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

func TestEachHashGetsItsOwnSalt(t *testing.T) {
	// Two mailboxes with the same password must not have the same hash. It is
	// the whole reason a salt exists, and it is the one property of this
	// package a caller can check without knowing the algorithm.
	first, err := Hash("the same password")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	second, err := Hash("the same password")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if first == second {
		t.Fatal("two hashes of one password are identical, so the salt is not random")
	}
}

func TestTheCostIsWrittenIntoTheHash(t *testing.T) {
	// A hash that does not say its own round count is one that silently gets
	// cheaper if this panel's default ever drops.
	hash, err := Hash("something")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if !strings.HasPrefix(hash, "$6$rounds=25000$") {
		t.Errorf("the hash does not record its cost: %s", hash)
	}
}

func TestTheSchemedFormIsWhatDovecotReads(t *testing.T) {
	hash, err := SchemedHash("something")
	if err != nil {
		t.Fatalf("SchemedHash: %v", err)
	}
	if !strings.HasPrefix(hash, "{SHA512-CRYPT}$6$") {
		t.Errorf("a passwd-file entry needs its scheme in front: %s", hash)
	}
	if !IsHash(hash) {
		t.Error("SchemedHash produced something IsHash does not recognise")
	}
}

func TestARoundCountOutsideTheFormatIsRefused(t *testing.T) {
	if _, err := HashWith("password", "salt", 10); err == nil {
		t.Error("a round count below the format's minimum was accepted")
	}
	if _, err := HashWith("password", "salt", MaxRounds+1); err == nil {
		t.Error("a round count above the format's maximum was accepted")
	}
}

func TestASaltOutsideTheAlphabetIsRefused(t *testing.T) {
	// Not pedantry: crypt(3) reads the salt up to the "$", so a salt
	// containing one produces a hash whose salt is a prefix of the one this
	// package thinks it used — and it would then never verify.
	if _, err := HashWith("password", "bad$salt", DefaultRounds); err == nil {
		t.Error("a salt containing a field separator was accepted")
	}
}

func TestIsHashRefusesWhatIsNotOne(t *testing.T) {
	for _, value := range []string{
		"",
		"hunter2",
		"$6$saltonly",
		"$2y$10$abcdefghijklmnopqrstuv",
		// The right prefix and the wrong length: a truncated hash.
		"$6$rounds=25000$saltsaltsaltsalt$tooshort",
	} {
		if IsHash(value) {
			t.Errorf("IsHash accepted %q, which is not a hash", value)
		}
	}
}

func TestTheEmptyPasswordStillHashes(t *testing.T) {
	// Not because an empty password is allowed — validation refuses one long
	// before here — but because the algorithm's length-driven loops are where
	// an off-by-one would live, and zero is where it would show.
	hash, err := HashWith("", "saltstring", 5000)
	if err != nil {
		t.Fatalf("HashWith: %v", err)
	}
	if !IsHash(hash) {
		t.Errorf("the empty password produced something malformed: %s", hash)
	}
}
