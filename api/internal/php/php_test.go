package php_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/api/internal/php"
	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/api/internal/websites"
)

type fixture struct {
	service  *php.Service
	repo     *php.Repository
	websites *websites.Repository
	jobs     *jobs.Repository
	siteSvc  *websites.Service
	serverID string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	deps := testsupport.Require(t)
	serverID := registerServer(t, deps.Pool)

	repo := php.NewRepository(deps.Pool)
	websiteRepo := websites.NewRepository(deps.Pool)
	jobRepo := jobs.NewRepository(deps.Pool)

	return &fixture{
		repo:     repo,
		websites: websiteRepo,
		jobs:     jobRepo,
		serverID: serverID,
		service: php.NewService(php.ServiceOptions{
			Repository: repo,
			Websites:   websiteRepo,
			Jobs:       jobRepo,
		}),
		siteSvc: websites.NewService(websites.ServiceOptions{
			Repository: websiteRepo,
			Jobs:       jobRepo,
			ServerID:   serverID,
		}),
	}
}

func registerServer(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()

	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO servers (hostname, status)
		VALUES ('php-test-host', 'online')
		RETURNING id::text`).Scan(&id)
	if err != nil {
		t.Fatalf("register server: %v", err)
	}
	return id
}

// activeSite creates a website and moves it to active, which is the state a
// real site is in by the time anyone selects a PHP version for it.
func (f *fixture) activeSite(t *testing.T, domain string) websites.Website {
	t.Helper()
	ctx := context.Background()

	created, err := f.siteSvc.Create(ctx, websites.CreateRequest{Domain: domain})
	if err != nil {
		t.Fatalf("create website: %v", err)
	}
	if err := f.websites.SetStatus(ctx, created.Website.ID, websites.StatusActive); err != nil {
		t.Fatalf("activate website: %v", err)
	}

	site, err := f.websites.Get(ctx, created.Website.ID)
	if err != nil {
		t.Fatalf("get website: %v", err)
	}
	return site
}

// installVersion records a version as present on the host.
func (f *fixture) installVersion(t *testing.T, version string) {
	t.Helper()

	err := f.repo.SyncVersions(context.Background(), []php.DetectedVersion{{
		Version:    version,
		BinaryPath: "/usr/sbin/php-fpm" + version,
		FPMService: "php-fpm" + version,
	}})
	if err != nil {
		t.Fatalf("sync versions: %v", err)
	}
}

// ------------------------------------------------------------------ versions

func TestSyncVersionsReflectsTheHost(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.installVersion(t, "8.3")

	versions, err := f.repo.ListVersions(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(versions) != 1 || versions[0].Version != "8.3" || !versions[0].Installed {
		t.Fatalf("versions = %+v, want one installed 8.3", versions)
	}
}

// A version removed from the host must stop being offered, or a site pointed
// at it returns 502 with no obvious cause.
func TestSyncVersionsMarksRemovedVersionsUninstalled(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.installVersion(t, "8.3")
	// The next detection no longer reports it.
	if err := f.repo.SyncVersions(ctx, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	version, err := f.repo.GetVersion(ctx, "8.3")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if version.Installed {
		t.Fatal("a version no longer on the host is still marked installed")
	}
	// The row survives so a site referencing it still has something to point
	// at, rather than the reference dangling.
	if version.Version != "8.3" {
		t.Fatalf("the version row was deleted rather than marked: %+v", version)
	}
}

// A removal that finished must not leave the panel showing one in progress.
//
// A version whose install failed is already installed = FALSE, so a sync keyed
// only on that flag skipped it — and "removing" became permanent for exactly
// the row that most needed settling.
func TestSyncVersionsSettlesAFinishedRemoval(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if err := f.repo.SetVersionStatus(ctx, "8.5", php.StatusRemoving); err != nil {
		t.Fatalf("set removing: %v", err)
	}

	// The host does not report 8.5, so the removal is done.
	if err := f.repo.SyncVersions(ctx, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	version, err := f.repo.GetVersion(ctx, "8.5")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if version.Status != php.StatusAvailable {
		t.Fatalf("status = %q, want %q once the removal has finished",
			version.Status, php.StatusAvailable)
	}
	if version.Installed {
		t.Fatal("a removed version is still marked installed")
	}
}

// A failure is the record of why a version is not on the host. Clearing it on
// the next sweep would erase the only explanation the operator has.
func TestSyncVersionsKeepsAFailedVersionFailed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if err := f.repo.SetVersionStatus(ctx, "8.5", php.StatusFailed); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	if err := f.repo.SyncVersions(ctx, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	version, err := f.repo.GetVersion(ctx, "8.5")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if version.Status != php.StatusFailed {
		t.Fatalf("status = %q, want it to stay %q", version.Status, php.StatusFailed)
	}
}

// An install still running must not be reported as settled by a sync that
// happens to fire while it is in flight.
func TestSyncVersionsLeavesAnInstallInFlight(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if err := f.repo.SetVersionStatus(ctx, "8.5", php.StatusInstalling); err != nil {
		t.Fatalf("set installing: %v", err)
	}
	if err := f.repo.SyncVersions(ctx, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	version, err := f.repo.GetVersion(ctx, "8.5")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if version.Status != php.StatusInstalling {
		t.Fatalf("status = %q, want it to stay %q while the install runs",
			version.Status, php.StatusInstalling)
	}
}

func TestListVersionsOrdersNumerically(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// 8.10 must sort above 8.9; a text sort gets this backwards.
	err := f.repo.SyncVersions(ctx, []php.DetectedVersion{
		{Version: "8.9", BinaryPath: "/usr/sbin/php-fpm8.9"},
		{Version: "8.10", BinaryPath: "/usr/sbin/php-fpm8.10"},
		{Version: "8.2", BinaryPath: "/usr/sbin/php-fpm8.2"},
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	versions, err := f.repo.ListVersions(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	want := []string{"8.10", "8.9", "8.2"}
	for i, version := range versions {
		if version.Version != want[i] {
			t.Fatalf("position %d = %q, want %q (order: %+v)", i, version.Version, want[i], versions)
		}
	}
}

// ------------------------------------------------------------- selecting PHP

func TestSetVersionRecordsThePoolAndQueuesTheWork(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.installVersion(t, "8.3")
	site := f.activeSite(t, "example.test")

	job, err := f.service.SetVersion(ctx, php.SetRequest{
		WebsiteID: site.ID,
		Version:   "8.3",
	})
	if err != nil {
		t.Fatalf("set version: %v", err)
	}

	if job.Type != jobs.TypeWebsitePHPSet {
		t.Fatalf("job type = %q, want website.php.set", job.Type)
	}

	pool, err := f.repo.GetPool(ctx, site.ID)
	if err != nil {
		t.Fatalf("get pool: %v", err)
	}
	if pool.PHPVersion != "8.3" {
		t.Fatalf("pool version = %q", pool.PHPVersion)
	}
	// The socket carries the version, so switching versions never has two FPM
	// masters contending for one path.
	if pool.SocketPath == "" || pool.PoolName == "" {
		t.Fatalf("pool = %+v, want a name and socket", pool)
	}
	if !contains(pool.SocketPath, "83") {
		t.Fatalf("socket %q does not name the version", pool.SocketPath)
	}
}

// Writing a pool for a version that is not installed produces a site that
// 502s on its first request, and the cause is far from obvious by then.
func TestSetVersionRefusesAnUninstalledVersion(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	site := f.activeSite(t, "example.test")

	_, err := f.service.SetVersion(ctx, php.SetRequest{
		WebsiteID: site.ID,
		Version:   "8.3",
	})
	if !errors.Is(err, php.ErrVersionUnavailable) {
		t.Fatalf("set uninstalled version = %v, want ErrVersionUnavailable", err)
	}

	if _, err := f.repo.GetPool(ctx, site.ID); !errors.Is(err, php.ErrPoolNotFound) {
		t.Fatal("a refused request left a pool record behind")
	}
}

func TestSetVersionRefusesAVersionThatWasRemoved(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.installVersion(t, "8.3")
	if err := f.repo.SyncVersions(ctx, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	site := f.activeSite(t, "example.test")

	// The row still exists but says installed = false.
	_, err := f.service.SetVersion(ctx, php.SetRequest{WebsiteID: site.ID, Version: "8.3"})
	if !errors.Is(err, php.ErrVersionUnavailable) {
		t.Fatalf("= %v, want ErrVersionUnavailable", err)
	}
}

func TestSetVersionRejectsAMalformedVersion(t *testing.T) {
	f := newFixture(t)
	site := f.activeSite(t, "example.test")

	for _, version := range []string{"8.3.19", "latest", "8.3; rm -rf /", "8"} {
		_, err := f.service.SetVersion(context.Background(), php.SetRequest{
			WebsiteID: site.ID,
			Version:   version,
		})
		if err == nil {
			t.Fatalf("version %q must be rejected", version)
		}
	}
}

// A site mid-provision has no directories yet and one mid-delete is going
// away; queuing a pool for either produces work that cannot succeed.
func TestSetVersionRefusesASiteThatIsNotReady(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.installVersion(t, "8.3")
	created, err := f.siteSvc.Create(ctx, websites.CreateRequest{Domain: "example.test"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Still "creating".
	_, err = f.service.SetVersion(ctx, php.SetRequest{
		WebsiteID: created.Website.ID,
		Version:   "8.3",
	})
	if !errors.Is(err, php.ErrWebsiteNotReady) {
		t.Fatalf("= %v, want ErrWebsiteNotReady", err)
	}
}

func TestSetVersionValidatesSettings(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.installVersion(t, "8.3")
	site := f.activeSite(t, "example.test")

	bad := "512M\nuser = root"
	_, err := f.service.SetVersion(ctx, php.SetRequest{
		WebsiteID: site.ID,
		Version:   "8.3",
		Settings:  php.Settings{MemoryLimit: &bad},
	})
	if err == nil {
		t.Fatal("an injected memory limit must be rejected before it is stored")
	}
}

// A PATCH that changes one value must not silently reset the others.
func TestSettingsAreMergedOverWhatThePoolAlreadyHas(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.installVersion(t, "8.3")
	site := f.activeSite(t, "example.test")

	memory := "512M"
	if _, err := f.service.SetVersion(ctx, php.SetRequest{
		WebsiteID: site.ID,
		Version:   "8.3",
		Settings:  php.Settings{MemoryLimit: &memory},
	}); err != nil {
		t.Fatalf("first set: %v", err)
	}

	// Change only the upload size.
	upload := "128M"
	if _, err := f.service.SetVersion(ctx, php.SetRequest{
		WebsiteID: site.ID,
		Version:   "8.3",
		Settings:  php.Settings{UploadMaxFilesize: &upload},
	}); err != nil {
		t.Fatalf("second set: %v", err)
	}

	pool, err := f.repo.GetPool(ctx, site.ID)
	if err != nil {
		t.Fatalf("get pool: %v", err)
	}
	if pool.MemoryLimit == nil || *pool.MemoryLimit != "512M" {
		t.Fatalf("memory limit = %v, want it preserved at 512M", pool.MemoryLimit)
	}
	if pool.UploadMaxFilesize == nil || *pool.UploadMaxFilesize != "128M" {
		t.Fatalf("upload size = %v, want 128M", pool.UploadMaxFilesize)
	}
}

func TestSwitchingVersionReplacesThePoolRatherThanAddingOne(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.installVersion(t, "8.3")
	f.installVersion(t, "8.4")
	// installVersion syncs, which would mark 8.3 removed; re-sync both.
	if err := f.repo.SyncVersions(ctx, []php.DetectedVersion{
		{Version: "8.3", BinaryPath: "/usr/sbin/php-fpm83"},
		{Version: "8.4", BinaryPath: "/usr/sbin/php-fpm84"},
	}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	site := f.activeSite(t, "example.test")

	if _, err := f.service.SetVersion(ctx, php.SetRequest{WebsiteID: site.ID, Version: "8.3"}); err != nil {
		t.Fatalf("set 8.3: %v", err)
	}
	if _, err := f.service.SetVersion(ctx, php.SetRequest{WebsiteID: site.ID, Version: "8.4"}); err != nil {
		t.Fatalf("set 8.4: %v", err)
	}

	// The website_id column is unique: two pools for one site would race for
	// the same socket.
	pool, err := f.repo.GetPool(ctx, site.ID)
	if err != nil {
		t.Fatalf("get pool: %v", err)
	}
	if pool.PHPVersion != "8.4" {
		t.Fatalf("pool version = %q, want 8.4", pool.PHPVersion)
	}
}

func TestDisablingPHPQueuesAnUnsetJob(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.installVersion(t, "8.3")
	site := f.activeSite(t, "example.test")

	if _, err := f.service.SetVersion(ctx, php.SetRequest{WebsiteID: site.ID, Version: "8.3"}); err != nil {
		t.Fatalf("enable: %v", err)
	}

	job, err := f.service.SetVersion(ctx, php.SetRequest{WebsiteID: site.ID, Version: ""})
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if job.Type != jobs.TypeWebsitePHPUnset {
		t.Fatalf("job type = %q, want website.php.unset", job.Type)
	}
}

func TestDisablingPHPOnAStaticSiteIsRefused(t *testing.T) {
	f := newFixture(t)
	site := f.activeSite(t, "example.test")

	_, err := f.service.SetVersion(context.Background(),
		php.SetRequest{WebsiteID: site.ID, Version: ""})
	if !errors.Is(err, php.ErrPoolNotFound) {
		t.Fatalf("= %v, want ErrPoolNotFound", err)
	}
}

// ---------------------------------------------------------------- uninstall

// Removing a version websites still run would take every one of them offline
// on a request that looked routine.
func TestUninstallRefusesAVersionStillInUse(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.installVersion(t, "8.3")
	site := f.activeSite(t, "example.test")
	if _, err := f.service.SetVersion(ctx, php.SetRequest{WebsiteID: site.ID, Version: "8.3"}); err != nil {
		t.Fatalf("set version: %v", err)
	}

	_, err := f.service.Uninstall(ctx, "8.3", php.Actor{})
	if !errors.Is(err, php.ErrVersionInUse) {
		t.Fatalf("uninstall in-use version = %v, want ErrVersionInUse", err)
	}
}

func TestUninstallAllowsAnUnusedVersion(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.installVersion(t, "8.3")

	job, err := f.service.Uninstall(ctx, "8.3", php.Actor{})
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if job.Type != jobs.TypePHPUninstall {
		t.Fatalf("job type = %q", job.Type)
	}
}

func TestInstallQueuesAJob(t *testing.T) {
	f := newFixture(t)

	job, err := f.service.Install(context.Background(), "8.3", php.Actor{})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if job.Type != jobs.TypePHPInstall {
		t.Fatalf("job type = %q", job.Type)
	}
	if got := job.Payload["version"]; got != "8.3" {
		t.Fatalf("payload version = %v", got)
	}
}

func TestInstallRejectsAMalformedVersion(t *testing.T) {
	f := newFixture(t)

	for _, version := range []string{"", "latest", "8.3; rm -rf /", "../8.3"} {
		if _, err := f.service.Install(context.Background(), version, php.Actor{}); err == nil {
			t.Fatalf("version %q must be rejected", version)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
