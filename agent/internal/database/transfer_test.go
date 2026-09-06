package database

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The transfer store is the one place a caller's input becomes a filesystem
// path, so these are mostly about what it refuses.
//
// A dump is every row of somebody's database. It sits on disk between being
// made and being fetched, and the shape of this store — tokens, not paths — is
// what stops the API being able to ask for a file it was never given.

func storeIn(t *testing.T) *TransferStore {
	t.Helper()
	return NewTransferStore(filepath.Join(t.TempDir(), "spool"))
}

func TestATokenIsNotAPath(t *testing.T) {
	store := storeIn(t)

	// Every one of these is a path, and none of them is a token. The store
	// must not resolve any of them, whatever the filesystem would make of it.
	for _, attempt := range []string{
		"../../etc/passwd",
		"..",
		"/etc/passwd",
		"a/b",
		`a\b`,
		"",
		"short",
		strings.Repeat("f", 33),
		"ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ",
		"../0123456789abcdef0123456789abcd",
	} {
		if _, err := store.PathFor(attempt); !errors.Is(err, ErrNoTransfer) {
			t.Fatalf("%q was not refused: %v", attempt, err)
		}
		if _, _, err := store.Read(attempt, 0, 16); !errors.Is(err, ErrNoTransfer) {
			t.Fatalf("reading %q was not refused: %v", attempt, err)
		}
		if _, err := store.Append(attempt, []byte("x")); !errors.Is(err, ErrNoTransfer) {
			t.Fatalf("appending to %q was not refused: %v", attempt, err)
		}
	}
}

func TestATransferRoundTrips(t *testing.T) {
	store := storeIn(t)

	transfer, path, err := store.Create("shop.sql")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	body := []byte("-- a dump\nCREATE TABLE t (id INT);\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write the dump: %v", err)
	}

	finished, err := store.Finish(transfer.Token)
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Size != int64(len(body)) {
		t.Fatalf("size = %d, want %d", finished.Size, len(body))
	}
	if finished.Name != "shop.sql" {
		t.Fatalf("name = %q", finished.Name)
	}

	read, eof, err := store.Read(transfer.Token, 0, len(body))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(read) != string(body) {
		t.Fatalf("read back %q", read)
	}
	if !eof {
		t.Fatal("reading the whole file did not report the end of it")
	}
}

func TestTheFileIsPrivateToRoot(t *testing.T) {
	store := storeIn(t)

	// Create reserves a name and touches nothing: mysqldump refuses a
	// destination that already exists, so the file appears when the dumper
	// writes it, with the dumper's umask. Finish is what secures it.
	transfer, path, err := store.Create("shop.sql")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Create made the file; mysqldump will refuse to write to it")
	}

	// Standing in for the dumper, deliberately with a wide mode.
	if err := os.WriteFile(path, []byte("-- dump\n"), 0o644); err != nil {
		t.Fatalf("write the dump: %v", err)
	}
	if _, err := store.Finish(transfer.Token); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// A dump is the contents of a database. Left 0644 it would be readable by
	// every website account on the host for as long as it sat here.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("the dump is mode %o, want 600", mode)
	}

	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat the spool: %v", err)
	}
	if mode := dir.Mode().Perm(); mode != 0o700 {
		t.Fatalf("the spool is mode %o, want 700", mode)
	}
}

func TestAnUploadCannotExceedTheCap(t *testing.T) {
	store := storeIn(t)
	// An upload, so the file exists to be appended to.
	transfer, path, err := store.CreateEmpty("upload.sql")
	if err != nil {
		t.Fatalf("CreateEmpty: %v", err)
	}

	// Filled to just under the cap without actually writing two gigabytes.
	if err := os.Truncate(path, MaxTransferBytes-4); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	if _, err := store.Append(transfer.Token, []byte("....")); err != nil {
		t.Fatalf("a write that fits was refused: %v", err)
	}
	if _, err := store.Append(transfer.Token, []byte("x")); !errors.Is(err, ErrTransferTooLarge) {
		t.Fatalf("a write past the cap was not refused: %v", err)
	}
}

func TestDiscardingIsSafeToRepeat(t *testing.T) {
	store := storeIn(t)
	transfer, path, err := store.CreateEmpty("shop.sql")
	if err != nil {
		t.Fatalf("CreateEmpty: %v", err)
	}

	if err := store.Discard(transfer.Token); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the file is still there")
	}
	// A caller cleaning up after a failure discards a token it may already
	// have discarded. That is not an error.
	if err := store.Discard(transfer.Token); err != nil {
		t.Fatalf("discarding twice: %v", err)
	}
	if _, err := store.PathFor(transfer.Token); !errors.Is(err, ErrNoTransfer) {
		t.Fatalf("a discarded token still resolves: %v", err)
	}
}

func TestTwoTransfersDoNotShareAFile(t *testing.T) {
	store := storeIn(t)

	first, firstPath, err := store.CreateEmpty("a.sql")
	if err != nil {
		t.Fatalf("CreateEmpty: %v", err)
	}
	second, secondPath, err := store.CreateEmpty("b.sql")
	if err != nil {
		t.Fatalf("CreateEmpty: %v", err)
	}

	if first.Token == second.Token || firstPath == secondPath {
		t.Fatal("two transfers were given the same token or file")
	}

	// Discarding one must not take the other with it.
	if err := store.Discard(first.Token); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, err := store.PathFor(second.Token); err != nil {
		t.Fatalf("the other transfer went too: %v", err)
	}
}

func TestReadingPastTheEndIsNotAnError(t *testing.T) {
	store := storeIn(t)
	transfer, path, err := store.CreateEmpty("shop.sql")
	if err != nil {
		t.Fatalf("CreateEmpty: %v", err)
	}
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The API reads in a loop until EOF. A final read that lands exactly on the
	// end must say so rather than fail, or every download would end in an
	// error after sending the whole file.
	data, eof, err := store.Read(transfer.Token, 3, 16)
	if err != nil {
		t.Fatalf("reading at the end: %v", err)
	}
	if len(data) != 0 || !eof {
		t.Fatalf("read %d bytes, eof=%v", len(data), eof)
	}
}
