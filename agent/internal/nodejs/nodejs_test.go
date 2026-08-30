package nodejs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testApp() App {
	return App{
		Name:    "shop",
		Version: "22",
		Root:    "/var/www/shop.example/public",
		Startup: "server.js",
		Port:    3000,
		User:    "web_shop",
		Group:   "web_shop",
	}
}

func TestAppValidateAcceptsAnOrdinaryApplication(t *testing.T) {
	if err := testApp().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// Every one of these becomes part of a unit file, an argument vector, or an
// environment. None may be accepted.
func TestAppValidateRejectsDangerousValues(t *testing.T) {
	cases := map[string]func(*App){
		"a name that is a unit suffix":   func(a *App) { a.Name = "shop.service" },
		"a name with an instance marker": func(a *App) { a.Name = "shop@host" },
		"an absolute startup file":       func(a *App) { a.Startup = "/etc/passwd" },
		"a traversing startup file":      func(a *App) { a.Startup = "../../server.js" },
		"a relative root":                func(a *App) { a.Root = "var/www/shop" },
		"a traversing root":              func(a *App) { a.Root = "/var/www/../etc" },
		"a privileged port":              func(a *App) { a.Port = 80 },
		"an ephemeral port":              func(a *App) { a.Port = 40000 },
		"root as the account":            func(a *App) { a.User = "root" },
	}

	for name, mutate := range cases {
		app := testApp()
		mutate(&app)
		if err := app.Validate(); err == nil {
			t.Errorf("Validate accepted %s", name)
		}
	}
}

// Setting one of these would turn "configure my application" into "run
// something else in it".
func TestAppValidateRejectsLoaderEnvironmentVariables(t *testing.T) {
	for _, key := range []string{"LD_PRELOAD", "NODE_OPTIONS", "PATH"} {
		app := testApp()
		app.Environment = map[string]string{key: "/tmp/evil.so"}
		if err := app.Validate(); err == nil {
			t.Errorf("Validate accepted %s", key)
		}
	}
}

// A newline in a value would close the line in a unit or env file and start a
// directive of the caller's choosing.
func TestAppValidateRejectsEnvironmentValuesWithLineBreaks(t *testing.T) {
	app := testApp()
	app.Environment = map[string]string{"CONFIG": "a\nExecStart=/bin/sh -c 'id'"}
	if err := app.Validate(); err == nil {
		t.Fatal("Validate accepted a value containing a newline")
	}
}

// PORT is set by the panel from the application's record. An application that
// could override it would listen somewhere nginx is not pointed at.
func TestEnvironIsAuthoritativeAboutThePort(t *testing.T) {
	app := testApp()
	app.Environment = map[string]string{"PORT": "9999"}

	env := app.Environ()
	if env["PORT"] != "3000" {
		t.Fatalf("PORT = %q, want the application's own port", env["PORT"])
	}
}

// NODE_ENV is the one default an application may change: a staging deployment
// legitimately wants a different value, and nothing outside the process has to
// agree with it.
func TestEnvironLetsTheApplicationChooseItsNodeEnv(t *testing.T) {
	app := testApp()
	app.Environment = map[string]string{"NODE_ENV": "staging"}

	if got := app.Environ()["NODE_ENV"]; got != "staging" {
		t.Fatalf("NODE_ENV = %q, want staging", got)
	}

	// And it defaults to production when nothing says otherwise.
	if got := testApp().Environ()["NODE_ENV"]; got != "production" {
		t.Fatalf("default NODE_ENV = %q, want production", got)
	}
}

func TestEnvFileRoundTrips(t *testing.T) {
	dir := t.TempDir()
	original := StateDir
	t.Cleanup(func() { setStateDir(original) })
	setStateDir(dir)

	app := testApp()
	app.Environment = map[string]string{
		"DATABASE_URL": "postgres://u:p@localhost/db?sslmode=disable",
		"API_KEY":      "abc def ghi",
	}

	if err := WriteEnvFile(app, os.Getuid(), os.Getgid()); err != nil {
		t.Fatalf("WriteEnvFile: %v", err)
	}

	read, err := ReadEnvFile(app)
	if err != nil {
		t.Fatalf("ReadEnvFile: %v", err)
	}
	for key, want := range app.Environment {
		if read[key] != want {
			t.Errorf("%s = %q, want %q", key, read[key], want)
		}
	}
	if read["PORT"] != "3000" {
		t.Errorf("PORT = %q, want the panel's value", read["PORT"])
	}
}

// The file holds whatever secrets the application needs, so no other account
// on a shared host has any business reading it.
func TestEnvFileIsNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	original := StateDir
	t.Cleanup(func() { setStateDir(original) })
	setStateDir(dir)

	app := testApp()
	if err := WriteEnvFile(app, os.Getuid(), os.Getgid()); err != nil {
		t.Fatalf("WriteEnvFile: %v", err)
	}

	info, err := os.Stat(app.EnvFile())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm()&0o007 != 0 {
		t.Fatalf("mode = %v, want nothing for other", info.Mode().Perm())
	}
}

// ------------------------------------------------------------ the unit file

func TestRenderUnitConfinesTheApplication(t *testing.T) {
	rendered, err := RenderUnit(testApp(), "/usr/bin/node")
	if err != nil {
		t.Fatalf("RenderUnit: %v", err)
	}

	// The hardening is the reason for using systemd at all. Losing any of it
	// silently would not fail anything else.
	for _, directive := range []string{
		"NoNewPrivileges=true",
		"PrivateTmp=true",
		"ProtectSystem=strict",
		"ProtectHome=true",
		"RestrictSUIDSGID=true",
		"RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX",
	} {
		if !strings.Contains(rendered, directive) {
			t.Errorf("the unit is missing %s", directive)
		}
	}

	// It must not run as root, and it must not be able to write outside its
	// own directory and its logs.
	if !strings.Contains(rendered, "User=web_shop") {
		t.Error("the unit does not name the site's account")
	}
	if !strings.Contains(rendered, "ReadWritePaths=/var/www/shop.example/public") {
		t.Error("the unit does not confine what it may write")
	}
}

// A restart loop on a broken deployment burns the host's CPU and hides the
// failure. systemd's start limit is what stops it.
func TestRenderUnitBoundsRestarts(t *testing.T) {
	rendered, err := RenderUnit(testApp(), "/usr/bin/node")
	if err != nil {
		t.Fatalf("RenderUnit: %v", err)
	}
	for _, directive := range []string{"Restart=always", "StartLimitBurst=5"} {
		if !strings.Contains(rendered, directive) {
			t.Errorf("the unit is missing %s", directive)
		}
	}
}

func TestRenderUnitRefusesAnInvalidApplication(t *testing.T) {
	app := testApp()
	app.Name = "shop; rm -rf /"
	if _, err := RenderUnit(app, "/usr/bin/node"); err == nil {
		t.Fatal("RenderUnit accepted an invalid name")
	}
}

func TestRenderUnitRefusesARelativeBinary(t *testing.T) {
	// A relative ExecStart would be resolved against systemd's own working
	// directory, which is not something a caller should be able to depend on.
	if _, err := RenderUnit(testApp(), "node"); err == nil {
		t.Fatal("RenderUnit accepted a relative binary path")
	}
}

func TestUnitAndFileNamesAreDerivedFromTheName(t *testing.T) {
	app := testApp()
	if app.UnitName() != "jothost-node-shop.service" {
		t.Errorf("UnitName = %q", app.UnitName())
	}
	if filepath.Base(app.PIDFile()) != "shop.pid" {
		t.Errorf("PIDFile = %q", app.PIDFile())
	}
	if app.StartupPath() != "/var/www/shop.example/public/server.js" {
		t.Errorf("StartupPath = %q", app.StartupPath())
	}
}

// ------------------------------------------------------------------- logs

func TestTailFileReturnsTheLastLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.log")

	var builder strings.Builder
	for i := 0; i < 500; i++ {
		builder.WriteString("line ")
		builder.WriteString(strings.Repeat("x", 10))
		builder.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	lines := tailFile(path, 10)
	if len(lines) != 10 {
		t.Fatalf("got %d lines, want 10", len(lines))
	}
}

func TestTailFileHandlesAMissingLog(t *testing.T) {
	// An application that has not started yet has no log, which is not an
	// error — it is an empty list.
	if lines := tailFile(filepath.Join(t.TempDir(), "absent.log"), 10); len(lines) != 0 {
		t.Fatalf("got %v, want nothing", lines)
	}
}

// ----------------------------------------------------------------- offers

func TestExtensionOfInstallerOffersIsPerManager(t *testing.T) {
	installer := NewInstaller(nil, nil)
	if installer.Available() {
		t.Fatal("an installer with no package manager reported itself available")
	}
	if offers := installer.Offers(); len(offers) != 0 {
		t.Fatalf("offers = %v, want none without a package manager", offers)
	}
}
