package database

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// Errors returned by the transfer store.
var (
	// ErrNoTransfer means the token names nothing. A token that has been
	// consumed, discarded or expired is indistinguishable from one that never
	// existed, which is the intended answer.
	ErrNoTransfer = errors.New("no such transfer")
	// ErrTransferTooLarge means the upload exceeded the cap.
	ErrTransferTooLarge = errors.New("the file is larger than this panel accepts")
)

// SpoolDir is where dumps waiting to be downloaded, and uploads waiting to be
// applied, are kept.
//
// Under the Agent's own state directory and owned by root: a dump is every row
// of somebody's database, and it must not sit anywhere a website's account can
// read it.
const SpoolDir = "/var/lib/jothost/db-transfers"

// MaxTransferBytes caps a single transfer.
//
// A dump larger than this is a job for the backup system, which streams to a
// destination rather than through a browser. Without a cap, an upload is a way
// to fill the host's disk from a form.
const MaxTransferBytes int64 = 2 << 30 // 2 GiB

// transferTTL is how long an unclaimed transfer survives.
//
// A download that is never fetched, or an upload that is never committed,
// would otherwise keep somebody's data on disk indefinitely.
const transferTTL = 2 * time.Hour

// tokenPattern is what a token may contain. Tokens are generated here, so this
// is not parsing untrusted input so much as refusing to look at anything that
// did not come from here.
var tokenPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Transfer is a file being moved between the panel and a database.
type Transfer struct {
	Token string `json:"token"`
	// Name is what a browser should call the download. Never used as a path.
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Created time.Time `json:"created"`
}

// TransferStore holds dumps on their way out and uploads on their way in.
//
// Callers name a token, never a path. That is the whole point of it: the API
// asks for "the file behind this token" and cannot ask for the file behind a
// path, so there is no traversal to attempt and nothing to normalise. The
// mapping from token to filename lives here and nowhere else.
type TransferStore struct {
	dir string

	mu    sync.Mutex
	known map[string]*Transfer
}

// NewTransferStore builds a store rooted at dir. An empty dir means SpoolDir.
func NewTransferStore(dir string) *TransferStore {
	if dir == "" {
		dir = SpoolDir
	}
	return &TransferStore{dir: dir, known: map[string]*Transfer{}}
}

// ensureDir creates the spool, private to root.
//
// 0700: a dump is the contents of a database. The directory is created on
// demand rather than at startup because a host that never exports anything
// should not have one.
func (s *TransferStore) ensureDir() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", s.dir, err)
	}
	// Applied every time, not only on creation: a directory left from an
	// earlier version with a wider mode would otherwise stay wide.
	if err := os.Chmod(s.dir, 0o700); err != nil {
		return fmt.Errorf("secure %s: %w", s.dir, err)
	}
	return nil
}

// pathFor resolves a token to its file.
//
// The token is checked against the pattern before it is joined to anything.
// A token is 32 hex characters and cannot contain a separator or a dot, so
// there is no path for "../" to appear in — but the check is here rather than
// assumed, because this is the one function that turns caller input into a
// filesystem path.
func (s *TransferStore) pathFor(token string) (string, error) {
	if !tokenPattern.MatchString(token) {
		return "", ErrNoTransfer
	}
	return filepath.Join(s.dir, token+".sql"), nil
}

// newToken returns an unpredictable handle.
func newToken() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate a transfer token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// Create reserves a transfer and returns its token and destination path.
//
// The file is deliberately *not* created. mysqldump refuses a destination that
// already exists — rightly, since overwriting one is how a dump silently
// replaces another — so reserving the name and touching nothing is what lets
// the dumper do its job. The directory is 0700 and root-owned, so an
// uncreated file is not a window anybody can reach through; Finish sets the
// file's own mode once it is there.
//
// The path is for this package's own use. It is never returned outside the
// Agent.
func (s *TransferStore) Create(name string) (*Transfer, string, error) {
	if err := s.ensureDir(); err != nil {
		return nil, "", err
	}
	s.expire()

	token, err := newToken()
	if err != nil {
		return nil, "", err
	}
	path, err := s.pathFor(token)
	if err != nil {
		return nil, "", err
	}

	transfer := &Transfer{Token: token, Name: name, Created: time.Now()}

	s.mu.Lock()
	s.known[token] = transfer
	s.mu.Unlock()

	return transfer, path, nil
}

// CreateEmpty reserves a transfer and creates the file, for an upload.
//
// An upload is appended to and so has to exist first. It is created 0600 from
// the outset: a dump is the contents of a database and there is no moment at
// which it should be readable by anything else.
func (s *TransferStore) CreateEmpty(name string) (*Transfer, string, error) {
	transfer, path, err := s.Create(name)
	if err != nil {
		return nil, "", err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("create the transfer file: %w", err)
	}
	_ = file.Close()
	return transfer, path, nil
}

// Finish records the final size of a transfer, and secures the file.
//
// The mode is set here rather than at creation because the dumper creates the
// file itself and does so with its own umask. This is the first moment the
// file exists and the last before anything reads it.
func (s *TransferStore) Finish(token string) (*Transfer, error) {
	path, err := s.pathFor(token)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure the dump: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, ErrNoTransfer
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	transfer, ok := s.known[token]
	if !ok {
		return nil, ErrNoTransfer
	}
	transfer.Size = info.Size()
	copied := *transfer
	return &copied, nil
}

// Read returns one chunk.
func (s *TransferStore) Read(token string, offset int64, length int) ([]byte, bool, error) {
	if offset < 0 || length <= 0 {
		return nil, false, fmt.Errorf("%w: offset and length must be positive", os.ErrInvalid)
	}
	path, err := s.pathFor(token)
	if err != nil {
		return nil, false, err
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, false, ErrNoTransfer
	}
	defer func() { _ = file.Close() }()

	buffer := make([]byte, length)
	read, err := file.ReadAt(buffer, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, fmt.Errorf("read the transfer: %w", err)
	}

	info, statErr := file.Stat()
	eof := errors.Is(err, io.EOF)
	if statErr == nil && offset+int64(read) >= info.Size() {
		eof = true
	}
	return buffer[:read], eof, nil
}

// Append adds bytes to an upload, refusing to exceed the cap.
func (s *TransferStore) Append(token string, data []byte) (int64, error) {
	path, err := s.pathFor(token)
	if err != nil {
		return 0, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return 0, ErrNoTransfer
	}
	if info.Size()+int64(len(data)) > MaxTransferBytes {
		return info.Size(), ErrTransferTooLarge
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return info.Size(), ErrNoTransfer
	}
	defer func() { _ = file.Close() }()

	if _, err := file.Write(data); err != nil {
		return info.Size(), fmt.Errorf("write to the transfer: %w", err)
	}

	size := info.Size() + int64(len(data))
	s.mu.Lock()
	if transfer, ok := s.known[token]; ok {
		transfer.Size = size
	}
	s.mu.Unlock()
	return size, nil
}

// PathFor resolves a token for this package's own use, checking it exists.
func (s *TransferStore) PathFor(token string) (string, error) {
	path, err := s.pathFor(token)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err != nil {
		return "", ErrNoTransfer
	}
	return path, nil
}

// Discard removes a transfer. A token that names nothing is not an error:
// discarding twice is what a caller cleaning up after a failure does.
func (s *TransferStore) Discard(token string) error {
	path, err := s.pathFor(token)
	if err != nil {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove the transfer: %w", err)
	}
	s.mu.Lock()
	delete(s.known, token)
	s.mu.Unlock()
	return nil
}

// expire removes transfers nobody claimed.
//
// Called when a new one is created rather than on a timer: it is the only
// moment the store is guaranteed to be in use, and a host that has stopped
// exporting has nothing left to expire.
func (s *TransferStore) expire() {
	cutoff := time.Now().Add(-transferTTL)

	s.mu.Lock()
	stale := make([]string, 0, 4)
	for token, transfer := range s.known {
		if transfer.Created.Before(cutoff) {
			stale = append(stale, token)
		}
	}
	s.mu.Unlock()

	for _, token := range stale {
		_ = s.Discard(token)
	}

	// Files with no entry in the map: the Agent restarted while a transfer was
	// in flight, so nothing remembers them and nothing ever will.
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		s.mu.Lock()
		_, known := s.known[trimSQL(entry.Name())]
		s.mu.Unlock()
		if !known {
			_ = os.Remove(filepath.Join(s.dir, entry.Name()))
		}
	}
}

func trimSQL(name string) string {
	if len(name) > 4 && name[len(name)-4:] == ".sql" {
		return name[:len(name)-4]
	}
	return name
}
