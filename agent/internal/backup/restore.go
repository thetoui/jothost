package backup

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/jothost/panel/agent/internal/fsperm"
	"github.com/jothost/panel/shared/validate"
)

// RestoreRequest asks for a backup to be put back.
//
// The panel says where each thing goes, because a restore onto a new host is
// the case this exists for and the paths there are not the paths the archive
// records. A site in the archive that the request does not name is skipped and
// reported rather than restored to wherever it came from: writing to a path
// nobody asked about is how a restore of one site overwrites another.
type RestoreRequest struct {
	Key      string `json:"key"`
	Checksum string `json:"checksum"`

	Sites     []SiteSpec     `json:"sites"`
	Databases []DatabaseSpec `json:"databases"`

	Destination Destination `json:"destination"`

	// KeepPrevious leaves the directory that was moved aside in place after a
	// successful restore, instead of deleting it.
	//
	// It defaults to false, and false is the right default: the copy is a
	// complete second copy of the site on the same disk, and a panel that left
	// one behind after every restore would fill the host. It is offered
	// because the first thing anybody wants after a restore that turns out to
	// be the wrong backup is what was there before.
	KeepPrevious bool `json:"keep_previous"`
}

// Restore puts a backup back.
//
// Verified first, in full, before anything on the host is touched. Two passes
// over a downloaded file is a cheap price for never half-restoring an archive
// that turns out to be truncated: a half-restored site is worse than an
// untouched broken one, because it looks repaired.
//
// The existing document root is moved aside rather than written over, and it is
// put back if the restore fails partway. That is what makes this recoverable
// from the one failure that matters — the host filling up in the middle of
// unpacking somebody's site.
func (p *Provider) Restore(ctx context.Context, req RestoreRequest, report Report) (
	RestoreResult, error,
) {
	started := p.now()

	if !p.Available() {
		return RestoreResult{}, ErrUnavailable
	}
	if err := validate.BackupKey(req.Key); err != nil {
		return RestoreResult{}, err
	}
	if req.Checksum != "" {
		if err := validate.Checksum(req.Checksum); err != nil {
			return RestoreResult{}, err
		}
	}

	store, err := p.storeFor(req.Destination)
	if err != nil {
		return RestoreResult{}, err
	}

	report.at(5, "Downloading the backup")

	downloaded, err := p.staging("restore")
	if err != nil {
		return RestoreResult{}, err
	}
	defer func() { _ = os.Remove(downloaded) }()

	if err := store.Get(ctx, req.Key, downloaded); err != nil {
		return RestoreResult{}, err
	}

	report.at(20, "Checking the backup before changing anything")

	// A sealed archive is the panel's own database, and it is not restored
	// through the panel: the panel would be replacing the database it is
	// running on. The host's recovery command does it, with the panel stopped.
	sealed, err := archiveIsSealed(downloaded)
	if err != nil {
		return RestoreResult{}, err
	}
	if sealed {
		return RestoreResult{}, ErrPanelRestoreOnHost
	}

	actual, _, err := digestFile(downloaded)
	if err != nil {
		return RestoreResult{}, err
	}
	if req.Checksum != "" && actual != req.Checksum {
		return RestoreResult{}, fmt.Errorf(
			"%w: the archive does not match the checksum recorded when it was written; nothing has been changed",
			ErrCorrupt)
	}

	manifest, _, detail, err := verifyMembers(downloaded)
	if err != nil {
		return RestoreResult{}, err
	}
	if detail != "" {
		return RestoreResult{}, fmt.Errorf("%w: %s; nothing has been changed", ErrCorrupt, detail)
	}
	if manifest.Version > ManifestVersion {
		return RestoreResult{}, fmt.Errorf(
			"%w: this backup was written by a newer version of the panel; nothing has been changed",
			ErrRestoreFailed)
	}

	result := RestoreResult{
		Key:             req.Key,
		SitesRestored:   []string{},
		DatabasesLoad:   []string{},
		Skipped:         []string{},
		PreviousMoved:   []string{},
		ManifestSubject: manifest.Subject,
	}

	report.at(35, "Putting the files back")
	if err := p.restoreSites(ctx, downloaded, manifest, req, &result, report); err != nil {
		return result, err
	}

	report.at(75, "Reloading the databases")
	if err := p.restoreDatabases(ctx, downloaded, manifest, req, &result); err != nil {
		return result, err
	}

	// Anything the archive holds that the request did not ask for is named, so
	// a restore that put back less than somebody expected says so rather than
	// looking complete.
	for _, site := range manifest.Sites {
		if !containsSite(req.Sites, site.Domain) {
			result.Skipped = append(result.Skipped,
				fmt.Sprintf("%s: the archive holds it and the restore did not ask for it",
					site.Domain))
		}
	}
	for _, db := range manifest.Databases {
		if !containsDatabase(req.Databases, db.Engine, db.Name) {
			result.Skipped = append(result.Skipped,
				fmt.Sprintf("%s (%s): the archive holds it and the restore did not ask for it",
					db.Name, db.Engine))
		}
	}
	result.Skipped = sortedStrings(result.Skipped)

	result.DurationMS = p.now().Sub(started).Milliseconds()
	report.at(100, "Restore complete")
	return result, nil
}

// restoreSites unpacks each requested site's files.
func (p *Provider) restoreSites(ctx context.Context, archivePath string, manifest Manifest,
	req RestoreRequest, result *RestoreResult, report Report,
) error {
	for _, spec := range req.Sites {
		entry, ok := findSite(manifest, spec.Domain)
		if !ok {
			result.Skipped = append(result.Skipped,
				fmt.Sprintf("%s: this backup does not contain it", spec.Domain))
			continue
		}
		if err := contextDone(ctx); err != nil {
			return err
		}

		target, err := p.resolveRestoreRoot(spec.DocumentRoot)
		if err != nil {
			return err
		}
		report.at(40, "Restoring the files of "+spec.Domain)

		files, bytes, moved, err := p.unpackSite(archivePath, entry.Prefix, target, req.KeepPrevious)
		if err != nil {
			return err
		}
		result.SitesRestored = append(result.SitesRestored, spec.Domain)
		result.FilesRestored += files
		result.BytesRestored += bytes
		if moved != "" {
			result.PreviousMoved = append(result.PreviousMoved, moved)
		}
	}
	return nil
}

// resolveRestoreRoot checks where files are about to be written.
//
// Unlike a backup's document root, this path need not exist yet: restoring a
// site that was deleted is the whole point. So it is checked textually against
// the site root, and its nearest existing ancestor is resolved through symlinks
// — which is what stops "/var/www/site" being restored into /etc when
// /var/www is a link.
func (p *Provider) resolveRestoreRoot(root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("%w: a document root is required", ErrRestoreFailed)
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("%w: %q is not an absolute path", ErrRestoreFailed, root)
	}
	if p.siteRoot == "" {
		return "", fmt.Errorf("%w: this agent has no site root configured", ErrUnavailable)
	}

	cleaned := filepath.Clean(root)
	siteRoot, err := filepath.EvalSymlinks(filepath.Clean(p.siteRoot))
	if err != nil {
		return "", fmt.Errorf("%w: the site root could not be resolved: %s", ErrUnavailable, err)
	}

	// Walk up to the nearest ancestor that exists, resolve that, and rebuild.
	existing := cleaned
	var missing []string
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", fmt.Errorf("%w: %s has no existing parent", ErrRestoreFailed, cleaned)
		}
		missing = append([]string{filepath.Base(existing)}, missing...)
		existing = parent
	}

	resolvedExisting, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrRestoreFailed, err)
	}
	target := filepath.Join(append([]string{resolvedExisting}, missing...)...)

	if target != siteRoot && !strings.HasPrefix(target, siteRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %s is not inside %s", ErrRestoreFailed, cleaned, siteRoot)
	}
	if target == siteRoot {
		return "", fmt.Errorf(
			"%w: a site cannot be restored over the whole site root", ErrRestoreFailed)
	}
	return target, nil
}

// unpackSite extracts one site's files, moving what is there aside first.
func (p *Provider) unpackSite(archivePath, prefix, target string, keepPrevious bool) (
	int, int64, string, error,
) {
	moved := ""
	if _, err := os.Lstat(target); err == nil {
		aside := fmt.Sprintf("%s.before-restore-%s", target, p.now().UTC().Format("20060102-150405"))
		if err := os.Rename(target, aside); err != nil {
			return 0, 0, "", fmt.Errorf(
				"%w: could not move the existing files aside: %s", ErrRestoreFailed, err)
		}
		moved = aside
	} else if !os.IsNotExist(err) {
		return 0, 0, "", fmt.Errorf("%w: %s", ErrRestoreFailed, err)
	}

	files, bytes, err := extractPrefix(archivePath, prefix, target)
	if err != nil {
		// Put the original back. This is the case the move exists for: a host
		// that filled up halfway through unpacking would otherwise be left
		// with a site that is neither the old one nor the new one.
		_ = os.RemoveAll(target)
		if moved != "" {
			if restoreErr := os.Rename(moved, target); restoreErr != nil {
				return 0, 0, moved, fmt.Errorf(
					"%w: %s — and the previous files could not be put back; they are at %s",
					ErrRestoreFailed, err, moved)
			}
		}
		return 0, 0, "", err
	}

	if moved != "" && !keepPrevious {
		if err := os.RemoveAll(moved); err != nil {
			// The restore worked. Failing it now because the old copy could
			// not be tidied would be reporting a success as a failure.
			p.log.Warn("could not remove the files kept from before the restore",
				"path", moved, "error", err.Error())
			return files, bytes, moved, nil
		}
		moved = ""
	}
	return files, bytes, moved, nil
}

// extractPrefix unpacks every member under prefix into target.
func extractPrefix(archivePath, prefix, target string) (int, int64, error) {
	archive, err := openArchive(archivePath)
	if err != nil {
		return 0, 0, err
	}
	defer archive.close()

	if err := fsperm.MkdirAll(target, restoredDirMode); err != nil {
		return 0, 0, fmt.Errorf("%w: %s", ErrRestoreFailed, err)
	}

	files := 0
	var bytes int64
	// Directory modes are applied after everything is written: a directory
	// restored as 0500 partway through would stop its own contents being
	// unpacked into it.
	type pending struct {
		path string
		mode os.FileMode
	}
	var dirs []pending

	for {
		header, err := archive.tar.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return files, bytes, fmt.Errorf("%w: %s", ErrArchiveMalformed, err)
		}

		name := path.Clean(header.Name)
		if name != prefix && !strings.HasPrefix(name, prefix+"/") {
			continue
		}
		relative := strings.TrimPrefix(strings.TrimPrefix(name, prefix), "/")
		if relative == "" {
			continue
		}

		destination, err := safeMemberPath(target, relative)
		if err != nil {
			return files, bytes, err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := fsperm.MkdirAll(destination, restoredDirMode); err != nil {
				return files, bytes, fmt.Errorf("%w: %s", ErrRestoreFailed, err)
			}
			dirs = append(dirs, pending{path: destination, mode: header.FileInfo().Mode().Perm()})
		case tar.TypeSymlink:
			if err := safeLinkTarget(target, relative, header.Linkname); err != nil {
				return files, bytes, err
			}
			if err := fsperm.MkdirAll(filepath.Dir(destination), restoredDirMode); err != nil {
				return files, bytes, fmt.Errorf("%w: %s", ErrRestoreFailed, err)
			}
			if err := os.Symlink(header.Linkname, destination); err != nil {
				return files, bytes, fmt.Errorf("%w: %s", ErrRestoreFailed, err)
			}
		case tar.TypeReg:
			if header.Size > MaxMemberBytes {
				return files, bytes, fmt.Errorf("%w: %s is %d bytes",
					ErrTooLarge, name, header.Size)
			}
			if err := fsperm.MkdirAll(filepath.Dir(destination), restoredDirMode); err != nil {
				return files, bytes, fmt.Errorf("%w: %s", ErrRestoreFailed, err)
			}
			written, err := writeMember(archive.tar, destination,
				header.FileInfo().Mode().Perm(), header.Size)
			if err != nil {
				return files, bytes, err
			}
			files++
			bytes += written
		default:
			// A device, socket or fifo. It was never archived by this Agent,
			// so one here came from somewhere else and is refused rather than
			// silently dropped.
			return files, bytes, fmt.Errorf(
				"%w: %s is an entry type this panel does not restore", ErrArchiveMalformed, name)
		}
	}

	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i].path, dirs[i].mode); err != nil {
			return files, bytes, fmt.Errorf("%w: %s", ErrRestoreFailed, err)
		}
	}
	return files, bytes, nil
}

// restoredDirMode is what a directory the archive does not describe is
// created with: the site's own directory convention, owner and web server
// group. Directories the archive does describe are set to their recorded mode
// once extraction finishes.
const restoredDirMode os.FileMode = 0o750

// writeMember writes one file, refusing to follow anything already there.
//
// O_EXCL is what makes that true: if a symlink was planted at this path — by an
// earlier member of the same archive — creating the file fails rather than
// writing through it.
func writeMember(source io.Reader, destination string, mode os.FileMode, size int64) (int64, error) {
	if mode == 0 {
		mode = 0o600
	}
	handle, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return 0, fmt.Errorf("%w: %s", ErrRestoreFailed, err)
	}
	// The archive recorded this file's mode, and a restore puts it back. The
	// umask would not: a site's 0644 files came back 0600, and the restored
	// site answered 403 to every visitor. O_EXCL means this is always a file
	// this call created.
	if err := fsperm.SetCreated(handle, mode); err != nil {
		_ = handle.Close()
		return 0, fmt.Errorf("%w: %s", ErrRestoreFailed, err)
	}
	written, err := io.Copy(handle, io.LimitReader(source, size))
	if err != nil {
		_ = handle.Close()
		return 0, fmt.Errorf("%w: %s", ErrRestoreFailed, err)
	}
	if err := handle.Close(); err != nil {
		return 0, fmt.Errorf("%w: %s", ErrRestoreFailed, err)
	}
	return written, nil
}

// restoreDatabases replays each requested dump.
func (p *Provider) restoreDatabases(ctx context.Context, archivePath string, manifest Manifest,
	req RestoreRequest, result *RestoreResult,
) error {
	if len(req.Databases) == 0 {
		return nil
	}
	if p.databases == nil {
		for _, spec := range req.Databases {
			result.Skipped = append(result.Skipped,
				fmt.Sprintf("%s (%s): this host manages no databases", spec.Name, spec.Engine))
		}
		return nil
	}

	dumpDir, err := p.stagingDir("reload")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dumpDir) }()

	for _, spec := range req.Databases {
		member, ok := findDatabase(manifest, spec.Engine, spec.Name)
		if !ok {
			result.Skipped = append(result.Skipped,
				fmt.Sprintf("%s (%s): this backup does not contain it", spec.Name, spec.Engine))
			continue
		}
		if err := contextDone(ctx); err != nil {
			return err
		}

		dumper, err := p.databases.DumperFor(spec.Engine)
		if err != nil {
			result.Skipped = append(result.Skipped,
				fmt.Sprintf("%s (%s): %s", spec.Name, spec.Engine, err))
			continue
		}

		local := filepath.Join(dumpDir, safeSegment(spec.Engine)+"-"+safeSegment(spec.Name)+".sql")
		if err := extractMember(archivePath, member.Path, local); err != nil {
			return err
		}
		if err := dumper.Reload(ctx, spec.Name, local); err != nil {
			return fmt.Errorf("%w: %s", ErrRestoreFailed, err)
		}
		_ = os.Remove(local)

		result.DatabasesLoad = append(result.DatabasesLoad, spec.Name)
	}
	return nil
}

// extractMember writes one named archive member to a local file.
func extractMember(archivePath, name, destination string) error {
	archive, err := openArchive(archivePath)
	if err != nil {
		return err
	}
	defer archive.close()

	for {
		header, err := archive.tar.Next()
		if err == io.EOF {
			return fmt.Errorf("%w: %s is not in the archive", ErrArchiveMalformed, name)
		}
		if err != nil {
			return fmt.Errorf("%w: %s", ErrArchiveMalformed, err)
		}
		if header.Name != name {
			continue
		}
		if header.Size > MaxMemberBytes {
			return fmt.Errorf("%w: %s is %d bytes", ErrTooLarge, name, header.Size)
		}
		if _, err := writeMember(archive.tar, destination, 0o600, header.Size); err != nil {
			return err
		}
		return nil
	}
}

// findSite looks a domain up in a manifest.
func findSite(manifest Manifest, domain string) (SiteEntry, bool) {
	for _, site := range manifest.Sites {
		if strings.EqualFold(site.Domain, domain) {
			return site, true
		}
	}
	return SiteEntry{}, false
}

// findDatabase looks a database up in a manifest.
func findDatabase(manifest Manifest, engine, name string) (DatabaseMember, bool) {
	for _, member := range manifest.Databases {
		if member.Engine == engine && member.Name == name {
			return member, true
		}
	}
	return DatabaseMember{}, false
}

func containsSite(specs []SiteSpec, domain string) bool {
	for _, spec := range specs {
		if strings.EqualFold(spec.Domain, domain) {
			return true
		}
	}
	return false
}

func containsDatabase(specs []DatabaseSpec, engine, name string) bool {
	for _, spec := range specs {
		if spec.Engine == engine && spec.Name == name {
			return true
		}
	}
	return false
}
