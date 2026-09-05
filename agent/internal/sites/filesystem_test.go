package sites

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// These tests are about who owns a file, so they need to be able to change
// that. Everything here skips unless it is running as root, which it is in the
// container the suite runs in and is not on a developer's laptop.
func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("ownership cannot be changed without root")
	}
}

// Two uids that certainly do not exist on the host. The bug being guarded
// against is about numbers, not accounts: a uid in a file is a number, and
// nothing checks whether an account still stands behind it.
const (
	uidFirstSite  = 61001
	uidSecondSite = 61002
)

func newTestProvisioner(t *testing.T) (*Provisioner, string) {
	t.Helper()
	root := t.TempDir()
	// "root" as the web group: the group only has to resolve, and every
	// distribution disagrees about whether nginx or www-data exists.
	p, err := NewProvisioner(root, "root")
	if err != nil {
		t.Fatalf("NewProvisioner: %v", err)
	}
	if !p.HasWebGroup() {
		t.Fatal("the web group did not resolve, so provisioning would refuse")
	}
	return p, root
}

func layoutFor(t *testing.T, p *Provisioner, root, domain string) Layout {
	t.Helper()
	layout, err := p.LayoutFor(filepath.Join(root, domain, ContentDir))
	if err != nil {
		t.Fatalf("LayoutFor: %v", err)
	}
	return layout
}

func ownerUID(t *testing.T, path string) int {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("no ownership information for %s", path)
	}
	return int(stat.Uid)
}

// TestProvisionIsIdempotentForItsOwnAccount guards the property the refusal
// below could easily have broken. A job that failed halfway and is retried
// must converge, not fail on its second attempt.
func TestProvisionIsIdempotentForItsOwnAccount(t *testing.T) {
	requireRoot(t)
	p, root := newTestProvisioner(t)
	layout := layoutFor(t, p, root, "retry.test")

	if err := p.Provision(layout, uidFirstSite, uidFirstSite); err != nil {
		t.Fatalf("first provision: %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.Content, "index.html"),
		[]byte("hello"), 0o640); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err := os.Chown(filepath.Join(layout.Content, "index.html"),
		uidFirstSite, 0); err != nil {
		t.Fatalf("chown content: %v", err)
	}

	if err := p.Provision(layout, uidFirstSite, uidFirstSite); err != nil {
		t.Fatalf("provisioning the same account's directory again must succeed: %v", err)
	}
}

// TestProvisionRefusesAnotherAccountsDirectory is the second half of the fix:
// the panel must not adopt a directory that already holds somebody's files.
func TestProvisionRefusesAnotherAccountsDirectory(t *testing.T) {
	requireRoot(t)
	p, root := newTestProvisioner(t)
	layout := layoutFor(t, p, root, "occupied.test")

	if err := p.Provision(layout, uidFirstSite, uidFirstSite); err != nil {
		t.Fatalf("provision for the first account: %v", err)
	}
	secret := filepath.Join(layout.Content, "config.php")
	if err := os.WriteFile(secret, []byte("<?php $db_password = 'hunter2';"), 0o640); err != nil {
		t.Fatalf("write content: %v", err)
	}

	err := p.Provision(layout, uidSecondSite, uidSecondSite)
	if !errors.Is(err, ErrOccupied) {
		t.Fatalf("provisioning over another account's files must be refused, got %v", err)
	}

	// And the refusal must not have half-done the job on its way out.
	if got := ownerUID(t, layout.Content); got != uidFirstSite {
		t.Fatalf("the refused directory changed hands anyway: uid %d", got)
	}
	if got := ownerUID(t, secret); got != 0 && got != uidFirstSite {
		t.Fatalf("the refused directory's content changed hands: uid %d", got)
	}
}

// TestProvisionAdoptsAnEmptyDirectory keeps the refusal narrow. An empty
// directory holds nothing that could belong to anyone, and refusing it would
// break the ordinary case of a site root created a moment before its content.
func TestProvisionAdoptsAnEmptyDirectory(t *testing.T) {
	requireRoot(t)
	p, root := newTestProvisioner(t)
	layout := layoutFor(t, p, root, "empty.test")

	if err := os.MkdirAll(layout.Content, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chown(layout.Content, uidFirstSite, 0); err != nil {
		t.Fatalf("chown: %v", err)
	}

	if err := p.Provision(layout, uidSecondSite, uidSecondSite); err != nil {
		t.Fatalf("an empty directory should be adopted, got %v", err)
	}
	if got := ownerUID(t, layout.Content); got != uidSecondSite {
		t.Fatalf("the empty directory was not handed over: uid %d", got)
	}
}

// TestNeutralizeStripsTheFormerOwner is the first half of the fix: after a
// delete that keeps the files, nothing in the tree may still carry the uid
// that is about to be freed.
func TestNeutralizeStripsTheFormerOwner(t *testing.T) {
	requireRoot(t)
	p, root := newTestProvisioner(t)
	layout := layoutFor(t, p, root, "retained.test")

	if err := p.Provision(layout, uidFirstSite, uidFirstSite); err != nil {
		t.Fatalf("provision: %v", err)
	}
	nested := filepath.Join(layout.Content, "uploads", "deep")
	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	file := filepath.Join(nested, "invoice.pdf")
	if err := os.WriteFile(file, []byte("kept"), 0o640); err != nil {
		t.Fatalf("write nested file: %v", err)
	}
	for _, path := range []string{nested, file, layout.Logs} {
		if err := os.Lchown(path, uidFirstSite, 0); err != nil {
			t.Fatalf("chown %s: %v", path, err)
		}
	}

	if err := p.Neutralize(layout); err != nil {
		t.Fatalf("Neutralize: %v", err)
	}

	// Nothing anywhere in the tree may still carry the freed uid.
	err := filepath.WalkDir(layout.Root, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if got := ownerUID(t, path); got != 0 {
			t.Errorf("%s is still owned by uid %d after neutralising", path, got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	// The files are kept. That is the whole reason for reassigning rather than
	// deleting, so it is asserted rather than assumed.
	content, err := os.ReadFile(file)
	if err != nil || string(content) != "kept" {
		t.Fatalf("the retained file did not survive: %q %v", content, err)
	}

	info, err := os.Stat(layout.Root)
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if perm := info.Mode().Perm(); perm != orphanMode {
		t.Fatalf("a retained tree should be closed to everyone but root, got %o", perm)
	}
}

// TestNeutralizeDoesNotFollowSymlinks is the one that would hurt most if it
// were wrong. The Agent runs as root and this walks a tree the customer
// controlled; a chown that followed a link would hand /etc/shadow to whatever
// the walk was reassigning.
func TestNeutralizeDoesNotFollowSymlinks(t *testing.T) {
	requireRoot(t)
	p, root := newTestProvisioner(t)
	layout := layoutFor(t, p, root, "symlink.test")

	if err := p.Provision(layout, uidFirstSite, uidFirstSite); err != nil {
		t.Fatalf("provision: %v", err)
	}

	// A file outside the site, standing in for anything on the host worth
	// owning. It is given a distinctive uid so a change is unmistakable.
	outside := filepath.Join(t.TempDir(), "shadow")
	if err := os.WriteFile(outside, []byte("root:!:1::::::"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Chown(outside, uidSecondSite, uidSecondSite); err != nil {
		t.Fatalf("chown outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(layout.Content, "escape")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := p.Neutralize(layout); err != nil {
		t.Fatalf("Neutralize: %v", err)
	}

	if got := ownerUID(t, outside); got != uidSecondSite {
		t.Fatalf("neutralising followed a symlink out of the site and took ownership "+
			"of %s (uid is now %d)", outside, got)
	}
}

func TestNeutralizeRefusesOutsideTheRoot(t *testing.T) {
	requireRoot(t)
	p, root := newTestProvisioner(t)

	// Resolve a real layout, then point it somewhere it has no business being.
	layout := layoutFor(t, p, root, "escape.test")
	layout.Root = "/etc"

	if err := p.Neutralize(layout); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("neutralising outside the site root must be refused, got %v", err)
	}

	layout.Root = root
	if err := p.Neutralize(layout); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("neutralising the site root itself must be refused, got %v", err)
	}
}

// TestDeleteThenCreateDoesNotHandOverTheDirectory is the reported bug, end to
// end and in one place.
//
// A site is provisioned, deleted with its files kept, and its uid is then
// handed to the next site created — which is what the system does on its own
// once an account is removed and the number goes back into the pool. Before
// the fix the new site silently inherited the old one's directory. Now the
// tree belongs to root, and provisioning into it is refused rather than
// adopted.
func TestDeleteThenCreateDoesNotHandOverTheDirectory(t *testing.T) {
	requireRoot(t)
	p, root := newTestProvisioner(t)

	// 1. The first customer's site, with something worth not losing in it.
	first := layoutFor(t, p, root, "first.test")
	if err := p.Provision(first, uidFirstSite, uidFirstSite); err != nil {
		t.Fatalf("provision the first site: %v", err)
	}
	secret := filepath.Join(first.Content, "config.php")
	if err := os.WriteFile(secret, []byte("<?php $db_password = 'hunter2';"), 0o640); err != nil {
		t.Fatalf("write the first site's content: %v", err)
	}
	if err := os.Chown(secret, uidFirstSite, 0); err != nil {
		t.Fatalf("chown the first site's content: %v", err)
	}

	// 2. The site is deleted with its files kept, which is the default.
	if err := p.Neutralize(first); err != nil {
		t.Fatalf("neutralise on delete: %v", err)
	}

	// 3. The account is removed and its uid goes back into the pool. Nothing
	//    in the tree may still be pointing at it — that is what makes step 4
	//    survivable at all.
	err := filepath.WalkDir(first.Root, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if got := ownerUID(t, path); got == uidFirstSite {
			t.Errorf("%s still carries the freed uid %d", path, uidFirstSite)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	// 4. A new site is created and the system hands it the recycled uid. It
	//    happens to want the same directory — a domain being reused, or a
	//    document root that collides.
	if err := p.Provision(first, uidFirstSite, uidFirstSite); !errors.Is(err, ErrOccupied) {
		t.Fatalf("a new site must not be provisioned into the retained directory, got %v", err)
	}

	// 5. And the previous customer's file is still there, still not theirs.
	content, err := os.ReadFile(secret)
	if err != nil || string(content) != "<?php $db_password = 'hunter2';" {
		t.Fatalf("the retained content was damaged: %q %v", content, err)
	}
	if got := ownerUID(t, secret); got != 0 {
		t.Fatalf("the retained content is owned by uid %d, not root", got)
	}
}
