package cron

import (
	"context"
	"strings"
	"testing"
)

// What is tested here is the part of this package that decides what a job
// becomes: the command line written into a crontab. Two of the three job types
// are built by the panel from a validated fragment, and this is where that
// claim either holds or does not.
//
// The repository and the Agent calls are exercised by the integration suite
// against a real database and a real cron daemon; mocking either here would
// test a mock.

func service() *Service { return NewService(ServiceOptions{}) }

func site() WebsiteRef {
	return WebsiteRef{
		ID:           "11111111-2222-4333-8444-555555555555",
		ServerID:     "99999999-8888-4777-8666-555555555555",
		Domain:       "shop.example",
		SystemUser:   "web_shop_example",
		DocumentRoot: "/var/www/shop.example/public",
		PHPVersion:   "8.4",
	}
}

func TestACommandJobIsTheCommandItself(t *testing.T) {
	got, err := service().buildCommand(context.Background(), "req", "command",
		"php artisan schedule:run", site())
	if err != nil {
		t.Fatalf("buildCommand: %v", err)
	}
	if got != "php artisan schedule:run" {
		t.Fatalf("command = %q, want it unchanged", got)
	}
}

// A URL job is a fetch of an address that has been parsed, with bounds the
// panel chose: -f so an HTTP error is a failed job rather than a success that
// saved an error page, and --max-time so a hung endpoint cannot leave curl
// running until the next run starts.
func TestAURLJobBecomesABoundedFetch(t *testing.T) {
	got, err := service().buildCommand(context.Background(), "req", "url",
		"https://shop.example/cron", site())
	if err != nil {
		t.Fatalf("buildCommand: %v", err)
	}
	if got != "curl -fsS --max-time 300 https://shop.example/cron" {
		t.Fatalf("command = %q", got)
	}
}

func TestAURLJobRefusesWhatItCannotFetchSafely(t *testing.T) {
	cases := []struct{ target, reason string }{
		{"file:///etc/passwd", "a scheme that is not a fetch"},
		{"ftp://x.test/a", "a scheme curl would treat differently"},
		{"https:///nohost", "no host"},
		{"notaurl", "not a URL at all"},
		// The address goes into a crontab line unquoted, so anything a shell
		// would read as syntax is refused rather than escaped.
		{"https://x.test/a;id", "a command separator"},
		{"https://x.test/$(id)", "a substitution"},
		{"https://x.test/a b", "a space"},
		{"https://x.test/a`id`", "a backtick"},
		{"https://x.test/a|id", "a pipe"},
		{"https://x.test/a%2Fb", "cron's percent escape"},
	}

	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			if _, err := service().buildCommand(context.Background(), "req", "url",
				tc.target, site()); err == nil {
				t.Fatalf("%q was accepted: %s", tc.target, tc.reason)
			}
		})
	}
}

// A PHP job names a file inside its own site's directory. The path is built
// here, from the site's document root and a fragment that has been checked, so
// the only thing the caller decides is which file *within* the site runs.
func TestScriptPathStaysInsideTheSite(t *testing.T) {
	root := "/var/www/shop.example/public"

	got, err := scriptPath(root, "cron.php")
	if err != nil {
		t.Fatalf("scriptPath: %v", err)
	}
	if got != root+"/cron.php" {
		t.Fatalf("path = %q", got)
	}

	// A subdirectory is fine: it is still inside the site.
	got, err = scriptPath(root, "bin/tasks.php")
	if err != nil {
		t.Fatalf("scriptPath: %v", err)
	}
	if got != root+"/bin/tasks.php" {
		t.Fatalf("path = %q", got)
	}
}

func TestScriptPathRefusesWhatIsNotAScriptInTheSite(t *testing.T) {
	root := "/var/www/shop.example/public"

	cases := []struct{ target, reason string }{
		{"", "an empty path"},
		{"/etc/shadow.php", "an absolute path"},
		// Refused rather than normalised: anchoring it would keep it inside the
		// root, but the operator would get a job pointing somewhere they did
		// not name, failing later about a file they never asked for.
		{"../../../etc/passwd.php", "a traversal"},
		{"a/../../b.php", "a traversal in the middle"},
		{"cron.sh", "something that is not PHP"},
		{"cron.php; id", "a command separator"},
		{"cron.php && id", "a conjunction"},
		{"my cron.php", "a space, which would need quoting"},
		{"$(id).php", "a substitution"},
		{"cron.php\nid", "a newline"},
		{"cron%.php", "cron's percent escape"},
	}

	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			if _, err := scriptPath(root, tc.target); err == nil {
				t.Fatalf("%q was accepted: %s", tc.target, tc.reason)
			}
		})
	}
}

func TestAPHPJobNeedsASiteWithAVersion(t *testing.T) {
	without := site()
	without.PHPVersion = ""

	_, err := service().buildCommand(context.Background(), "req", "php", "cron.php", without)
	if err == nil {
		t.Fatal("a PHP job was accepted for a site with no PHP version")
	}
	if !strings.Contains(err.Error(), "PHP version") {
		t.Fatalf("error = %q, want it to say what is missing", err)
	}
}

func TestAJobTypeThePanelDoesNotHaveIsRefused(t *testing.T) {
	for _, jobType := range []string{"", "shell", "PHP", "exec"} {
		if _, err := service().buildCommand(context.Background(), "req", jobType,
			"true", site()); err == nil {
			t.Fatalf("%q was accepted as a job type", jobType)
		}
	}
}

// A disabled job has no next run, and showing one would say it is about to
// happen. A schedule that can never fire — 31 February is a valid expression —
// has none either, and the page says so rather than leaving it blank.
func TestNextRunIsFilledInOnlyWhereThereIsOne(t *testing.T) {
	jobs := withNextRun([]Job{
		{ID: "a", Schedule: "* * * * *", Enabled: true},
		{ID: "b", Schedule: "* * * * *", Enabled: false},
		{ID: "c", Schedule: "0 0 31 2 *", Enabled: true},
	})

	if jobs[0].NextRunAt == nil {
		t.Fatal("an enabled job has no next run")
	}
	if jobs[1].NextRunAt != nil {
		t.Fatal("a disabled job was given a next run")
	}
	if jobs[2].NextRunAt != nil {
		t.Fatal("a schedule that never fires was given a next run")
	}
}
