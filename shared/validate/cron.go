package validate

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Errors returned by cron validation.
var (
	ErrInvalidSchedule = errors.New("invalid schedule")
	ErrInvalidCommand  = errors.New("invalid command")
	ErrInvalidJobName  = errors.New("invalid job name")
	ErrInvalidJobType  = errors.New("invalid job type")
)

// The kinds of scheduled job the panel offers.
//
// Three rather than one, because two of the three are the cases people actually
// want and neither needs a command at all: the panel builds the command line
// itself from a script path or a URL. What is left in JobCommand is genuinely
// free-form, and being a separate type means it is an explicit choice rather
// than the only way to schedule anything.
const (
	// JobPHP runs a PHP script under the website's document root, with the
	// PHP version that website uses.
	JobPHP = "php"
	// JobURL fetches a URL. The web-application idiom: the work is a route in
	// the application, and cron just asks for it.
	JobURL = "url"
	// JobCommand runs a command line through the account's shell, exactly as
	// crond would run a hand-written crontab entry.
	JobCommand = "command"
)

// ErrInvalidUUID means an identifier is not one.
var ErrInvalidUUID = errors.New("invalid identifier")

// UUID checks an identifier that came from the panel's database.
//
// It exists because such an identifier becomes a filename — a job's log is
// named after it — and "it came from our own database" is a claim the Agent
// cannot verify. Checking the shape is cheap; trusting the caller about a value
// that will be joined to a path is not.
func UUID(value string) error {
	if len(value) != 36 {
		return fmt.Errorf("%w: %q", ErrInvalidUUID, value)
	}
	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return fmt.Errorf("%w: %q", ErrInvalidUUID, value)
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return fmt.Errorf("%w: %q", ErrInvalidUUID, value)
			}
		}
	}
	return nil
}

// JobTypes returns the job types, for a caller offering a choice.
func JobTypes() []string { return []string{JobPHP, JobURL, JobCommand} }

// JobType checks a job type.
func JobType(value string) error {
	switch value {
	case JobPHP, JobURL, JobCommand:
		return nil
	}
	return fmt.Errorf("%w: %q", ErrInvalidJobType, value)
}

// Bounds.
const (
	// MaxCommandLength bounds a command. Long enough for a real command line
	// with arguments, short enough that a crontab file cannot be filled from
	// the panel.
	MaxCommandLength = 500
	// MaxJobNameLength bounds the name an operator gives a job.
	MaxJobNameLength = 60
	// MaxScheduleLength bounds the expression.
	MaxScheduleLength = 100
)

// JobName checks the label an operator gives a job.
func JobName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("%w: a name is required", ErrInvalidJobName)
	}
	if len(trimmed) > MaxJobNameLength {
		return fmt.Errorf("%w: at most %d characters", ErrInvalidJobName, MaxJobNameLength)
	}
	// The name is shown in the panel and written into the crontab file as a
	// comment above the entry, so a line break in it would end the comment and
	// make the rest of the name a crontab line of its own.
	if strings.ContainsAny(trimmed, "\n\r\x00") {
		return fmt.Errorf("%w: a name cannot contain line breaks", ErrInvalidJobName)
	}
	return nil
}

// Command checks a command line before it becomes a crontab entry.
//
// This is the validation that matters most in this phase, and what it defends
// against is not what the command *does* — crond runs it as the website's own
// unprivileged account, which is exactly the authority that account already has
// over its own files — but what the command could do to the *crontab file*.
//
// A crontab is a line-oriented format with no quoting. A command containing a
// newline is not a long command: it is two entries, and the second one is
// whatever the caller wants on whatever schedule they wrote. That is the whole
// attack, and it is why this refuses rather than escapes.
func Command(command string) error {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return fmt.Errorf("%w: a command is required", ErrInvalidCommand)
	}
	if len(trimmed) > MaxCommandLength {
		return fmt.Errorf("%w: at most %d characters", ErrInvalidCommand, MaxCommandLength)
	}
	if strings.ContainsAny(trimmed, "\n\r") {
		return fmt.Errorf("%w: a command must be a single line", ErrInvalidCommand)
	}
	if strings.ContainsRune(trimmed, 0) {
		return fmt.Errorf("%w: a command cannot contain a null byte", ErrInvalidCommand)
	}
	// Cron's own escape. An unescaped % ends the command and everything after
	// it becomes the command's standard input, with each further % becoming a
	// newline — so "%" is a line break wearing a disguise, and the most common
	// way a working command line stops working when it is put into a crontab.
	if strings.ContainsRune(trimmed, '%') {
		return fmt.Errorf("%w: %% has a special meaning to cron and is not allowed; "+
			"put the command in a script if you need it", ErrInvalidCommand)
	}
	return nil
}

// Schedule checks a cron expression and returns it normalised.
//
// Five fields, or one of the @-shorthands. Seconds are deliberately not
// accepted: a six-field expression means different things to different cron
// implementations, and a job that runs sixty times more often than intended is
// not a mistake a panel should make possible.
func Schedule(expression string) (string, error) {
	trimmed := strings.TrimSpace(expression)
	if trimmed == "" {
		return "", fmt.Errorf("%w: a schedule is required", ErrInvalidSchedule)
	}
	if len(trimmed) > MaxScheduleLength {
		return "", fmt.Errorf("%w: at most %d characters", ErrInvalidSchedule, MaxScheduleLength)
	}
	if strings.ContainsAny(trimmed, "\n\r\x00%") {
		return "", fmt.Errorf("%w: a schedule must be a single line", ErrInvalidSchedule)
	}

	if strings.HasPrefix(trimmed, "@") {
		expanded, ok := shorthands[strings.ToLower(trimmed)]
		if !ok {
			return "", fmt.Errorf("%w: %q is not a schedule this panel understands",
				ErrInvalidSchedule, trimmed)
		}
		return expanded, nil
	}

	fields := strings.Fields(trimmed)
	if len(fields) != 5 {
		return "", fmt.Errorf(
			"%w: five fields are required — minute, hour, day of month, month, day of week",
			ErrInvalidSchedule)
	}

	for i, field := range fields {
		if err := checkField(field, cronFields[i]); err != nil {
			return "", fmt.Errorf("%w: %s: %s", ErrInvalidSchedule, cronFields[i].label, err)
		}
	}
	return strings.Join(fields, " "), nil
}

// shorthands are the @-forms, expanded to their five-field equivalents.
//
// Expanded rather than passed through, so that everything downstream — the
// crontab writer, the next-run calculation, the description shown to an
// operator — deals with one representation. @reboot has no five-field
// equivalent and is deliberately absent: it fires when the machine boots, which
// is not a schedule the panel can describe, and a panel that regenerates a
// crontab cannot tell an operator when it will next happen.
var shorthands = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

// field describes one position in a cron expression.
type field struct {
	label string
	min   int
	max   int
	// names are the alphabetic aliases this field accepts, lowercased.
	names map[string]int
}

var cronFields = [5]field{
	{label: "minute", min: 0, max: 59},
	{label: "hour", min: 0, max: 23},
	{label: "day of month", min: 1, max: 31},
	{label: "month", min: 1, max: 12, names: map[string]int{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}},
	// Sunday is both 0 and 7, which every cron accepts and which surprises
	// people who assume one of them is wrong.
	{label: "day of week", min: 0, max: 7, names: map[string]int{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}},
}

// checkField validates one field: a comma-separated list of ranges, each
// optionally stepped.
func checkField(value string, f field) error {
	if value == "" {
		return errors.New("is empty")
	}
	for _, part := range strings.Split(value, ",") {
		if err := checkRange(part, f); err != nil {
			return err
		}
	}
	return nil
}

func checkRange(part string, f field) error {
	body, step, hasStep := strings.Cut(part, "/")
	if hasStep {
		value, err := strconv.Atoi(step)
		if err != nil || value < 1 {
			return fmt.Errorf("%q is not a step", step)
		}
		if value > f.max {
			return fmt.Errorf("a step of %d is larger than the field allows", value)
		}
	}

	if body == "*" {
		return nil
	}

	from, to, isRange := strings.Cut(body, "-")
	if err := fieldValue(from, f); err != nil {
		return err
	}
	if !isRange {
		return nil
	}
	if err := fieldValue(to, f); err != nil {
		return err
	}

	// A range that ends before it starts matches nothing, which is a job that
	// silently never runs rather than an error anyone would notice.
	start, _ := resolve(from, f)
	end, _ := resolve(to, f)
	if end < start {
		return fmt.Errorf("%s ends before it starts", body)
	}
	return nil
}

// fieldValue resolves one number or name.
func fieldValue(value string, f field) error {
	if value == "" {
		return errors.New("is empty")
	}

	if f.names != nil {
		if _, ok := f.names[strings.ToLower(value)]; ok {
			return nil
		}
	}

	number, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("%q is not a number this field accepts", value)
	}
	if number < f.min || number > f.max {
		return fmt.Errorf("%d is outside %d–%d", number, f.min, f.max)
	}
	return nil
}

// NextRun returns the next time a schedule fires at or after `from`.
//
// Cron has no closed form, so this walks forward minute by minute — bounded to
// four years, which is longer than the longest gap any five-field expression
// can produce (29 February on a specific weekday). It exists because the most
// common cron mistake is an expression that means something other than what was
// intended, and "next run: in 4 minutes" catches that before the job does.
//
// ok is false when the expression can never fire — 31 February is a valid
// expression and a job that never runs.
func NextRun(schedule string, from time.Time) (next time.Time, ok bool) {
	normalised, err := Schedule(schedule)
	if err != nil {
		return time.Time{}, false
	}
	fields := strings.Fields(normalised)

	// Cron fires on the minute, so the search starts at the next whole one.
	candidate := from.Truncate(time.Minute).Add(time.Minute)
	limit := candidate.AddDate(4, 0, 0)

	for candidate.Before(limit) {
		if matches(candidate, fields) {
			return candidate, true
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, false
}

// matches reports whether a time satisfies a five-field expression.
func matches(at time.Time, fields []string) bool {
	if !fieldMatches(fields[0], at.Minute(), cronFields[0]) ||
		!fieldMatches(fields[1], at.Hour(), cronFields[1]) ||
		!fieldMatches(fields[3], int(at.Month()), cronFields[3]) {
		return false
	}

	// Day of month and day of week are an OR when both are restricted, which is
	// cron's oldest and least obvious rule: "0 0 13 * 5" is the 13th *or* any
	// Friday, not Friday the 13th. Panels that get this wrong silently run jobs
	// on days nobody asked for.
	domRestricted := !isUnrestricted(fields[2])
	dowRestricted := !isUnrestricted(fields[4])

	dom := fieldMatches(fields[2], at.Day(), cronFields[2])
	dow := weekdayMatches(fields[4], at.Weekday())

	switch {
	case domRestricted && dowRestricted:
		return dom || dow
	case domRestricted:
		return dom
	case dowRestricted:
		return dow
	default:
		return true
	}
}

// isUnrestricted reports whether a field matches everything.
func isUnrestricted(value string) bool {
	return value == "*" || strings.HasPrefix(value, "*/1")
}

// weekdayMatches handles Sunday being both 0 and 7.
func weekdayMatches(value string, day time.Weekday) bool {
	if fieldMatches(value, int(day), cronFields[4]) {
		return true
	}
	return day == time.Sunday && fieldMatches(value, 7, cronFields[4])
}

func fieldMatches(value string, actual int, f field) bool {
	for _, part := range strings.Split(value, ",") {
		if rangeMatches(part, actual, f) {
			return true
		}
	}
	return false
}

func rangeMatches(part string, actual int, f field) bool {
	body, stepText, hasStep := strings.Cut(part, "/")
	step := 1
	if hasStep {
		parsed, err := strconv.Atoi(stepText)
		if err != nil || parsed < 1 {
			return false
		}
		step = parsed
	}

	start, end := f.min, f.max
	if body != "*" {
		from, to, isRange := strings.Cut(body, "-")
		value, ok := resolve(from, f)
		if !ok {
			return false
		}
		start = value
		end = value
		if isRange {
			value, ok := resolve(to, f)
			if !ok {
				return false
			}
			end = value
		}
	}

	if actual < start || actual > end {
		return false
	}
	return (actual-start)%step == 0
}

// resolve turns one number or name into its value.
func resolve(value string, f field) (int, bool) {
	if f.names != nil {
		if number, ok := f.names[strings.ToLower(value)]; ok {
			return number, true
		}
	}
	number, err := strconv.Atoi(value)
	if err != nil || number < f.min || number > f.max {
		return 0, false
	}
	return number, true
}
