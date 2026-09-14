package backup

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jothost/panel/shared/validate"
)

// VerifyRequest asks whether a stored backup is still intact.
type VerifyRequest struct {
	Key         string      `json:"key"`
	Checksum    string      `json:"checksum"`
	Size        int64       `json:"size"`
	Destination Destination `json:"destination"`
	// SealingKey opens a sealed archive, which is verified by opening it. It
	// is a secret, sent only for a panel backup.
	SealingKey string `json:"sealing_key,omitempty"`
}

// Verify reads a backup from its destination and checks it.
//
// Three questions, in order, and the order matters because each is cheaper than
// the next and a failure of an earlier one makes the later ones meaningless:
//
//  1. Is it there, and is it the size it was?
//  2. Does the whole file still hash to what was recorded?
//  3. Does every member inside it hash to what the manifest says?
//
// The third is not redundant. The archive's digest catches a file that changed;
// the per-member digests catch an archive that was *written* wrong — a file
// that changed length while it was being read, a dump that was truncated by a
// full disk — because those produce a self-consistent archive of the wrong
// thing. Only the members' digests, which were taken from what was actually
// read, can say so.
func (p *Provider) Verify(ctx context.Context, req VerifyRequest) (VerifyResult, error) {
	if !p.Available() {
		return VerifyResult{}, ErrUnavailable
	}
	if err := validate.BackupKey(req.Key); err != nil {
		return VerifyResult{}, err
	}
	if req.Checksum != "" {
		if err := validate.Checksum(req.Checksum); err != nil {
			return VerifyResult{}, err
		}
	}

	store, err := p.storeFor(req.Destination)
	if err != nil {
		return VerifyResult{}, err
	}

	result := VerifyResult{Key: req.Key, Expected: req.Checksum}

	remoteSize, err := store.Stat(ctx, req.Key)
	if err != nil {
		if IsNotFound(err) {
			result.Detail = "the backup is not at its destination"
			return result, nil
		}
		return result, err
	}
	result.Size = remoteSize

	if req.Size > 0 && remoteSize >= 0 && remoteSize != req.Size {
		result.Detail = fmt.Sprintf(
			"the destination holds %d bytes and %d were recorded", remoteSize, req.Size)
		return result, nil
	}

	downloaded, err := p.staging("verify")
	if err != nil {
		return result, err
	}
	defer func() { _ = os.Remove(downloaded) }()

	if err := store.Get(ctx, req.Key, downloaded); err != nil {
		if IsNotFound(err) {
			result.Detail = "the backup is not at its destination"
			return result, nil
		}
		return result, err
	}

	actual, size, err := digestFile(downloaded)
	if err != nil {
		return result, err
	}
	result.Checksum = actual
	result.Bytes = size

	if req.Checksum != "" && actual != req.Checksum {
		result.Detail = "the archive does not match the checksum recorded when it was written"
		return result, nil
	}

	readable := downloaded
	sealed, err := archiveIsSealed(downloaded)
	if err != nil {
		return result, err
	}
	if sealed {
		// Reported as a failed verification rather than an error: "the key no
		// longer opens this" is precisely the finding a verify exists to
		// surface, and it has to reach the record, not be lost as a 500.
		key, keyErr := sealingKey(req.SealingKey)
		if keyErr != nil {
			result.Detail = "the archive is sealed and no key was given to open it"
			return result, nil
		}
		opened, err := p.staging("opened")
		if err != nil {
			return result, err
		}
		defer func() { _ = os.Remove(opened) }()
		if err := unsealFile(downloaded, opened, key); err != nil {
			result.Detail = "the sealed archive could not be opened with this panel's key: " + err.Error()
			return result, nil
		}
		readable = opened
	}

	manifest, members, detail, err := verifyMembers(readable)
	if err != nil {
		return result, err
	}
	result.Manifest = manifest
	result.Members = members
	if detail != "" {
		result.Detail = detail
		return result, nil
	}

	result.OK = true
	return result, nil
}

// verifyMembers walks an archive and checks each member against the manifest.
//
// The manifest is the archive's last member, so this cannot check as it goes:
// it digests every member on the way through, reads the manifest at the end,
// and compares. That is one pass and a map of digests rather than two passes,
// which matters on an archive larger than the host's memory — the map holds a
// digest per file, not the files.
func verifyMembers(source string) (Manifest, int, string, error) {
	archive, err := openArchive(source)
	if err != nil {
		return Manifest{}, 0, "", err
	}
	defer archive.close()

	seen := make(map[string]Member)
	var manifest Manifest
	haveManifest := false
	count := 0

	for {
		header, err := archive.tar.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Manifest{}, count, "", fmt.Errorf("%w: %s", ErrArchiveMalformed, err)
		}
		count++
		if count > MaxMembers {
			return Manifest{}, count, "", fmt.Errorf("%w: more than %d entries",
				ErrTooLarge, MaxMembers)
		}

		// A member whose name escapes fails the whole archive here, before a
		// restore has touched anything.
		//
		// The extractor would not write it — it only unpacks members under a
		// site's own prefix, and an escaping name cleans to something outside
		// that — so this is not what stops the write. What it stops is the
		// silence: an archive carrying "../../etc/shadow" would otherwise be
		// restored successfully with that member quietly dropped, and a restore
		// that reports success while ignoring part of what it was given is the
		// worst outcome available.
		if err := checkMemberName(header.Name); err != nil {
			return Manifest{}, count, "", err
		}

		if header.Name == ManifestName {
			if header.Size > MaxManifestBytes {
				return Manifest{}, count, "", fmt.Errorf("%w: the manifest is %d bytes",
					ErrTooLarge, header.Size)
			}
			encoded, err := io.ReadAll(io.LimitReader(archive.tar, MaxManifestBytes))
			if err != nil {
				return Manifest{}, count, "", fmt.Errorf("%w: %s", ErrArchiveMalformed, err)
			}
			if err := decodeManifest(encoded, &manifest); err != nil {
				return Manifest{}, count, "", err
			}
			haveManifest = true
			continue
		}

		switch header.Typeflag {
		case tar.TypeReg:
			if header.Size > MaxMemberBytes {
				return manifest, count, "", fmt.Errorf("%w: %s is %d bytes",
					ErrTooLarge, header.Name, header.Size)
			}
			sum := sha256.New()
			copied, err := io.Copy(sum, archive.tar)
			if err != nil {
				return manifest, count, "", fmt.Errorf("%w: %s", ErrArchiveMalformed, err)
			}
			seen[header.Name] = Member{
				Path:     header.Name,
				Size:     copied,
				Checksum: hex.EncodeToString(sum.Sum(nil)),
			}
		default:
			seen[header.Name] = Member{Path: header.Name, Link: header.Linkname}
		}
	}

	if !haveManifest {
		return manifest, count, "", fmt.Errorf("%w: it has no %s",
			ErrArchiveMalformed, ManifestName)
	}
	if manifest.Version > ManifestVersion {
		return manifest, count, fmt.Sprintf(
			"this backup was written by a newer version of the panel (manifest version %d)",
			manifest.Version), nil
	}

	var problems []string
	for _, declared := range manifest.Files {
		if declared.Checksum == "" {
			continue
		}
		actual, ok := seen[declared.Path]
		if !ok {
			problems = append(problems, declared.Path+" is missing")
			continue
		}
		if actual.Size != declared.Size {
			problems = append(problems, fmt.Sprintf(
				"%s is %d bytes and %d were recorded", declared.Path, actual.Size, declared.Size))
			continue
		}
		if actual.Checksum != declared.Checksum {
			problems = append(problems, declared.Path+" does not match its checksum")
		}
	}
	for _, declared := range manifest.Databases {
		actual, ok := seen[declared.Path]
		if !ok {
			problems = append(problems, declared.Path+" is missing")
			continue
		}
		if actual.Checksum != declared.Checksum {
			problems = append(problems, declared.Path+" does not match its checksum")
		}
	}

	if len(problems) > 0 {
		// Bounded: an archive with a hundred thousand bad members would
		// otherwise produce a failure message nothing can store or display.
		const maxProblems = 10
		shown := problems
		suffix := ""
		if len(problems) > maxProblems {
			shown = problems[:maxProblems]
			suffix = fmt.Sprintf(" (and %d more)", len(problems)-maxProblems)
		}
		return manifest, count, strings.Join(shown, "; ") + suffix, nil
	}

	return manifest, count, "", nil
}

// decodeManifest reads a manifest, refusing one this Agent cannot understand.
func decodeManifest(encoded []byte, into *Manifest) error {
	if err := json.Unmarshal(encoded, into); err != nil {
		return fmt.Errorf("%w: the manifest is not readable: %s", ErrArchiveMalformed, err)
	}
	if into.Version <= 0 {
		return fmt.Errorf("%w: the manifest has no version", ErrArchiveMalformed)
	}
	return nil
}

// checkMemberName refuses an archive entry this Agent would never have written.
func checkMemberName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: it contains an entry with no name", ErrArchiveMalformed)
	}
	if strings.ContainsAny(name, "\\\x00") {
		return fmt.Errorf("%w: %q contains a backslash or a null byte",
			ErrArchiveMalformed, name)
	}
	if strings.HasPrefix(name, "/") {
		return fmt.Errorf("%w: %q is an absolute path", ErrArchiveMalformed, name)
	}
	for _, segment := range strings.Split(strings.Trim(name, "/"), "/") {
		if segment == ".." {
			return fmt.Errorf("%w: %q escapes the archive", ErrArchiveMalformed, name)
		}
	}
	return nil
}
