package updates

import (
	"strings"
	"testing"
	"time"

	"github.com/jothost/panel/shared/validate"
)

// The panel's own rules about updates, tested without a database or a host:
// when the scheduler decides something is due, what a transcript is trimmed to,
// and which packages an exclusion removes.

func scheduler(now time.Time) *Scheduler {
	return NewScheduler(SchedulerOptions{
		ServerID: "server-1",
		Now:      func() time.Time { return now },
	})
}

func at(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed
}

func TestCheckIsDueOnAHostNobodyHasCheckedYet(t *testing.T) {
	// Looking once at startup is what makes a freshly installed panel show
	// something other than "unknown" on its first day.
	settings := DefaultSettings("server-1")
	if !scheduler(at(t, "2026-09-03T09:00:00Z")).checkDue(settings) {
		t.Fatal("a host that has never been checked was not due")
	}
}

func TestCheckIsDueOnlyAfterTheInterval(t *testing.T) {
	now := at(t, "2026-09-03T09:00:00Z")
	settings := DefaultSettings("server-1")
	settings.CheckIntervalHours = 6

	recent := now.Add(-2 * time.Hour)
	settings.LastCheckedAt = &recent
	if scheduler(now).checkDue(settings) {
		t.Error("a check two hours after the last one was due with a six-hour interval")
	}

	old := now.Add(-7 * time.Hour)
	settings.LastCheckedAt = &old
	if !scheduler(now).checkDue(settings) {
		t.Error("a check seven hours after the last one was not due")
	}
}

func TestTheWindowIsOnlyDueAtItsOwnMinute(t *testing.T) {
	settings := DefaultSettings("server-1")
	settings.Policy = validate.UpdatesAll
	settings.DayOfWeek = validate.EveryDay
	settings.Hour = 3
	settings.Minute = 30

	if !scheduler(at(t, "2026-09-03T03:30:00Z")).windowDue(settings) {
		t.Error("the configured minute was not due")
	}
	if scheduler(at(t, "2026-09-03T03:29:00Z")).windowDue(settings) {
		t.Error("a minute before the window was due")
	}
	if scheduler(at(t, "2026-09-03T04:30:00Z")).windowDue(settings) {
		t.Error("an hour after the window was due")
	}
}

func TestTheWindowRespectsTheDay(t *testing.T) {
	settings := DefaultSettings("server-1")
	settings.DayOfWeek = 0 // Sunday
	settings.Hour = 3
	settings.Minute = 0

	// 2026-09-06 is a Sunday; 2026-09-03 is a Thursday.
	if !scheduler(at(t, "2026-09-06T03:00:00Z")).windowDue(settings) {
		t.Error("Sunday at 03:00 was not due for a Sunday schedule")
	}
	if scheduler(at(t, "2026-09-03T03:00:00Z")).windowDue(settings) {
		t.Error("Thursday was due for a Sunday schedule")
	}
}

func TestTheWindowDoesNotFireTwice(t *testing.T) {
	// The loop wakes every minute and the window is a minute wide. Without
	// this, an upgrade that took ninety seconds would start a second one.
	now := at(t, "2026-09-03T03:00:00Z")
	settings := DefaultSettings("server-1")
	settings.DayOfWeek = validate.EveryDay
	settings.Hour = 3
	settings.Minute = 0

	justRan := now.Add(-90 * time.Second)
	settings.LastRunAt = &justRan
	if scheduler(now).windowDue(settings) {
		t.Error("a window fired again ninety seconds after it ran")
	}

	yesterday := now.Add(-24 * time.Hour)
	settings.LastRunAt = &yesterday
	if !scheduler(now).windowDue(settings) {
		t.Error("a window that ran yesterday was not due today")
	}
}

func TestDefaultsAreOff(t *testing.T) {
	// Applying updates restarts daemons. An operator who has not asked for that
	// should not discover it from their monitoring at three in the morning.
	settings := DefaultSettings("server-1")
	if settings.Policy != validate.UpdatesOff {
		t.Errorf("policy = %q, want off", settings.Policy)
	}
	if settings.DayOfWeek != validate.EveryDay {
		t.Errorf("day = %d, want every day", settings.DayOfWeek)
	}
}

func TestExcludeRemovesOnlyWhatWasNamed(t *testing.T) {
	kept := exclude([]string{"nginx", "php84-fpm", "openssl"}, []string{"php84-fpm"})
	if len(kept) != 2 || kept[0] != "nginx" || kept[1] != "openssl" {
		t.Fatalf("kept = %v", kept)
	}
	// An empty exclusion list must not be mistaken for "exclude everything".
	if got := exclude([]string{"nginx"}, nil); len(got) != 1 {
		t.Errorf("an empty exclusion list removed something: %v", got)
	}
}

func TestTrimOutputKeepsBothEnds(t *testing.T) {
	// What went wrong is at the end and what was attempted is at the start;
	// the middle of an "upgrade everything" transcript is the part nobody reads.
	head := strings.Repeat("A", 40<<10)
	tail := strings.Repeat("Z", 40<<10)
	trimmed := TrimOutput(head + tail)

	if len(trimmed) >= len(head+tail) {
		t.Fatalf("nothing was trimmed: %d bytes", len(trimmed))
	}
	if !strings.HasPrefix(trimmed, "AAAA") {
		t.Error("the start of the transcript was lost")
	}
	if !strings.HasSuffix(trimmed, "ZZZZ") {
		t.Error("the end of the transcript was lost")
	}
	if !strings.Contains(trimmed, "bytes omitted") {
		t.Error("the trim was silent")
	}
	// A short transcript is left exactly as it was.
	if got := TrimOutput("short"); got != "short" {
		t.Errorf("a short transcript was changed: %q", got)
	}
}

func TestIsExpectedCoversTheOrdinaryNights(t *testing.T) {
	// All four are states a healthy host reaches routinely. Logging them as
	// errors would make a log nobody reads, which is how a real failure goes
	// unnoticed.
	for _, err := range []error{ErrNothingToDo, ErrSecurityUnknown, ErrExcluded, ErrUnavailable} {
		if !isExpected(err) {
			t.Errorf("%v was treated as a failure", err)
		}
	}
	if isExpected(ErrAlreadyRunning) {
		t.Error("an update already running was treated as an ordinary night")
	}
}
