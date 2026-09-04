// Package crypt implements the SHA-512 variant of crypt(3), the password hash
// format Dovecot's passwd-file understands.
//
// # Why this exists at all
//
// A mailbox password has to reach Dovecot as a hash in a format Dovecot can
// verify. There are three ways to produce one and this package is the third.
//
// The first is to shell out to `doveadm pw`, which takes the password as an
// argument — and the process table on a Linux host is world-readable, so every
// account on the machine can see it for as long as the command runs. The second
// is to feed it on standard input, which works but puts the panel's only
// password-hashing step behind an external program's prompt-handling, on a host
// where that program may not be installed.
//
// The third is to compute it, which is what this does: no plaintext leaves the
// process it arrived in, and no external program is involved. The format is
// fully specified (Ulrich Drepper's SHA-crypt specification, the same one glibc
// and musl implement), and the tests check this implementation against both the
// specification's own published vectors and, in the integration suite, against
// Dovecot itself — `doveadm pw -t` verifying a hash this package produced is
// the only proof that actually matters.
//
// # Why SHA-512 crypt and not Argon2id
//
// The panel hashes its *own* users' passwords with Argon2id, and that is the
// better algorithm: it is memory-hard, so an attacker with a graphics card gains
// far less from it. This is not that, and the difference is not carelessness.
//
// A mailbox password is verified by Dovecot, not by the panel, so the format is
// Dovecot's choice rather than the panel's. Dovecot supports ARGON2ID only when
// it was built against libsodium, which is a build option rather than a
// guarantee — a panel that wrote ARGON2ID hashes would produce mailboxes that
// authenticate on one distribution's package and fail to authenticate on
// another's, with the failure appearing as "password incorrect" for a password
// that is correct. SHA512-CRYPT is supported by every build of Dovecot there is.
//
// What the panel does about the weaker algorithm is spend more on it:
// DefaultRounds is five times the format's default, which costs a few
// milliseconds per login and multiplies the cost of an offline attack by the
// same factor.
package crypt

import (
	"crypto/rand"
	"crypto/sha512"
	"errors"
	"fmt"
	"strings"
)

// Prefix identifies a SHA-512 crypt hash.
const Prefix = "$6$"

// Scheme is the name Dovecot knows this format by. It is written in front of
// the hash in the passwd-file so Dovecot does not have to guess the scheme from
// the shape of the string — and a passwd-file whose scheme is guessed is one
// where a change of default silently stops every login working.
const Scheme = "SHA512-CRYPT"

// Round bounds, from the specification.
const (
	// MinRounds is the fewest the format allows.
	MinRounds = 1000
	// MaxRounds is the most. Far beyond anything sensible; it is here so a
	// caller cannot ask for a number that overflows into a fast hash.
	MaxRounds = 999999999
	// DefaultRounds is what this panel uses.
	//
	// Five times the format's own default of 5000. The cost is a few
	// milliseconds on each IMAP login, which nobody notices; the benefit is
	// that every guess in an offline attack against a leaked hash costs five
	// times as much. Written explicitly into the hash as "rounds=25000$", so a
	// hash produced today keeps its cost even if this constant changes.
	DefaultRounds = 25000
)

// SaltLength is the number of salt characters, which is the format's maximum.
//
// The maximum rather than something shorter because the salt is what stops one
// precomputed table from covering every mailbox on the host: two people who
// choose the same password must not produce the same hash.
const SaltLength = 16

// itoa64 is the alphabet crypt(3) encodes with. It is not standard base64 and
// the order matters: a hash encoded with the standard alphabet is a hash no
// crypt implementation will verify.
const itoa64 = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// Errors returned by this package.
var (
	// ErrInvalidSalt covers a salt containing characters the format cannot
	// represent.
	ErrInvalidSalt = errors.New("invalid crypt salt")
	// ErrInvalidRounds covers a round count outside the format's range.
	ErrInvalidRounds = errors.New("invalid crypt rounds")
)

// Hash produces a SHA-512 crypt hash of password with a fresh random salt.
//
// The returned string is the bare crypt hash, beginning "$6$rounds=...$". It
// carries no scheme prefix: see SchemedHash for the form that goes into a
// Dovecot passwd-file.
func Hash(password string) (string, error) {
	salt, err := newSalt()
	if err != nil {
		return "", err
	}
	return HashWith(password, salt, DefaultRounds)
}

// SchemedHash produces a hash with the "{SHA512-CRYPT}" prefix Dovecot expects.
func SchemedHash(password string) (string, error) {
	hash, err := Hash(password)
	if err != nil {
		return "", err
	}
	return "{" + Scheme + "}" + hash, nil
}

// HashWith produces a hash with a caller-supplied salt and round count.
//
// Exported for the tests, which have to reproduce published vectors, and those
// vectors fix both.
func HashWith(password, salt string, rounds int) (string, error) {
	if rounds < MinRounds || rounds > MaxRounds {
		return "", fmt.Errorf("%w: %d is outside %d-%d",
			ErrInvalidRounds, rounds, MinRounds, MaxRounds)
	}
	if len(salt) > SaltLength {
		salt = salt[:SaltLength]
	}
	for _, r := range salt {
		if !strings.ContainsRune(itoa64, r) {
			return "", fmt.Errorf("%w: %q is not in the crypt alphabet", ErrInvalidSalt, r)
		}
	}

	sum := sha512Crypt([]byte(password), []byte(salt), rounds)

	var out strings.Builder
	out.WriteString(Prefix)
	// The rounds field is omitted by the format when it is the default 5000.
	// This panel never uses the default, but the branch is here because a hash
	// that says its own cost is one that keeps working when the default
	// changes — and because HashWith is what reproduces the published vectors,
	// which include unrounded ones.
	if rounds != 5000 {
		fmt.Fprintf(&out, "rounds=%d$", rounds)
	}
	out.WriteString(salt)
	out.WriteByte('$')
	out.WriteString(encode(sum))
	return out.String(), nil
}

// sha512Crypt is the specification's algorithm, step for step.
//
// The comments name the steps as the specification numbers them, because the
// only way to check an implementation of this against its source is side by
// side: it is a sequence of digest operations with no structure that would make
// a mistake obvious, and a single misplaced step produces a hash that is stable,
// well-formed, and wrong.
func sha512Crypt(password, salt []byte, rounds int) []byte {
	// Steps 4-8: digest B is the password, the salt, and the password again.
	b := sha512.New()
	b.Write(password)
	b.Write(salt)
	b.Write(password)
	digestB := b.Sum(nil)

	// Steps 1-3 and 9-12: digest A starts with the password and the salt, then
	// takes one byte of B for each byte of the password.
	a := sha512.New()
	a.Write(password)
	a.Write(salt)
	for count := len(password); count > sha512.Size; count -= sha512.Size {
		a.Write(digestB)
	}
	a.Write(digestB[:len(password)%sha512.Size])

	// Step 13: walk the bits of the password length, low bit first, adding B
	// for a set bit and the password for a clear one.
	for count := len(password); count > 0; count >>= 1 {
		if count&1 != 0 {
			a.Write(digestB)
		} else {
			a.Write(password)
		}
	}
	digestA := a.Sum(nil)

	// Steps 15-16: DP is the password repeated once per byte of itself, and the
	// P sequence is DP stretched back out to the password's length.
	dp := sha512.New()
	for i := 0; i < len(password); i++ {
		dp.Write(password)
	}
	sequenceP := stretch(dp.Sum(nil), len(password))

	// Steps 17-19: DS is the salt repeated 16 plus the first byte of A times,
	// and the S sequence is DS stretched to the salt's length.
	ds := sha512.New()
	for i := 0; i < 16+int(digestA[0]); i++ {
		ds.Write(salt)
	}
	sequenceS := stretch(ds.Sum(nil), len(salt))

	// Step 21: the rounds. This is the whole cost of the algorithm, and the
	// alternating pattern is what makes it impossible to shortcut.
	current := digestA
	for i := 0; i < rounds; i++ {
		c := sha512.New()
		if i&1 != 0 {
			c.Write(sequenceP)
		} else {
			c.Write(current)
		}
		if i%3 != 0 {
			c.Write(sequenceS)
		}
		if i%7 != 0 {
			c.Write(sequenceP)
		}
		if i&1 != 0 {
			c.Write(current)
		} else {
			c.Write(sequenceP)
		}
		current = c.Sum(nil)
	}
	return current
}

// stretch repeats digest until it is length bytes long.
func stretch(digest []byte, length int) []byte {
	out := make([]byte, 0, length)
	for len(out) < length {
		remaining := length - len(out)
		if remaining > len(digest) {
			remaining = len(digest)
		}
		out = append(out, digest[:remaining]...)
	}
	return out
}

// permutation is the order the output bytes are read in, three at a time.
//
// It is not a pattern anybody would derive; it is copied from the
// specification, where it exists so that the printable form of the hash mixes
// bytes from across the digest rather than reading it in order. Getting one
// index wrong produces a hash that is the right length and never verifies.
var permutation = [21][3]int{
	{0, 21, 42}, {22, 43, 1}, {44, 2, 23},
	{3, 24, 45}, {25, 46, 4}, {47, 5, 26},
	{6, 27, 48}, {28, 49, 7}, {50, 8, 29},
	{9, 30, 51}, {31, 52, 10}, {53, 11, 32},
	{12, 33, 54}, {34, 55, 13}, {56, 14, 35},
	{15, 36, 57}, {37, 58, 16}, {59, 17, 38},
	{18, 39, 60}, {40, 61, 19}, {62, 20, 41},
}

// encode writes a 64-byte digest in crypt(3)'s printable form.
func encode(digest []byte) string {
	var out strings.Builder
	out.Grow(86)
	for _, group := range permutation {
		writeGroup(&out, digest[group[0]], digest[group[1]], digest[group[2]], 4)
	}
	// The 64th byte has no pair, and is written as two characters on its own.
	writeGroup(&out, 0, 0, digest[63], 2)
	return out.String()
}

// writeGroup emits count characters of the little-endian base-64 of three
// bytes.
func writeGroup(out *strings.Builder, b2, b1, b0 byte, count int) {
	value := uint32(b2)<<16 | uint32(b1)<<8 | uint32(b0)
	for i := 0; i < count; i++ {
		out.WriteByte(itoa64[value&0x3f])
		value >>= 6
	}
}

// newSalt draws a random salt from the crypt alphabet.
//
// crypto/rand rather than math/rand, and the error is returned rather than
// swallowed: a salt that is not random is one where two mailboxes with the same
// password have the same hash, which is the property the salt exists to
// destroy.
func newSalt() (string, error) {
	buf := make([]byte, SaltLength)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes for a password salt: %w", err)
	}
	out := make([]byte, SaltLength)
	for i, b := range buf {
		// The alphabet is 64 characters, so masking is a uniform choice over
		// it — no modulo bias to reason about.
		out[i] = itoa64[b&0x3f]
	}
	return string(out), nil
}

// IsHash reports whether value looks like a SHA-512 crypt hash this package
// produced, with or without the Dovecot scheme prefix.
//
// It is a shape check and nothing more: it exists so a repository can refuse to
// store something that is plainly not a hash — a plaintext password that
// reached the wrong function — rather than to validate a hash's contents.
func IsHash(value string) bool {
	value = strings.TrimPrefix(value, "{"+Scheme+"}")
	if !strings.HasPrefix(value, Prefix) {
		return false
	}
	// "$6$", a salt, "$", and 86 characters of digest: four fields at least,
	// counting the empty one before the first "$".
	parts := strings.Split(value, "$")
	return len(parts) >= 4 && len(parts[len(parts)-1]) == 86
}
