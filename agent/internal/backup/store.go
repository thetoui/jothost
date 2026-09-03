package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jothost/panel/shared/validate"
)

// Store is somewhere archives are kept.
//
// Four operations, and no "list everything" beyond a prefix: retention is
// decided from the panel's own records, not from what happens to be in a
// bucket. A destination somebody also uses for something else must not have
// that something else pruned by this panel.
type Store interface {
	// Kind names the destination kind, for messages.
	Kind() string
	// Put writes source to key, replacing anything already there.
	Put(ctx context.Context, key, source string) error
	// Get copies key to destination, which must not exist.
	Get(ctx context.Context, key, destination string) error
	// Stat reports the object's size, or ErrNotFound.
	Stat(ctx context.Context, key string) (int64, error)
	// Delete removes the object. Deleting one that is not there succeeds:
	// retention runs repeatedly, and an object somebody already removed by
	// hand must not make it fail every time afterwards.
	Delete(ctx context.Context, key string) error
}

// storeFor builds the store for a destination, validating it first.
//
// Validation happens here rather than at each call site so there is exactly one
// place a destination is checked, and so a destination that cannot work is
// refused before anything has been written.
func (p *Provider) storeFor(dest Destination) (Store, error) {
	if err := validate.DestinationKind(dest.Kind); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}

	switch dest.Kind {
	case validate.DestinationLocal:
		return p.localStore(dest)
	case validate.DestinationS3:
		return p.s3Store(dest)
	case validate.DestinationSFTP:
		return p.sftpStore(dest)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedDestination, dest.Kind)
	}
}

// ---------------------------------------------------------------- local

// localStore writes archives to a directory on this host.
type localStore struct {
	root string
}

// localStore builds and checks a local destination.
func (p *Provider) localStore(dest Destination) (Store, error) {
	if err := validate.LocalBackupRoot(dest.Directory); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	if len(p.localRoots) == 0 {
		return nil, fmt.Errorf(
			"%w: this agent has no directories a local backup may be written to",
			ErrUnsupportedDestination)
	}

	cleaned := filepath.Clean(dest.Directory)
	// The directory is created if it is missing, because an operator naming a
	// new subdirectory of an allowed root should not have to go and make it —
	// but only after it has been checked to be inside one, so this cannot
	// create a directory anywhere it likes.
	if err := insideAny(cleaned, p.localRoots); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cleaned, 0o700); err != nil {
		return nil, fmt.Errorf("%w: %s could not be created: %s",
			ErrDestinationFailed, cleaned, err)
	}

	// Re-check after creating, following symlinks this time. A directory that
	// is a symlink out of the allowed root would otherwise pass the textual
	// check and write somewhere else entirely.
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	if err := insideAny(resolved, p.localRoots); err != nil {
		return nil, err
	}

	return &localStore{root: resolved}, nil
}

// insideAny reports whether path is within one of the allowed roots.
func insideAny(target string, roots []string) error {
	for _, root := range roots {
		cleaned := filepath.Clean(root)
		resolved, err := filepath.EvalSymlinks(cleaned)
		if err != nil {
			// A configured root that does not exist is not a reason to refuse
			// the others.
			resolved = cleaned
		}
		if target == resolved || strings.HasPrefix(target, resolved+string(os.PathSeparator)) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s is not one of the directories this agent may write backups to (%s)",
		ErrDestinationFailed, target, strings.Join(roots, ", "))
}

func (s *localStore) Kind() string { return validate.DestinationLocal }

// resolve turns a key into a path under the store's root.
func (s *localStore) resolve(key string) (string, error) {
	if err := validate.BackupKey(key); err != nil {
		return "", fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	target := filepath.Join(s.root, filepath.FromSlash(key))
	if !strings.HasPrefix(target, s.root+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %q escapes the destination", ErrDestinationFailed, key)
	}
	return target, nil
}

func (s *localStore) Put(ctx context.Context, key, source string) error {
	if err := contextDone(ctx); err != nil {
		return err
	}
	target, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}

	// Written to a neighbouring temporary name and renamed into place. A
	// reader — including this panel's own verify — must never see a partial
	// archive under the name of a finished one.
	staging := target + ".partial"
	if err := copyFile(source, staging); err != nil {
		_ = os.Remove(staging)
		return fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	if err := os.Rename(staging, target); err != nil {
		_ = os.Remove(staging)
		return fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	return syncDir(filepath.Dir(target))
}

func (s *localStore) Get(ctx context.Context, key, destination string) error {
	if err := contextDone(ctx); err != nil {
		return err
	}
	source, err := s.resolve(key)
	if err != nil {
		return err
	}
	if _, err := os.Stat(source); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	if err := copyFile(source, destination); err != nil {
		return fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	return nil
}

func (s *localStore) Stat(ctx context.Context, key string) (int64, error) {
	if err := contextDone(ctx); err != nil {
		return 0, err
	}
	target, err := s.resolve(key)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return 0, fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	return info.Size(), nil
}

func (s *localStore) Delete(ctx context.Context, key string) error {
	if err := contextDone(ctx); err != nil {
		return err
	}
	target, err := s.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	return nil
}

// copyFile writes source to destination, which must not exist.
//
// The copy is flushed to disk before it is reported as done. Without the sync,
// "the backup is on the destination" would mean "the kernel has agreed to write
// it eventually", and a host that lost power between the two would have a
// panel claiming a backup that is not there.
func copyFile(source, destination string) error {
	in, err := os.Open(source) //nolint:gosec // agent-owned paths
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// syncDir flushes a directory entry, so a rename survives a power loss.
func syncDir(dir string) error {
	handle, err := os.Open(dir) //nolint:gosec // agent-owned path
	if err != nil {
		return fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	defer func() { _ = handle.Close() }()
	if err := handle.Sync(); err != nil {
		// Not every filesystem supports syncing a directory; on those it is
		// not an error worth failing a completed backup for.
		if !errors.Is(err, os.ErrInvalid) {
			return nil
		}
	}
	return nil
}

// ------------------------------------------------------------------ keys

// Key builds the object key for a backup.
//
// The shape — type/subject/date/name — exists so somebody looking at a bucket
// with a browser can find last Tuesday's copy of one site without the panel.
// A backup nobody can find without the software that made it is a backup that
// fails on the day the software is what broke.
func Key(prefix, kind, subject string, at time.Time, id string) string {
	parts := []string{}
	if trimmed := strings.Trim(prefix, "/"); trimmed != "" {
		parts = append(parts, trimmed)
	}
	parts = append(parts, safeSegment(kind))
	if subject != "" {
		parts = append(parts, safeSegment(subject))
	}
	parts = append(parts, at.UTC().Format("2006-01-02"))
	parts = append(parts,
		fmt.Sprintf("%s-%s.tar.gz", at.UTC().Format("150405"), safeSegment(id)))
	return path.Join(parts...)
}

// safeSegment reduces a value to what validate.BackupKey accepts.
//
// Domains and database names are already narrow, but this is what a key is
// built from and a key becomes three different kinds of path. Reducing here
// rather than validating and failing means a site called something unusual
// still gets backed up.
func safeSegment(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_':
			builder.WriteRune(r)
		case r == '.':
			// A dot is allowed but never at the start, where it would make a
			// hidden file, and never doubled, where it would be a dot segment.
			if builder.Len() > 0 && !strings.HasSuffix(builder.String(), ".") {
				builder.WriteRune('.')
			}
		default:
			builder.WriteRune('-')
		}
	}
	trimmed := strings.Trim(builder.String(), ".-")
	if trimmed == "" {
		return "unnamed"
	}
	if len(trimmed) > 100 {
		trimmed = strings.Trim(trimmed[:100], ".-")
	}
	return trimmed
}

// sortedStrings returns a sorted copy, for stable output.
func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
