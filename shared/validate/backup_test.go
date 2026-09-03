package validate

import (
	"errors"
	"strings"
	"testing"
)

func TestBackupKeyAcceptsWhatThePanelBuilds(t *testing.T) {
	// The shapes the panel actually produces. If one of these is refused, no
	// backup can be written at all.
	keys := []string{
		"website/example.com/2026-09-03/031500.tar.gz",
		"full/server/2026-01-01/000000.tar.gz",
		"database/my_app_db/2026-12-31/235959.tar.gz",
		"a",
	}
	for _, key := range keys {
		if err := BackupKey(key); err != nil {
			t.Errorf("BackupKey(%q) = %v, want nil", key, err)
		}
	}
}

func TestBackupKeyRefusesAnythingThatCouldEscape(t *testing.T) {
	// One key becomes three different things: a path under a local directory,
	// a remote path over SFTP, and a URL path against S3. Every entry here goes
	// wrong in at least one of the three.
	cases := map[string]string{
		"a parent segment":     "website/../../etc/shadow",
		"a leading slash":      "/etc/shadow",
		"a current segment":    "website/./x.tar.gz",
		"a backslash":          "website\\x.tar.gz",
		"an empty segment":     "website//x.tar.gz",
		"a leading dash":       "-rf",
		"a trailing slash":     "website/",
		"a space":              "website/my site.tar.gz",
		"a quote":              "website/x\".tar.gz",
		"a semicolon":          "website/x;rm.tar.gz",
		"a newline":            "website/x\n.tar.gz",
		"a null byte":          "website/x\x00.tar.gz",
		"a tilde":              "website/~root/x.tar.gz",
		"nothing at all":       "",
		"a percent escape":     "website/%2e%2e/x",
		"an absolute-ish path": "//etc/shadow",
	}
	for name, key := range cases {
		if err := BackupKey(key); err == nil {
			t.Errorf("BackupKey accepted %s (%q)", name, key)
		} else if !errors.Is(err, ErrInvalidBackupKey) {
			t.Errorf("BackupKey(%q) = %v, want ErrInvalidBackupKey", key, err)
		}
	}
}

func TestBackupKeyRefusesOneLongerThanTheLimit(t *testing.T) {
	if err := BackupKey(strings.Repeat("a", MaxBackupKeyLength+1)); err == nil {
		t.Fatal("BackupKey accepted a key over the length limit")
	}
}

func TestS3EndpointRefusesPlainHTTPToAnotherMachine(t *testing.T) {
	// A backup sent over plain http is a copy of every site on the host, read
	// by anyone on the path.
	if err := S3Endpoint("http://storage.example.com", false); err == nil {
		t.Fatal("S3Endpoint accepted plain http to another machine")
	}
}

func TestS3EndpointAllowsPlainHTTPToThisMachine(t *testing.T) {
	// The narrow exception, which is what makes a local S3 service testable.
	for _, endpoint := range []string{
		"http://localhost:9000", "http://127.0.0.1:9000", "http://[::1]:9000",
	} {
		if err := S3Endpoint(endpoint, false); err != nil {
			t.Errorf("S3Endpoint(%q) = %v, want nil", endpoint, err)
		}
	}
}

func TestS3EndpointRefusesCredentialsInTheURL(t *testing.T) {
	// A URL carrying a secret ends up in a log the first time anything prints
	// the destination.
	if err := S3Endpoint("https://key:secret@storage.example.com", false); err == nil {
		t.Fatal("S3Endpoint accepted credentials in the URL")
	}
}

func TestS3EndpointRefusesAPath(t *testing.T) {
	// The bucket and key are appended to the endpoint, so a path here would
	// silently address a different object than the one asked for.
	if err := S3Endpoint("https://storage.example.com/some/prefix", false); err == nil {
		t.Fatal("S3Endpoint accepted a path")
	}
}

func TestS3BucketFollowsTheRulesEveryImplementationShares(t *testing.T) {
	ok := []string{"backups", "my-backups", "b1.b2", "abc"}
	for _, bucket := range ok {
		if err := S3Bucket(bucket); err != nil {
			t.Errorf("S3Bucket(%q) = %v, want nil", bucket, err)
		}
	}

	bad := map[string]string{
		"too short":     "ab",
		"uppercase":     "Backups",
		"a slash":       "backups/nested",
		"leading dash":  "-backups",
		"trailing dot":  "backups.",
		"two dots":      "back..ups",
		"an underscore": "my_backups",
		"far too long":  strings.Repeat("a", 64),
	}
	for name, bucket := range bad {
		if err := S3Bucket(bucket); err == nil {
			t.Errorf("S3Bucket accepted %s (%q)", name, bucket)
		}
	}
}

func TestSFTPUserRefusesALeadingDash(t *testing.T) {
	// The account becomes part of a "user@host" argument, and a value read as
	// an option changes what the client does rather than where it connects.
	if err := SFTPUser("-oProxyCommand=id"); err == nil {
		t.Fatal("SFTPUser accepted a value that would be read as an option")
	}
}

func TestSFTPUserRefusesWhitespaceAndSeparators(t *testing.T) {
	for _, user := range []string{"back up", "root@evil", "a:b", "a/b", "a\nb"} {
		if err := SFTPUser(user); err == nil {
			t.Errorf("SFTPUser accepted %q", user)
		}
	}
}

func TestLocalBackupRootMustBeAbsoluteAndNormalised(t *testing.T) {
	if err := LocalBackupRoot("/var/lib/jothost/backups"); err != nil {
		t.Fatalf("LocalBackupRoot rejected a good path: %v", err)
	}
	for _, dir := range []string{"", "relative/path", "/var/../etc", "/var/lib/./x"} {
		if err := LocalBackupRoot(dir); err == nil {
			t.Errorf("LocalBackupRoot accepted %q", dir)
		}
	}
}

func TestRetentionRefusesSettingsThatWouldLoseEverything(t *testing.T) {
	// Zero days would delete a backup the moment it finished; zero kept would
	// let a schedule prune its way to nothing.
	if err := RetentionDays(0); err == nil {
		t.Error("RetentionDays accepted zero days")
	}
	if err := KeepLast(0); err == nil {
		t.Error("KeepLast accepted keeping none")
	}
	if err := RetentionDays(14); err != nil {
		t.Errorf("RetentionDays(14) = %v, want nil", err)
	}
	if err := KeepLast(3); err != nil {
		t.Errorf("KeepLast(3) = %v, want nil", err)
	}
}

func TestChecksumIsStrictAboutCaseAndWhitespace(t *testing.T) {
	good := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if err := Checksum(good); err != nil {
		t.Fatalf("Checksum rejected a valid digest: %v", err)
	}
	// A digest in the wrong case, or with whitespace, would never match and
	// would report every intact archive as corrupt.
	for _, digest := range []string{
		strings.ToUpper(good), " " + good, good + "\n", good[:63], good + "a", "",
	} {
		if err := Checksum(digest); err == nil {
			t.Errorf("Checksum accepted %q", digest)
		}
	}
}

func TestScheduleDayAcceptsEveryDayAndTheSevenDays(t *testing.T) {
	if err := ScheduleDay(EveryDay); err != nil {
		t.Fatalf("ScheduleDay(EveryDay) = %v, want nil", err)
	}
	for day := 0; day <= 6; day++ {
		if err := ScheduleDay(day); err != nil {
			t.Errorf("ScheduleDay(%d) = %v, want nil", day, err)
		}
	}
	if err := ScheduleDay(7); err == nil {
		t.Error("ScheduleDay accepted 7")
	}
}

func TestBackupTypeIsAClosedSet(t *testing.T) {
	for _, kind := range BackupTypes {
		if err := BackupType(kind); err != nil {
			t.Errorf("BackupType(%q) = %v, want nil", kind, err)
		}
	}
	// "files" is the one somebody would reach for: a backup of a path nobody
	// named is a restore with nowhere to put it back.
	for _, kind := range []string{"", "files", "everything", "WEBSITE"} {
		if err := BackupType(kind); err == nil {
			t.Errorf("BackupType accepted %q", kind)
		}
	}
}

func TestDestinationKindIsAClosedSet(t *testing.T) {
	for _, kind := range DestinationKinds {
		if err := DestinationKind(kind); err != nil {
			t.Errorf("DestinationKind(%q) = %v, want nil", kind, err)
		}
	}
	for _, kind := range []string{"", "ftp", "dropbox", "S3"} {
		if err := DestinationKind(kind); err == nil {
			t.Errorf("DestinationKind accepted %q", kind)
		}
	}
}

func TestS3EndpointAllowsPlainHTTPOnlyWhenSomebodyAcceptsIt(t *testing.T) {
	// Self-hosted object storage on a private network is an ordinary
	// arrangement, and a panel that flatly refused it would be worked around
	// rather than obeyed. So it is possible and never the default: accepting a
	// backup that travels in the clear has to be a decision somebody made.
	const endpoint = "http://minio.internal:9000"
	if err := S3Endpoint(endpoint, false); err == nil {
		t.Fatal("plain http to another machine was accepted without being asked for")
	}
	if err := S3Endpoint(endpoint, true); err != nil {
		t.Fatalf("plain http was refused even when accepted: %v", err)
	}
	// The acknowledgement does not turn off the rest of the checks.
	if err := S3Endpoint("http://minio.internal:9000/path", true); err == nil {
		t.Error("accepting plain http also accepted an endpoint with a path")
	}
	if err := S3Endpoint("ftp://minio.internal", true); err == nil {
		t.Error("accepting plain http also accepted a non-http scheme")
	}
}
