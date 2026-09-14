// Package seal encrypts a stream so that it can be stored somewhere untrusted
// and later read back only by whoever holds the key - and so that any change
// to it, including cutting it short, is refused rather than read.
//
// It exists for backups of the panel's own database (docs/PANEL_BACKUP.md),
// which hold every account's password hash and every stored credential and are
// sent to storage the panel does not control. It uses the standard library
// only: the Agent, which writes these archives, has no third-party
// dependencies, and the recovery command that reads them must derive exactly
// the same key as the API that asked for them.
//
// # Format
//
//	"JHSEAL1\n"      8 bytes   the format and its version
//	salt             32 bytes  random, fresh for every stream
//	chunk size       4 bytes   big-endian plaintext bytes per chunk
//	chunk 0 … n                AES-256-GCM ciphertext, each with its 16-byte tag
//
// The key for a stream is HKDF-SHA256 over the 32-byte master key, with that
// stream's salt and a fixed label, so no two streams share a key and none of
// them is the master key itself. Each chunk's nonce is an 8-byte counter, three
// zero bytes and a final-chunk flag, and every chunk authenticates the whole
// header.
//
// The flag is the part that matters most. Without it, a stream cut off after
// any whole chunk would open as a shorter stream that is perfectly valid - a
// backup with its last megabytes silently missing. With it, the reader knows a
// stream that ends before a chunk marked final has been truncated.
//
// There is one version and no options. A format with choices is a format with
// weaker choices.
package seal

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

const (
	magic = "JHSEAL1\n"

	// KeySize is the master key's length, the same 32 bytes as the panel's
	// ENCRYPTION_KEY.
	KeySize = 32

	saltSize   = 32
	headerSize = len(magic) + saltSize + 4
	tagSize    = 16
	nonceSize  = 12

	// DefaultChunkSize is how much plaintext each chunk holds.
	DefaultChunkSize = 1 << 20
	// maxChunkSize bounds what a header may claim, so a hostile archive cannot
	// make the reader allocate gigabytes before its first chunk fails to open.
	maxChunkSize = 16 << 20

	label = "jothost panel backup v1"
)

// Errors a reader can act on.
var (
	ErrKey       = fmt.Errorf("seal: the key must be %d bytes", KeySize)
	ErrFormat    = errors.New("seal: not a sealed stream, or a version this build cannot read")
	ErrOpen      = errors.New("seal: a chunk failed to open: the key is wrong, or the stream was altered")
	ErrTruncated = errors.New("seal: the stream ends before its final chunk")
	ErrTrailing  = errors.New("seal: data follows the final chunk")
	ErrClosed    = errors.New("seal: write after close")
)

// MagicSize is how many leading bytes HasMagic needs.
const MagicSize = len(magic)

// HasMagic reports whether prefix begins a sealed stream. It says only what
// the file claims to be; opening it is what proves anything.
func HasMagic(prefix []byte) bool {
	return len(prefix) >= MagicSize && string(prefix[:MagicSize]) == magic
}

// panelBackupLabel separates the backup key from every other use of
// ENCRYPTION_KEY.
const panelBackupLabel = "jothost panel backup master v1"

// PanelBackupKey derives the master key panel-database backups are sealed
// with from the panel's ENCRYPTION_KEY.
//
// It is what the API hands the Agent, instead of ENCRYPTION_KEY itself. That
// key decrypts every credential stored in the panel's database; the Agent
// needs to seal a backup, not to read those credentials, and a key derived
// for this one purpose lets it do the first without being able to do the
// second. The recovery command derives the same key from the escrowed
// ENCRYPTION_KEY, which is why this lives here rather than in either binary.
func PanelBackupKey(encryptionKey []byte) ([]byte, error) {
	if len(encryptionKey) != KeySize {
		return nil, ErrKey
	}
	key, err := hkdf.Key(sha256.New, encryptionKey, nil, panelBackupLabel, KeySize)
	if err != nil {
		return nil, fmt.Errorf("seal: derive the panel backup key: %w", err)
	}
	return key, nil
}

// ParseHexKey decodes a master key written as 64 hexadecimal characters, the
// form ENCRYPTION_KEY takes.
func ParseHexKey(s string) ([]byte, error) {
	key, err := hex.DecodeString(s)
	if err != nil || len(key) != KeySize {
		return nil, fmt.Errorf("%w (64 hexadecimal characters)", ErrKey)
	}
	return key, nil
}

// NewWriter returns a writer that seals everything written to it into w.
//
// Close must be called: it writes the final chunk, and a stream without one is
// refused as truncated. Close does not close w.
func NewWriter(w io.Writer, masterKey []byte) (io.WriteCloser, error) {
	return newWriter(w, masterKey, DefaultChunkSize, rand.Reader)
}

func newWriter(w io.Writer, masterKey []byte, chunkSize int, random io.Reader) (*writer, error) {
	if len(masterKey) != KeySize {
		return nil, ErrKey
	}
	if chunkSize <= 0 || chunkSize > maxChunkSize {
		return nil, fmt.Errorf("seal: chunk size %d is out of range", chunkSize)
	}

	header := make([]byte, headerSize)
	copy(header, magic)
	salt := header[len(magic) : len(magic)+saltSize]
	if _, err := io.ReadFull(random, salt); err != nil {
		return nil, fmt.Errorf("seal: generate the salt: %w", err)
	}
	binary.BigEndian.PutUint32(header[len(magic)+saltSize:], uint32(chunkSize))

	aead, err := streamAEAD(masterKey, salt)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(header); err != nil {
		return nil, fmt.Errorf("seal: write the header: %w", err)
	}

	return &writer{
		out:       w,
		aead:      aead,
		header:    header,
		chunkSize: chunkSize,
		buf:       make([]byte, 0, chunkSize),
		sealed:    make([]byte, 0, chunkSize+tagSize),
	}, nil
}

type writer struct {
	out       io.Writer
	aead      cipher.AEAD
	header    []byte
	chunkSize int
	buf       []byte
	sealed    []byte
	counter   uint64
	closed    bool
	err       error
}

// Write buffers p, sealing each chunk once it is known not to be the last.
//
// A full buffer is not sealed until more data arrives: only then is it certain
// that another chunk follows. That is what lets a stream whose length is an
// exact multiple of the chunk size end on a full chunk marked final, rather
// than on an empty one.
func (s *writer) Write(p []byte) (int, error) {
	if s.closed {
		return 0, ErrClosed
	}
	if s.err != nil {
		return 0, s.err
	}
	written := 0
	for len(p) > 0 {
		if len(s.buf) == s.chunkSize {
			if err := s.seal(false); err != nil {
				return written, err
			}
		}
		n := copy(s.buf[len(s.buf):s.chunkSize], p)
		s.buf = s.buf[:len(s.buf)+n]
		p = p[n:]
		written += n
	}
	return written, nil
}

// Close seals what remains as the final chunk.
func (s *writer) Close() error {
	if s.closed {
		return s.err
	}
	s.closed = true
	if s.err != nil {
		return s.err
	}
	return s.seal(true)
}

func (s *writer) seal(final bool) error {
	nonce := chunkNonce(s.counter, final)
	s.sealed = s.aead.Seal(s.sealed[:0], nonce[:], s.buf, s.header)
	if _, err := s.out.Write(s.sealed); err != nil {
		s.err = fmt.Errorf("seal: write chunk %d: %w", s.counter, err)
		return s.err
	}
	s.buf = s.buf[:0]
	s.counter++
	return nil
}

// NewReader returns a reader of the plaintext sealed in r.
//
// Nothing is returned from a chunk until it has been authenticated. An error
// can still arrive after earlier chunks were read, so a caller restoring from
// the output must treat the whole read as failed if Read ever returns anything
// but io.EOF - and write to a staging copy rather than over what it replaces.
func NewReader(r io.Reader, masterKey []byte) (io.Reader, error) {
	if len(masterKey) != KeySize {
		return nil, ErrKey
	}
	in := bufio.NewReader(r)

	header := make([]byte, headerSize)
	if _, err := io.ReadFull(in, header); err != nil {
		return nil, ErrFormat
	}
	if string(header[:len(magic)]) != magic {
		return nil, ErrFormat
	}
	chunkSize := int(binary.BigEndian.Uint32(header[len(magic)+saltSize:]))
	if chunkSize <= 0 || chunkSize > maxChunkSize {
		return nil, ErrFormat
	}

	aead, err := streamAEAD(masterKey, header[len(magic):len(magic)+saltSize])
	if err != nil {
		return nil, err
	}
	return &reader{
		in:       in,
		aead:     aead,
		header:   header,
		cipher:   make([]byte, chunkSize+tagSize),
		plainBuf: make([]byte, 0, chunkSize),
	}, nil
}

type reader struct {
	in     *bufio.Reader
	aead   cipher.AEAD
	header []byte
	cipher []byte
	// plainBuf is separate from cipher on purpose. Opening in place would be
	// cheaper, but a failed Open may overwrite its destination, and a failed
	// open is followed by a second attempt on the same ciphertext to tell a
	// truncated stream from an altered one.
	plainBuf []byte
	plain    []byte
	counter  uint64
	done     bool
	err      error
}

func (s *reader) Read(p []byte) (int, error) {
	for len(s.plain) == 0 {
		if s.err != nil {
			return 0, s.err
		}
		if s.done {
			return 0, io.EOF
		}
		s.err = s.next()
	}
	n := copy(p, s.plain)
	s.plain = s.plain[n:]
	return n, nil
}

// next reads and opens one chunk.
func (s *reader) next() error {
	n, err := io.ReadFull(s.in, s.cipher)
	switch {
	case errors.Is(err, io.EOF):
		// Nothing at all where a chunk should be: the previous chunk, if any,
		// was not marked final.
		return ErrTruncated
	case errors.Is(err, io.ErrUnexpectedEOF):
		// A short chunk can only legitimately be the last one.
		return s.open(s.cipher[:n], true)
	case err != nil:
		return fmt.Errorf("seal: read chunk %d: %w", s.counter, err)
	}

	// A full chunk is the last one exactly when nothing follows it.
	if _, err := s.in.Peek(1); errors.Is(err, io.EOF) {
		return s.open(s.cipher, true)
	} else if err != nil {
		return fmt.Errorf("seal: read chunk %d: %w", s.counter, err)
	}
	return s.open(s.cipher, false)
}

func (s *reader) open(chunk []byte, last bool) error {
	nonce := chunkNonce(s.counter, last)
	plain, err := s.aead.Open(s.plainBuf[:0], nonce[:], chunk, s.header)
	if err != nil {
		if last {
			// Distinguish a stream cut off after a whole chunk from one that
			// was altered: the chunk opens, just not as the final one.
			other := chunkNonce(s.counter, false)
			if _, againErr := s.aead.Open(nil, other[:], chunk, s.header); againErr == nil {
				return ErrTruncated
			}
		}
		return ErrOpen
	}
	s.counter++
	s.plain = plain

	if last {
		// Bytes appended to a stream are already refused by the framing: they
		// either lengthen a short final chunk, whose tag then fails, or turn a
		// full final chunk into one read as not-final, which fails the same
		// way. What reaches here is a reader that produces more data after
		// reporting the end of it - a file still being appended to - and that
		// is refused too rather than silently ignored.
		if _, err := s.in.Peek(1); err == nil {
			return ErrTrailing
		}
		s.done = true
		if len(plain) == 0 {
			// An empty final chunk: the loop in Read sees no plaintext and
			// returns EOF on its next pass.
			s.plain = nil
		}
	}
	return nil
}

func streamAEAD(masterKey, salt []byte) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, masterKey, salt, label, KeySize)
	if err != nil {
		return nil, fmt.Errorf("seal: derive the stream key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("seal: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("seal: %w", err)
	}
	return aead, nil
}

func chunkNonce(counter uint64, final bool) [nonceSize]byte {
	var nonce [nonceSize]byte
	binary.BigEndian.PutUint64(nonce[:8], counter)
	if final {
		nonce[nonceSize-1] = 1
	}
	return nonce
}
