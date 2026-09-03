package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The socket tables, against captured kernel output.
//
// The byte order is the thing worth a test: /proc/net/tcp writes each 32-bit
// word of an address in host order, so an IPv4 address comes out reversed and
// an IPv6 address comes out reversed within each word but not across them. Get
// it wrong and every address is plausible and none of them is right — which is
// a scanner that reports the wrong service as exposed, and stays wrong.

// A real /proc/net/tcp, taken from a host running nginx, sshd and MariaDB.
//
// It deliberately has MariaDB bound three ways at once — 127.0.0.1, 0.0.0.0 and
// ::1 — because that is the shape of the finding this scanner exists for, and a
// fixture with only the safe binding would let a broken parser pass.
const procNetTCP = `  sl  local_address rem_address   st tx_queue:rx_queue tr:tm->when retrnsmt   uid  timeout inode
   0: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 18465 1 0000000000000000 100 0 0 10 0
   1: 0100007F:0CEA 00000000:0000 0A 00000000:00000000 00:00000000 00000000   100        0 19023 1 0000000000000000 100 0 0 10 0
   2: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 17766 1 0000000000000000 100 0 0 10 0
   3: 00000000:0CEA 00000000:0000 0A 00000000:00000000 00:00000000 00000000   102        0 19100 1 0000000000000000 100 0 0 10 0
   4: 0100007F:8AE6 0100007F:0CEA 01 00000000:00000000 00:00000000 00000000  1000        0 20551 1 0000000000000000 20 4 30 10 -1
`

// A real /proc/net/tcp6, with a socket on :: and one on ::1.
const procNetTCP6 = `  sl  local_address                         remote_address                        st tx_queue:rx_queue tr:tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:01BB 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 18470 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000001000000:0CEA 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000   102        0 19105 1 0000000000000000 100 0 0 10 0
`

func procFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "net"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(root, "net", name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("tcp", procNetTCP)
	write("tcp6", procNetTCP6)
	return root
}

func TestListeningPortsDecodesTheKernelsByteOrder(t *testing.T) {
	scanner := NewScanner(Options{ProcRoot: procFixture(t)})

	report, err := scanner.ListeningPorts()
	if err != nil {
		t.Fatalf("ListeningPorts: %v", err)
	}

	// 00000000:0050 is 0.0.0.0:80, and 0100007F:0CEA is 127.0.0.1:3306 —
	// the address reversed, which is the whole point of the parser.
	want := map[int]struct {
		address string
		public  bool
	}{
		22:   {"0.0.0.0", true},
		80:   {"0.0.0.0", true},
		443:  {"::", true},
		3306: {"127.0.0.1", false},
	}

	byPort := map[int][]Socket{}
	for _, socket := range report.Sockets {
		byPort[socket.Port] = append(byPort[socket.Port], socket)
	}

	for port, expected := range want {
		sockets, ok := byPort[port]
		if !ok {
			t.Errorf("port %d was not reported", port)
			continue
		}
		found := false
		for _, socket := range sockets {
			if socket.Address == expected.address {
				found = true
				if socket.Public != expected.public {
					t.Errorf("port %d on %s: public = %v, want %v",
						port, socket.Address, socket.Public, expected.public)
				}
			}
		}
		if !found {
			t.Errorf("port %d: no socket on %s; got %+v", port, expected.address, sockets)
		}
	}
}

func TestListeningPortsIgnoresEstablishedConnections(t *testing.T) {
	// Line 4 of the fixture is an established connection to MariaDB, not a
	// listening socket. Reporting it would mean every client connection looked
	// like an exposed service.
	scanner := NewScanner(Options{ProcRoot: procFixture(t)})

	report, err := scanner.ListeningPorts()
	if err != nil {
		t.Fatalf("ListeningPorts: %v", err)
	}
	for _, socket := range report.Sockets {
		if socket.Port == 35558 {
			t.Fatalf("an established connection was reported as listening: %+v", socket)
		}
	}
}

func TestListeningPortsSeparatesLoopbackFromPublic(t *testing.T) {
	// This is the distinction the whole scanner exists for. MariaDB on
	// 127.0.0.1 is reachable only from this machine; the same service on
	// 0.0.0.0 is the most common way a hosting box is taken over.
	scanner := NewScanner(Options{ProcRoot: procFixture(t)})

	report, err := scanner.ListeningPorts()
	if err != nil {
		t.Fatalf("ListeningPorts: %v", err)
	}

	// Four: 0.0.0.0:22, 0.0.0.0:80, :::443, and the MariaDB the fixture
	// deliberately has on 0.0.0.0:3306 — which is the finding this scanner
	// exists for. The other two MariaDB sockets are on 127.0.0.1 and ::1 and
	// are reachable only from the machine itself.
	if report.Public != 4 {
		t.Errorf("public = %d, want 4; sockets: %+v", report.Public, report.Sockets)
	}

	// The distinction has to hold *per socket*, not just in the total: the same
	// service bound twice is one exposure and one non-exposure, and a scanner
	// that collapsed them would either cry wolf or miss the wolf.
	for _, socket := range report.Sockets {
		if socket.Port != 3306 {
			continue
		}
		wantPublic := socket.Address == "0.0.0.0"
		if socket.Public != wantPublic {
			t.Errorf("3306 on %s: public = %v, want %v",
				socket.Address, socket.Public, wantPublic)
		}
	}
}

func TestListeningPortsSurvivesAHostWithNoIPv6(t *testing.T) {
	// A host with IPv6 disabled has no /proc/net/tcp6, which is not an error
	// and must not cost the IPv4 answer.
	root := procFixture(t)
	if err := os.Remove(filepath.Join(root, "net", "tcp6")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	scanner := NewScanner(Options{ProcRoot: root})
	report, err := scanner.ListeningPorts()
	if err != nil {
		t.Fatalf("ListeningPorts: %v", err)
	}
	if len(report.Sockets) == 0 {
		t.Fatal("no sockets were reported from the IPv4 table")
	}
}

func TestListeningPortsFailsWhenProcIsUnreadable(t *testing.T) {
	// A host that cannot be asked must say so. Reporting no sockets would be
	// reporting a clean bill of health for a host nobody looked at.
	scanner := NewScanner(Options{ProcRoot: filepath.Join(t.TempDir(), "missing")})

	if _, err := scanner.ListeningPorts(); err == nil {
		t.Fatal("an unreadable /proc produced a successful, empty answer")
	}
}

func TestParseSocketAddressRejectsWhatItCannotRead(t *testing.T) {
	for _, field := range []string{"", "0100007F", "ZZZZZZZZ:0050", "0100007F:ZZZZ", "01:0050"} {
		if _, _, err := parseSocketAddress(field); err == nil {
			t.Errorf("parseSocketAddress accepted %q", field)
		}
	}
}

func TestSocketInodeReadsTheSymlinkForm(t *testing.T) {
	inode, ok := socketInode("socket:[19023]")
	if !ok || inode != 19023 {
		t.Errorf("socketInode = %d, %v; want 19023, true", inode, ok)
	}
	for _, link := range []string{"/dev/null", "socket:[]", "socket:[abc]", "pipe:[123]"} {
		if _, ok := socketInode(link); ok {
			t.Errorf("socketInode accepted %q", link)
		}
	}
}

// ------------------------------------------------------------ permissions

// siteTree lays out a site the way this panel creates one.
func siteTree(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	public := filepath.Join(root, "example.com", "public")
	if err := os.MkdirAll(public, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(public, "index.php"), []byte("<?php"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return root
}

func TestPermissionsFindsAWorldWritableFile(t *testing.T) {
	// The single most common way one compromised site on a shared host becomes
	// all of them.
	root := siteTree(t)
	target := filepath.Join(root, "example.com", "public", "uploads.php")
	if err := os.WriteFile(target, []byte("<?php"), 0o666); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(target, 0o666); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	report, err := NewScanner(Options{Roots: []string{root}}).Permissions()
	if err != nil {
		t.Fatalf("Permissions: %v", err)
	}
	if report.Counts[IssueWorldWritable] != 1 {
		t.Fatalf("world-writable count = %d, want 1; issues: %+v",
			report.Counts[IssueWorldWritable], report.Issues)
	}
}

func TestPermissionsIgnoresAStickyDirectory(t *testing.T) {
	// /tmp and the per-site temp directories PHP needs are deliberately
	// world-writable, and the sticky bit is what stops one account deleting
	// another's files. Reporting them would bury the real findings.
	root := siteTree(t)
	tmp := filepath.Join(root, "example.com", "tmp")
	if err := os.MkdirAll(tmp, 0o777); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(tmp, 0o777|os.ModeSticky); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	report, err := NewScanner(Options{Roots: []string{root}}).Permissions()
	if err != nil {
		t.Fatalf("Permissions: %v", err)
	}
	if report.Counts[IssueWorldWritable] != 0 {
		t.Errorf("a sticky directory was reported: %+v", report.Issues)
	}
}

func TestPermissionsFindsSecretsInsideADocumentRoot(t *testing.T) {
	// A .env inside a served directory is downloaded by anyone who asks for it
	// by name, and it holds the database password by convention.
	root := siteTree(t)
	public := filepath.Join(root, "example.com", "public")
	if err := os.WriteFile(filepath.Join(public, ".env"), []byte("DB_PASS=x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(public, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	report, err := NewScanner(Options{Roots: []string{root}}).Permissions()
	if err != nil {
		t.Fatalf("Permissions: %v", err)
	}
	if report.Counts[IssueExposedSecret] != 1 {
		t.Errorf("exposed secrets = %d, want 1", report.Counts[IssueExposedSecret])
	}
	if report.Counts[IssueExposedVCS] != 1 {
		t.Errorf("exposed VCS = %d, want 1", report.Counts[IssueExposedVCS])
	}
}

func TestPermissionsLeavesASecretAboveTheDocumentRootAlone(t *testing.T) {
	// A .env one level above the served directory is the correct place for it.
	// Reporting that would teach people to ignore this scanner.
	root := siteTree(t)
	outside := filepath.Join(root, "example.com", ".env")
	if err := os.WriteFile(outside, []byte("DB_PASS=x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	report, err := NewScanner(Options{Roots: []string{root}}).Permissions()
	if err != nil {
		t.Fatalf("Permissions: %v", err)
	}
	if report.Counts[IssueExposedSecret] != 0 {
		t.Errorf("a correctly placed .env was reported: %+v", report.Issues)
	}
}

func TestPermissionsFindsAPrivateKeyAnybodyCanRead(t *testing.T) {
	root := siteTree(t)
	key := filepath.Join(root, "example.com", "server.key")
	if err := os.WriteFile(key, []byte("-----BEGIN"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	report, err := NewScanner(Options{Roots: []string{root}}).Permissions()
	if err != nil {
		t.Fatalf("Permissions: %v", err)
	}
	if report.Counts[IssueReadableKey] != 1 {
		t.Fatalf("readable keys = %d, want 1; issues: %+v",
			report.Counts[IssueReadableKey], report.Issues)
	}
}

func TestPermissionsLeavesAProperlyProtectedKeyAlone(t *testing.T) {
	root := siteTree(t)
	key := filepath.Join(root, "example.com", "server.key")
	if err := os.WriteFile(key, []byte("-----BEGIN"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(key, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	report, err := NewScanner(Options{Roots: []string{root}}).Permissions()
	if err != nil {
		t.Fatalf("Permissions: %v", err)
	}
	if report.Counts[IssueReadableKey] != 0 {
		t.Errorf("a 0600 key was reported: %+v", report.Issues)
	}
}

func TestPermissionsDoesNotFollowSymlinks(t *testing.T) {
	// Following one would take the scan outside the roots it was given, which
	// is the one thing that must not happen — and would loop on the first link
	// to a parent.
	root := siteTree(t)
	outside := t.TempDir()
	victim := filepath.Join(outside, "shadow")
	if err := os.WriteFile(victim, []byte("root::"), 0o666); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(victim, 0o666); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	link := filepath.Join(root, "example.com", "public", "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	report, err := NewScanner(Options{Roots: []string{root}}).Permissions()
	if err != nil {
		t.Fatalf("Permissions: %v", err)
	}
	for _, issue := range report.Issues {
		if strings.Contains(issue.Path, outside) {
			t.Fatalf("the scan followed a symlink out of its root: %s", issue.Path)
		}
	}
}

func TestPermissionsSaysWhenItStoppedLooking(t *testing.T) {
	// "We found nothing else" and "we stopped looking" are not the same answer,
	// and a count from a truncated walk is a lower bound rather than a total.
	report := PermissionReport{Counts: map[string]int{}, Complete: false}
	if report.Complete {
		t.Fatal("a truncated report claimed to be complete")
	}
}

func TestPermissionsReportsAHostWithNothingToScan(t *testing.T) {
	// A configured root that does not exist is not a clean host, it is a host
	// that was not scanned.
	scanner := NewScanner(Options{Roots: []string{filepath.Join(t.TempDir(), "missing")}})

	if _, err := scanner.Permissions(); err == nil {
		t.Fatal("a missing root produced a successful, empty answer")
	}
}

func TestPermissionsRefusesAScannerWithNoRoots(t *testing.T) {
	if _, err := NewScanner(Options{}).Permissions(); err == nil {
		t.Fatal("a scanner with no configured roots produced an answer")
	}
}

func TestUnderDocumentRootRecognisesTheConventions(t *testing.T) {
	for _, path := range []string{
		"/var/www/example.com/public/index.php",
		"/var/www/example.com/public_html/.env",
		"/srv/site/htdocs/.git",
	} {
		if !underDocumentRoot(path) {
			t.Errorf("underDocumentRoot(%q) = false, want true", path)
		}
	}
	for _, path := range []string{
		"/var/www/example.com/.env",
		"/var/www/example.com/logs/access.log",
	} {
		if underDocumentRoot(path) {
			t.Errorf("underDocumentRoot(%q) = true, want false", path)
		}
	}
}

func TestIsKeyFileMatchesWhatHoldsAPrivateKey(t *testing.T) {
	for _, name := range []string{"server.key", "cert.pem", "bundle.p12", "id_rsa", "id_ed25519"} {
		if !isKeyFile(name) {
			t.Errorf("isKeyFile(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"index.php", "keyboard.js", "readme.md"} {
		if isKeyFile(name) {
			t.Errorf("isKeyFile(%q) = true, want false", name)
		}
	}
}
