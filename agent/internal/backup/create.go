package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jothost/panel/shared/validate"
)

// Request asks for a backup.
//
// The panel decides *what* is in it — which sites, which databases — because the
// panel is what knows the relationships. The Agent decides how it is written and
// where it goes, because that is the part that needs root.
type Request struct {
	Type    string `json:"type"`
	Key     string `json:"key"`
	Subject string `json:"subject"`

	Sites     []SiteSpec     `json:"sites"`
	Databases []DatabaseSpec `json:"databases"`

	Destination Destination `json:"destination"`
}

// Report is the progress callback the job runner supplies.
type Report func(percent int, message string)

func (r Report) at(percent int, message string) {
	if r != nil {
		r(percent, message)
	}
}

// Create takes a backup and reads it back.
//
// The order is the phase's whole argument: stage, upload, *read back*, and only
// then report success. A backup that has not been read back is not a backup,
// and the failures that make that true — a destination pointing at a directory
// that no longer exists, an upload that returned 200 and wrote nothing, a disk
// that dropped its last block — all look like success until somebody reads.
func (p *Provider) Create(ctx context.Context, req Request, report Report) (Result, error) {
	started := p.now()

	if !p.Available() {
		return Result{}, ErrUnavailable
	}
	if err := validate.BackupType(req.Type); err != nil {
		return Result{}, err
	}
	if err := validate.BackupKey(req.Key); err != nil {
		return Result{}, err
	}

	store, err := p.storeFor(req.Destination)
	if err != nil {
		return Result{}, err
	}

	staging, err := p.staging("archive")
	if err != nil {
		return Result{}, err
	}
	// The staging copy is always removed, including on the paths that fail:
	// it is a complete copy of somebody's website sitting in a directory that
	// is not the destination, and leaving one behind per failed backup fills
	// the host's disk with copies nothing knows about.
	defer func() { _ = os.Remove(staging) }()

	report.at(5, "Preparing the archive")

	manifest, size, checksum, err := p.writeArchive(ctx, req, staging, report)
	if err != nil {
		return Result{}, err
	}

	report.at(70, fmt.Sprintf("Sending %s to %s", humanBytes(size), store.Kind()))
	if err := store.Put(ctx, req.Key, staging); err != nil {
		return Result{}, err
	}

	report.at(85, "Reading the backup back to check it")
	verified, detail := p.readBack(ctx, store, req.Key, size, checksum)

	result := Result{
		Key:          req.Key,
		Size:         size,
		Checksum:     checksum,
		Verified:     verified,
		VerifyDetail: detail,
		Manifest:     manifest,
		DurationMS:   p.now().Sub(started).Milliseconds(),
	}

	if !verified {
		// The bytes were written and could not be confirmed. That is a failed
		// backup, not a qualified success: reporting it as done is how a panel
		// ends up with a year of archives nobody can restore.
		return result, fmt.Errorf("%w: %s", ErrCorrupt, detail)
	}

	report.at(100, "Backup complete and verified")
	return result, nil
}

// readBack downloads the archive from where it was stored and checks it.
func (p *Provider) readBack(ctx context.Context, store Store, key string,
	size int64, checksum string,
) (bool, string) {
	remoteSize, err := store.Stat(ctx, key)
	if err != nil {
		return false, err.Error()
	}
	if remoteSize >= 0 && remoteSize != size {
		return false, fmt.Sprintf(
			"the destination holds %d bytes and the archive was %d", remoteSize, size)
	}

	downloaded, err := p.staging("verify")
	if err != nil {
		return false, err.Error()
	}
	defer func() { _ = os.Remove(downloaded) }()

	if err := store.Get(ctx, key, downloaded); err != nil {
		return false, err.Error()
	}

	actual, actualSize, err := digestFile(downloaded)
	if err != nil {
		return false, err.Error()
	}
	if actualSize != size {
		return false, fmt.Sprintf(
			"the copy read back is %d bytes and the archive was %d", actualSize, size)
	}
	if actual != checksum {
		return false, "the copy read back does not match the archive's checksum"
	}

	// Reading the manifest proves the archive decompresses and its last member
	// is intact — which a digest over the whole file already implies, but this
	// is also what catches an archive that is byte-identical to something that
	// was never a valid archive.
	if _, err := readManifest(downloaded); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// writeArchive builds the archive and returns its manifest, size and digest.
func (p *Provider) writeArchive(ctx context.Context, req Request, staging string,
	report Report,
) (Manifest, int64, string, error) {
	archive, err := newWriter(staging)
	if err != nil {
		return Manifest{}, 0, "", err
	}

	manifest := Manifest{
		Version:      ManifestVersion,
		Type:         req.Type,
		Subject:      req.Subject,
		CreatedAt:    p.now().UTC(),
		Hostname:     p.hostname,
		AgentVersion: agentVersion(),
		Sites:        []SiteEntry{},
		Databases:    []DatabaseMember{},
		Files:        []Member{},
		Skipped:      []string{},
	}

	// Databases are dumped into a directory of their own first, then added to
	// the archive. Dumping straight into the tar stream is not possible: the
	// tar header carries the member's length and a dump's length is not known
	// until it has been written.
	dumpDir, err := p.stagingDir("dumps")
	if err != nil {
		archive.abandon()
		return Manifest{}, 0, "", err
	}
	defer func() { _ = os.RemoveAll(dumpDir) }()

	for index, site := range req.Sites {
		if err := contextDone(ctx); err != nil {
			archive.abandon()
			return Manifest{}, 0, "", err
		}
		report.at(10+index*40/max(1, len(req.Sites)),
			fmt.Sprintf("Archiving the files of %s", site.Domain))

		entry, members, err := p.addSite(archive, site)
		if err != nil {
			archive.abandon()
			return Manifest{}, 0, "", err
		}
		manifest.Sites = append(manifest.Sites, entry)
		manifest.Files = append(manifest.Files, members...)
		manifest.FileCount += entry.FileCount
		manifest.FileBytes += entry.Bytes
	}

	for index, spec := range req.Databases {
		if err := contextDone(ctx); err != nil {
			archive.abandon()
			return Manifest{}, 0, "", err
		}
		report.at(50+index*15/max(1, len(req.Databases)),
			fmt.Sprintf("Dumping %s", spec.Name))

		member, skipped, err := p.addDatabase(ctx, archive, dumpDir, spec)
		if err != nil {
			archive.abandon()
			return Manifest{}, 0, "", err
		}
		if skipped != "" {
			manifest.Skipped = append(manifest.Skipped, skipped)
			continue
		}
		manifest.Databases = append(manifest.Databases, member)
	}

	if len(manifest.Sites) == 0 && len(manifest.Databases) == 0 {
		archive.abandon()
		detail := ""
		if len(manifest.Skipped) > 0 {
			detail = ": " + strings.Join(manifest.Skipped, "; ")
		}
		return Manifest{}, 0, "", fmt.Errorf("%w%s", ErrNothingToBackUp, detail)
	}

	sort.Slice(manifest.Databases, func(i, j int) bool {
		return manifest.Databases[i].Path < manifest.Databases[j].Path
	})
	manifest.Skipped = sortedStrings(manifest.Skipped)

	if err := archive.addManifest(manifest); err != nil {
		archive.abandon()
		return Manifest{}, 0, "", err
	}

	size, checksum, err := archive.close()
	if err != nil {
		_ = os.Remove(staging)
		return Manifest{}, 0, "", err
	}
	if size > MaxArchiveBytes {
		return Manifest{}, 0, "", fmt.Errorf("%w: the archive is %d bytes", ErrTooLarge, size)
	}
	return manifest, size, checksum, nil
}

// addSite archives one website's document root.
func (p *Provider) addSite(archive *writer, site SiteSpec) (SiteEntry, []Member, error) {
	if site.Domain == "" {
		return SiteEntry{}, nil, fmt.Errorf("%w: a site needs a domain", ErrNothingToBackUp)
	}
	if err := validate.Domain(site.Domain); err != nil {
		return SiteEntry{}, nil, fmt.Errorf("%w: %s", ErrNothingToBackUp, err)
	}

	root, err := p.resolveDocumentRoot(site.DocumentRoot)
	if err != nil {
		return SiteEntry{}, nil, err
	}

	prefix := path.Join(SitesPrefix, safeSegment(site.Domain))
	members, bytes, err := archive.walkTree(root, prefix)
	if err != nil {
		return SiteEntry{}, nil, err
	}

	files := 0
	for _, member := range members {
		if member.Checksum != "" {
			files++
		}
	}

	return SiteEntry{
		Domain:       site.Domain,
		DocumentRoot: root,
		SystemUser:   site.SystemUser,
		Prefix:       prefix,
		FileCount:    files,
		Bytes:        bytes,
	}, members, nil
}

// addDatabase dumps one database and adds it to the archive.
//
// A database the host cannot dump is *skipped and named* rather than failing
// the whole backup. A website backup that failed entirely because one of three
// databases uses an engine whose tools are missing would leave the operator
// with nothing; skipping leaves them with the files and a manifest that says
// plainly what is not in it.
//
// A database that exists and cannot be read is a different matter and does
// fail: that is a dump the host should have been able to take.
func (p *Provider) addDatabase(ctx context.Context, archive *writer, dumpDir string,
	spec DatabaseSpec,
) (DatabaseMember, string, error) {
	if err := validate.DatabaseEngine(spec.Engine); err != nil {
		return DatabaseMember{}, "", fmt.Errorf("%w: %s", ErrNothingToBackUp, err)
	}
	if err := validate.DatabaseName(spec.Name); err != nil {
		return DatabaseMember{}, "", fmt.Errorf("%w: %s", ErrNothingToBackUp, err)
	}
	if p.databases == nil {
		return DatabaseMember{}, fmt.Sprintf(
			"%s (%s): this host manages no databases", spec.Name, spec.Engine), nil
	}

	dumper, err := p.databases.DumperFor(spec.Engine)
	if err != nil {
		return DatabaseMember{}, fmt.Sprintf("%s (%s): %s", spec.Name, spec.Engine, err), nil
	}

	local := path.Join(dumpDir, safeSegment(spec.Engine)+"-"+safeSegment(spec.Name)+".sql")
	if err := dumper.Dump(ctx, spec.Name, local); err != nil {
		return DatabaseMember{}, "", fmt.Errorf("%w: %s", ErrNothingToBackUp, err)
	}
	defer func() { _ = os.Remove(local) }()

	info, err := os.Stat(local)
	if err != nil {
		return DatabaseMember{}, "", fmt.Errorf("%w: %s", ErrNothingToBackUp, err)
	}

	name := path.Join(DatabasesPrefix, safeSegment(spec.Engine), safeSegment(spec.Name)+".sql")
	member, err := archive.addFile(name, local, info)
	if err != nil {
		return DatabaseMember{}, "", err
	}

	return DatabaseMember{
		Engine:   spec.Engine,
		Name:     spec.Name,
		Path:     member.Path,
		Size:     member.Size,
		Checksum: member.Checksum,
	}, "", nil
}

// Delete removes one archive from its destination.
//
// It is used by retention and by an operator deleting a backup by hand. It does
// not check that the object is there first: a destination somebody has already
// tidied by hand must not make retention fail on every run afterwards.
func (p *Provider) Delete(ctx context.Context, key string, dest Destination) error {
	if err := validate.BackupKey(key); err != nil {
		return err
	}
	store, err := p.storeFor(dest)
	if err != nil {
		return err
	}
	return store.Delete(ctx, key)
}

// CheckDestination writes a small object, reads it back, and removes it.
//
// This is what turns "the operator typed a bucket name" into "the panel has
// written to that bucket and read the same bytes out". Doing it at the moment
// the destination is configured is the only time anybody is watching; the
// alternative is finding out at the first scheduled run, in the middle of the
// night, from a job that failed.
func (p *Provider) CheckDestination(ctx context.Context, dest Destination) error {
	if !p.Available() {
		return ErrUnavailable
	}
	store, err := p.storeFor(dest)
	if err != nil {
		return err
	}

	staging, err := p.staging("probe")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(staging) }()

	// The content includes the time, so a destination that returns a cached or
	// stale object rather than what was just written is caught.
	content := fmt.Sprintf("jothost destination check %s\n", p.now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(staging, []byte(content), 0o600); err != nil {
		return fmt.Errorf("%w: %s", ErrUnavailable, err)
	}
	expected, _, err := digestFile(staging)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrUnavailable, err)
	}

	key := Key("", "checks", "destination", p.now(), "probe")
	if err := store.Put(ctx, key, staging); err != nil {
		return err
	}
	// Removed whatever happens next: a probe object left in somebody's bucket
	// after every check is litter this panel produced.
	defer func() {
		if err := store.Delete(context.WithoutCancel(ctx), key); err != nil {
			p.log.Warn("could not remove the destination check object",
				"kind", store.Kind(), "error", err.Error())
		}
	}()

	readBack, err := p.staging("probe-read")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(readBack) }()

	if err := store.Get(ctx, key, readBack); err != nil {
		return err
	}
	actual, _, err := digestFile(readBack)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	if actual != expected {
		return fmt.Errorf(
			"%w: the destination accepted a test object and returned different bytes",
			ErrDestinationFailed)
	}
	return nil
}

// humanBytes renders a size for a progress message.
func humanBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	for _, suffix := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PiB", value/unit)
}

// max returns the larger of two ints. Go 1.23 has a builtin; this file keeps
// its own so the meaning is obvious at the call sites that divide by it.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// errNotFound lets callers distinguish a missing object without importing the
// destination packages.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
