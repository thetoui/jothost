package seal

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

// A small chunk size throughout, so a few dozen bytes cross several chunk
// boundaries and every edge - an exact multiple, one over, one under, empty -
// is reachable without megabytes of data.
const testChunk = 16

func testKey() []byte {
	key := make([]byte, KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

func sealBytes(t *testing.T, key, plain []byte, chunk int) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := newWriter(&out, key, chunk, rand.Reader)
	if err != nil {
		t.Fatalf("newWriter: %v", err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return out.Bytes()
}

func openBytes(key, sealed []byte) ([]byte, error) {
	r, err := NewReader(bytes.NewReader(sealed), key)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + 3)
	}
	return b
}

func TestRoundTripAtEveryBoundary(t *testing.T) {
	key := testKey()
	for _, size := range []int{0, 1, testChunk - 1, testChunk, testChunk + 1,
		2 * testChunk, 2*testChunk + 1, 7*testChunk + 5} {
		plain := pattern(size)
		got, err := openBytes(key, sealBytes(t, key, plain, testChunk))
		if err != nil {
			t.Fatalf("size %d: open: %v", size, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("size %d: the plaintext did not come back intact", size)
		}
	}
}

func TestHowTheWritesAreSplitDoesNotMatter(t *testing.T) {
	key := testKey()
	plain := pattern(5*testChunk + 3)

	var out bytes.Buffer
	w, err := newWriter(&out, key, testChunk, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range plain {
		if _, err := w.Write([]byte{b}); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := openBytes(key, out.Bytes())
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("byte-at-a-time writes did not round-trip: %v", err)
	}
}

func TestTheSameInputSealsDifferentlyEachTime(t *testing.T) {
	// A fresh salt per stream means a fresh key per stream; identical backups
	// must not produce identical ciphertext.
	key := testKey()
	plain := pattern(40)
	a := sealBytes(t, key, plain, testChunk)
	b := sealBytes(t, key, plain, testChunk)
	if bytes.Equal(a, b) {
		t.Fatal("two seals of the same plaintext were identical")
	}
}

func TestTheWrongKeyOpensNothing(t *testing.T) {
	key := testKey()
	sealed := sealBytes(t, key, pattern(40), testChunk)

	wrong := testKey()
	wrong[0] ^= 1
	got, err := openBytes(wrong, sealed)
	if !errors.Is(err, ErrOpen) {
		t.Fatalf("the wrong key gave %v, want ErrOpen", err)
	}
	if len(got) != 0 {
		t.Fatalf("the wrong key yielded %d bytes of plaintext", len(got))
	}
}

func TestEverySingleByteChangeIsRefused(t *testing.T) {
	// Header, salt, chunk size, every ciphertext byte and every tag byte.
	key := testKey()
	sealed := sealBytes(t, key, pattern(3*testChunk+5), testChunk)

	for i := range sealed {
		altered := bytes.Clone(sealed)
		altered[i] ^= 0x01
		if _, err := openBytes(key, altered); err == nil {
			t.Fatalf("changing byte %d of %d went unnoticed", i, len(sealed))
		}
	}
}

func TestAStreamCutOffAtAChunkBoundaryIsTruncated(t *testing.T) {
	// The attack the final flag exists for: drop whole chunks from the end,
	// and what remains is a sequence of valid chunks. It must not read as a
	// shorter, valid stream.
	key := testKey()
	sealed := sealBytes(t, key, pattern(3*testChunk+5), testChunk)
	chunk := testChunk + tagSize

	for kept := 0; kept <= 3; kept++ {
		cut := sealed[:headerSize+kept*chunk]
		_, err := openBytes(key, cut)
		if !errors.Is(err, ErrTruncated) {
			t.Fatalf("keeping %d whole chunks gave %v, want ErrTruncated", kept, err)
		}
	}
}

func TestAStreamCutOffInsideAChunkIsRefused(t *testing.T) {
	key := testKey()
	sealed := sealBytes(t, key, pattern(3*testChunk+5), testChunk)

	for cut := headerSize + 1; cut < len(sealed); cut++ {
		if _, err := openBytes(key, sealed[:cut]); err == nil {
			t.Fatalf("a stream cut at byte %d of %d opened", cut, len(sealed))
		}
	}
}

func TestReorderedChunksAreRefused(t *testing.T) {
	key := testKey()
	sealed := sealBytes(t, key, pattern(3*testChunk+5), testChunk)
	chunk := testChunk + tagSize

	swapped := bytes.Clone(sealed)
	first := swapped[headerSize : headerSize+chunk]
	second := swapped[headerSize+chunk : headerSize+2*chunk]
	tmp := bytes.Clone(first)
	copy(first, second)
	copy(second, tmp)

	if _, err := openBytes(key, swapped); !errors.Is(err, ErrOpen) {
		t.Fatalf("swapping two chunks gave %v, want ErrOpen", err)
	}
}

// Refused by the framing itself, for a short final chunk and for a full one
// alike; the explicit trailing-data check in the reader is a second line for
// a source that keeps producing bytes after its end, and is not what these
// cases rely on.
func TestDataAfterTheFinalChunkIsRefused(t *testing.T) {
	key := testKey()
	short := sealBytes(t, key, pattern(2*testChunk+5), testChunk)
	full := sealBytes(t, key, pattern(2*testChunk), testChunk)

	for _, sealed := range [][]byte{short, full} {
		for _, extra := range [][]byte{{0}, pattern(testChunk + tagSize)} {
			appended := append(bytes.Clone(sealed), extra...)
			if _, err := openBytes(key, appended); err == nil {
				t.Fatalf("%d bytes after a %d-byte stream went unnoticed", len(extra), len(sealed))
			}
		}
	}
}

// A source that reports the end of the stream and then produces more.
type growingReader struct {
	data  []byte
	extra []byte
	eofs  int
}

func (g *growingReader) Read(p []byte) (int, error) {
	if len(g.data) > 0 {
		n := copy(p, g.data)
		g.data = g.data[n:]
		return n, nil
	}
	if g.eofs == 0 {
		g.eofs++
		return 0, io.EOF
	}
	if len(g.extra) > 0 {
		n := copy(p, g.extra)
		g.extra = g.extra[n:]
		return n, nil
	}
	return 0, io.EOF
}

func TestDataArrivingAfterTheEndIsRefused(t *testing.T) {
	// The case the explicit trailing check exists for: the framing saw a
	// complete stream, and then the source produced more.
	key := testKey()
	sealed := sealBytes(t, key, pattern(2*testChunk+5), testChunk)

	r, err := NewReader(&growingReader{data: sealed, extra: []byte("more")}, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); !errors.Is(err, ErrTrailing) {
		t.Fatalf("data arriving after the end gave %v, want ErrTrailing", err)
	}
}

func TestAHeaderThatIsNotOursIsRefused(t *testing.T) {
	key := testKey()
	sealed := sealBytes(t, key, pattern(20), testChunk)

	notOurs := bytes.Clone(sealed)
	copy(notOurs, "JHSEAL2\n")
	if _, err := openBytes(key, notOurs); !errors.Is(err, ErrFormat) {
		t.Fatalf("an unknown version gave %v, want ErrFormat", err)
	}

	if _, err := openBytes(key, []byte("gzip or something")); !errors.Is(err, ErrFormat) {
		t.Fatalf("a short, foreign file gave %v, want ErrFormat", err)
	}

	// A header claiming an absurd chunk size is refused before anything is
	// allocated for it.
	huge := bytes.Clone(sealed)
	copy(huge[len(magic)+saltSize:], []byte{0xff, 0xff, 0xff, 0xff})
	if _, err := openBytes(key, huge); !errors.Is(err, ErrFormat) {
		t.Fatalf("a 4 GiB chunk size gave %v, want ErrFormat", err)
	}
}

func TestKeysMustBeTheRightSize(t *testing.T) {
	if _, err := NewWriter(io.Discard, make([]byte, 16)); !errors.Is(err, ErrKey) {
		t.Fatalf("a 16-byte key was accepted for writing: %v", err)
	}
	if _, err := NewReader(strings.NewReader(""), make([]byte, 31)); !errors.Is(err, ErrKey) {
		t.Fatalf("a 31-byte key was accepted for reading: %v", err)
	}

	good := strings.Repeat("ab", KeySize)
	if key, err := ParseHexKey(good); err != nil || len(key) != KeySize {
		t.Fatalf("a valid 64-character key was refused: %v", err)
	}
	for _, bad := range []string{"", "abcd", strings.Repeat("zz", KeySize), good + "00"} {
		if _, err := ParseHexKey(bad); !errors.Is(err, ErrKey) {
			t.Fatalf("ParseHexKey(%q) = %v, want ErrKey", bad, err)
		}
	}
}

func TestWritingAfterCloseIsAnError(t *testing.T) {
	w, err := NewWriter(io.Discard, testKey())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("late")); !errors.Is(err, ErrClosed) {
		t.Fatalf("a write after Close gave %v, want ErrClosed", err)
	}
}

// knownAnswer pins the format. A backup written today has to open with the
// code of a year from now; a change that altered the derivation, the nonce
// layout, the associated data or the header would pass every round-trip test
// above and still make every existing archive unreadable. This one fails.
//
// Key 00…1f, salt of zero bytes, chunk size 16, and the plaintext below.
const knownAnswer = "4a485345414c310a000000000000000000000000000000000000000000000000000000000000000000000010d4546e603b87a75c78fe0d2e33ed259b6124814136dc494e35ff58325ddd5eb3e5dbe2cc53bfddecdb73610567d73636746043184978ee8b8a5096cdad95da83211a1f770476c46dc3d82d8ab169adb7cd0120925bc5dbf49d29d1"

func TestTheFormatHasNotChanged(t *testing.T) {
	plain := []byte("The quick brown fox jumps over the lazy dog")

	var out bytes.Buffer
	w, err := newWriter(&out, testKey(), testChunk, bytes.NewReader(make([]byte, saltSize)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(out.Bytes()); got != knownAnswer {
		t.Fatalf("the sealed format changed; archives written before this change will not open.\ngot:  %s\nwant: %s", got, knownAnswer)
	}

	vector, err := hex.DecodeString(knownAnswer)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := openBytes(testKey(), vector)
	if err != nil || !bytes.Equal(opened, plain) {
		t.Fatalf("the known-answer vector did not open to its plaintext: %v", err)
	}
}
