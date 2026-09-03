// Package updates is the panel's record of what this host has to install.
//
// Unlike most of this panel, the rows here are not intent. The host's package
// manager is the authority on what is outstanding, and these are a *cache* of
// what it last said — kept because a check refreshes the package index and
// reaches the network, so asking on every page load would make the page slow
// and hammer a distribution's mirrors from every panel in existence.
//
// The one field that carries more weight than the rest is Succeeded. A check
// that could not reach the repositories produces an empty package list which is
// indistinguishable from a host with nothing to do, and "up to date" is the
// sentence an operator reads to decide they are safe. A failed check is stored
// as a failed check, and the page says "not known" rather than "none".
package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Errors returned by the repository.
var (
	// ErrNoCheck means this host has never been checked.
	ErrNoCheck = errors.New("this host has not been checked for updates yet")
	// ErrRunNotFound means no such update run.
	ErrRunNotFound = errors.New("update run not found")
)

// Package is one update the host has waiting.
type Package struct {
	Name      string `json:"name"`
	Installed string `json:"installed"`
	Available string `json:"available"`
	Security  bool   `json:"security"`
	Origin    string `json:"origin,omitempty"`
}

// Held is a package the host will not upgrade.
type Held struct {
	Name      string `json:"name"`
	Installed string `json:"installed"`
	Available string `json:"available"`
	Reason    string `json:"reason"`
}

// Change is one package an update moved.
type Change struct {
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

// Check is one reading of what the host has waiting.
type Check struct {
	ID       string `json:"id"`
	ServerID string `json:"server_id"`
	Manager  string `json:"manager"`
	// Succeeded reports whether the check actually worked. False means the
	// lists below are unknown, not empty.
	Succeeded bool   `json:"succeeded"`
	Reason    string `json:"reason,omitempty"`
	// SecurityKnown reports whether this host can distinguish security updates
	// at all. When it is false, SecurityCount is not "none" — it is "cannot
	// tell".
	SecurityKnown bool `json:"security_known"`

	PackageCount  int `json:"package_count"`
	SecurityCount int `json:"security_count"`
	HeldCount     int `json:"held_count"`

	Unavailable int `json:"unavailable_repositories"`
	Stale       int `json:"stale_repositories"`

	RebootRequired bool `json:"reboot_required"`

	Packages []Package `json:"packages"`
	Held     []Held    `json:"held"`

	CheckedAt time.Time `json:"checked_at"`
}

// Run is one application of updates.
type Run struct {
	ID       string `json:"id"`
	ServerID string `json:"server_id"`
	// Trigger is "manual", "scheduled" or "revert".
	Trigger string `json:"trigger"`
	Status  string `json:"status"`

	Requested []string `json:"requested"`
	// Changes is what actually moved, which is routinely more than was asked
	// for: a package manager resolves dependencies.
	Changes []Change `json:"changes"`

	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`

	RebootRequired bool `json:"reboot_required"`

	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	RequestedBy *string    `json:"requested_by,omitempty"`
}

// Run triggers and statuses.
const (
	TriggerManual    = "manual"
	TriggerScheduled = "scheduled"
	TriggerRevert    = "revert"

	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// Settings are when and whether the panel applies updates by itself.
type Settings struct {
	ServerID string `json:"server_id"`
	// Policy is "off", "security" or "all".
	Policy string `json:"policy"`
	// CheckIntervalHours is how often the panel looks.
	CheckIntervalHours int `json:"check_interval_hours"`
	// The window automatic updates run in.
	DayOfWeek int `json:"day_of_week"`
	Hour      int `json:"hour"`
	Minute    int `json:"minute"`
	// Excluded are packages the panel will never apply automatically. It is the
	// panel's own list, not a pin: the host's package manager is not told about
	// it, so nothing here changes what a person can do at a shell.
	Excluded []string `json:"excluded"`

	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
	LastRunAt     *time.Time `json:"last_run_at,omitempty"`
}

// DefaultSettings are what a host has before anybody has chosen.
//
// Off, and that is not timidity: applying updates restarts daemons, and an
// operator who has not asked for that should not discover it from their
// monitoring at three in the morning.
func DefaultSettings(serverID string) Settings {
	return Settings{
		ServerID:           serverID,
		Policy:             "off",
		CheckIntervalHours: 6,
		DayOfWeek:          -1,
		Hour:               3,
		Minute:             0,
		Excluded:           []string{},
	}
}

// Repository reads and writes update records.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// maxOutputBytes bounds a package manager's transcript before it is stored.
//
// An "upgrade everything" on a neglected host prints thousands of lines, and
// none of the useful ones are in the middle: what went wrong is at the end, and
// what was attempted is at the start. Keeping both ends is what this bound
// costs; keeping the whole thing would be a row nobody can load.
const maxOutputBytes = 64 << 10

// TrimOutput shortens a transcript, keeping both ends.
func TrimOutput(output string) string {
	if len(output) <= maxOutputBytes {
		return output
	}
	half := maxOutputBytes / 2
	return output[:half] +
		fmt.Sprintf("\n\n… %d bytes omitted …\n\n", len(output)-maxOutputBytes) +
		output[len(output)-half:]
}

// SaveCheck records one reading.
func (r *Repository) SaveCheck(ctx context.Context, check Check) (Check, error) {
	packages, err := json.Marshal(nonNilPackages(check.Packages))
	if err != nil {
		return Check{}, fmt.Errorf("encode the package list: %w", err)
	}
	held, err := json.Marshal(nonNilHeld(check.Held))
	if err != nil {
		return Check{}, fmt.Errorf("encode the held list: %w", err)
	}

	row := r.pool.QueryRow(ctx, `
		INSERT INTO update_checks
			(server_id, manager, succeeded, reason, security_known,
			 package_count, security_count, held_count,
			 unavailable_repositories, stale_repositories, reboot_required,
			 packages, held, checked_at)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		RETURNING `+checkColumns,
		check.ServerID, check.Manager, check.Succeeded, check.Reason,
		check.SecurityKnown, check.PackageCount, check.SecurityCount,
		check.HeldCount, check.Unavailable, check.Stale, check.RebootRequired,
		packages, held, check.CheckedAt)

	return scanCheck(row)
}

const checkColumns = `
	id, server_id, manager, succeeded, reason, security_known,
	package_count, security_count, held_count,
	unavailable_repositories, stale_repositories, reboot_required,
	packages, held, checked_at`

// scanCheck reads a check row.
func scanCheck(row pgx.Row) (Check, error) {
	var check Check
	var packages, held []byte

	err := row.Scan(&check.ID, &check.ServerID, &check.Manager, &check.Succeeded,
		&check.Reason, &check.SecurityKnown, &check.PackageCount,
		&check.SecurityCount, &check.HeldCount, &check.Unavailable, &check.Stale,
		&check.RebootRequired, &packages, &held, &check.CheckedAt)
	if err != nil {
		return Check{}, err
	}

	if err := json.Unmarshal(packages, &check.Packages); err != nil {
		return Check{}, fmt.Errorf("read the package list: %w", err)
	}
	if err := json.Unmarshal(held, &check.Held); err != nil {
		return Check{}, fmt.Errorf("read the held list: %w", err)
	}
	check.Packages = nonNilPackages(check.Packages)
	check.Held = nonNilHeld(check.Held)
	return check, nil
}

// LatestCheck returns the newest reading for a host.
func (r *Repository) LatestCheck(ctx context.Context, serverID string) (Check, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+checkColumns+` FROM update_checks
		 WHERE server_id = $1::uuid ORDER BY checked_at DESC LIMIT 1`, serverID)

	check, err := scanCheck(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Check{}, ErrNoCheck
		}
		return Check{}, fmt.Errorf("select the latest update check: %w", err)
	}
	return check, nil
}

// PruneChecks removes readings older than the newest few.
//
// The history exists so "when did this update appear" has an answer, not so
// every check a panel ever made is kept: a six-hourly check is fourteen hundred
// rows a year, each carrying the whole package list.
func (r *Repository) PruneChecks(ctx context.Context, serverID string, keep int) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM update_checks
		WHERE server_id = $1::uuid
		  AND id NOT IN (
			SELECT id FROM update_checks
			WHERE server_id = $1::uuid
			ORDER BY checked_at DESC
			LIMIT $2
		  )`, serverID, keep)
	if err != nil {
		return fmt.Errorf("prune update checks: %w", err)
	}
	return nil
}

const runColumns = `
	id, server_id, trigger, status, requested, changes, output, error,
	reboot_required, started_at, finished_at, requested_by::text`

// scanRun reads a run row.
func scanRun(row pgx.Row) (Run, error) {
	var run Run
	var changes []byte

	err := row.Scan(&run.ID, &run.ServerID, &run.Trigger, &run.Status,
		&run.Requested, &changes, &run.Output, &run.Error, &run.RebootRequired,
		&run.StartedAt, &run.FinishedAt, &run.RequestedBy)
	if err != nil {
		return Run{}, err
	}
	if err := json.Unmarshal(changes, &run.Changes); err != nil {
		return Run{}, fmt.Errorf("read the change list: %w", err)
	}
	if run.Changes == nil {
		run.Changes = []Change{}
	}
	if run.Requested == nil {
		run.Requested = []string{}
	}
	return run, nil
}

// StartRun records an application that is about to happen.
//
// The row is written before the work rather than after, so a panel that is
// restarted mid-upgrade leaves a run stuck in "running" — which is a true and
// useful thing to see. A row written only on success would leave no trace of
// the upgrade that took the machine down.
func (r *Repository) StartRun(ctx context.Context, serverID, trigger string,
	requested []string, requestedBy string,
) (Run, error) {
	if requested == nil {
		requested = []string{}
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO update_runs (server_id, trigger, status, requested, requested_by)
		VALUES ($1::uuid, $2, 'running', $3, NULLIF($4, '')::uuid)
		RETURNING `+runColumns,
		serverID, trigger, requested, requestedBy)

	return scanRun(row)
}

// FinishRun records how an application ended.
func (r *Repository) FinishRun(ctx context.Context, id, status string,
	changes []Change, output, failure string, rebootRequired bool,
) (Run, error) {
	if changes == nil {
		changes = []Change{}
	}
	encoded, err := json.Marshal(changes)
	if err != nil {
		return Run{}, fmt.Errorf("encode the change list: %w", err)
	}

	row := r.pool.QueryRow(ctx, `
		UPDATE update_runs
		SET status = $2, changes = $3, output = $4, error = $5,
		    reboot_required = $6, finished_at = now()
		WHERE id = $1::uuid
		RETURNING `+runColumns,
		id, status, encoded, TrimOutput(output), failure, rebootRequired)

	run, err := scanRun(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, ErrRunNotFound
		}
		return Run{}, fmt.Errorf("record the end of an update run: %w", err)
	}
	return run, nil
}

// ListRuns returns a host's update history, newest first.
func (r *Repository) ListRuns(ctx context.Context, serverID string, limit int) ([]Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+runColumns+` FROM update_runs
		 WHERE server_id = $1::uuid ORDER BY started_at DESC LIMIT $2`, serverID, limit)
	if err != nil {
		return nil, fmt.Errorf("list update runs: %w", err)
	}
	defer rows.Close()

	runs := make([]Run, 0, 16)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan an update run: %w", err)
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// GetRun returns one run.
func (r *Repository) GetRun(ctx context.Context, id string) (Run, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM update_runs WHERE id = $1::uuid`, id)
	run, err := scanRun(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, ErrRunNotFound
		}
		return Run{}, fmt.Errorf("select an update run: %w", err)
	}
	return run, nil
}

// RunningRun returns the run in flight for a host, if there is one.
//
// Two upgrades at once is a package manager lock contention at best and a
// half-applied set of packages at worst, so the service asks this before
// starting one.
func (r *Repository) RunningRun(ctx context.Context, serverID string) (Run, bool, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+runColumns+` FROM update_runs
		 WHERE server_id = $1::uuid AND status = 'running'
		 ORDER BY started_at DESC LIMIT 1`, serverID)

	run, err := scanRun(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, false, nil
		}
		return Run{}, false, fmt.Errorf("select the running update: %w", err)
	}
	return run, true, nil
}

// Settings returns a host's update settings, or the defaults.
func (r *Repository) Settings(ctx context.Context, serverID string) (Settings, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT server_id, policy, check_interval_hours, day_of_week, hour, minute,
		       excluded, last_checked_at, last_run_at
		FROM update_settings WHERE server_id = $1::uuid`, serverID)

	var settings Settings
	err := row.Scan(&settings.ServerID, &settings.Policy, &settings.CheckIntervalHours,
		&settings.DayOfWeek, &settings.Hour, &settings.Minute, &settings.Excluded,
		&settings.LastCheckedAt, &settings.LastRunAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DefaultSettings(serverID), nil
		}
		return Settings{}, fmt.Errorf("select the update settings: %w", err)
	}
	if settings.Excluded == nil {
		settings.Excluded = []string{}
	}
	return settings, nil
}

// SaveSettings writes a host's update settings.
func (r *Repository) SaveSettings(ctx context.Context, settings Settings) (Settings, error) {
	if settings.Excluded == nil {
		settings.Excluded = []string{}
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO update_settings
			(server_id, policy, check_interval_hours, day_of_week, hour, minute, excluded)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (server_id) DO UPDATE SET
			policy               = EXCLUDED.policy,
			check_interval_hours = EXCLUDED.check_interval_hours,
			day_of_week          = EXCLUDED.day_of_week,
			hour                 = EXCLUDED.hour,
			minute               = EXCLUDED.minute,
			excluded             = EXCLUDED.excluded,
			updated_at           = now()
		RETURNING server_id, policy, check_interval_hours, day_of_week, hour, minute,
		          excluded, last_checked_at, last_run_at`,
		settings.ServerID, settings.Policy, settings.CheckIntervalHours,
		settings.DayOfWeek, settings.Hour, settings.Minute, settings.Excluded)

	var saved Settings
	if err := row.Scan(&saved.ServerID, &saved.Policy, &saved.CheckIntervalHours,
		&saved.DayOfWeek, &saved.Hour, &saved.Minute, &saved.Excluded,
		&saved.LastCheckedAt, &saved.LastRunAt); err != nil {
		return Settings{}, fmt.Errorf("save the update settings: %w", err)
	}
	if saved.Excluded == nil {
		saved.Excluded = []string{}
	}
	return saved, nil
}

// MarkChecked records that the panel looked, whatever it found.
//
// Whatever it found is the point: a check that failed still means the panel
// tried, and without this the scheduler would retry a broken mirror every time
// it woke up.
func (r *Repository) MarkChecked(ctx context.Context, serverID string, at time.Time) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO update_settings (server_id, last_checked_at)
		VALUES ($1::uuid, $2)
		ON CONFLICT (server_id) DO UPDATE SET last_checked_at = $2, updated_at = now()`,
		serverID, at)
	if err != nil {
		return fmt.Errorf("record the update check: %w", err)
	}
	return nil
}

// MarkRun records that the panel applied updates.
func (r *Repository) MarkRun(ctx context.Context, serverID string, at time.Time) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO update_settings (server_id, last_run_at)
		VALUES ($1::uuid, $2)
		ON CONFLICT (server_id) DO UPDATE SET last_run_at = $2, updated_at = now()`,
		serverID, at)
	if err != nil {
		return fmt.Errorf("record the update run: %w", err)
	}
	return nil
}

func nonNilPackages(packages []Package) []Package {
	if packages == nil {
		return []Package{}
	}
	return packages
}

func nonNilHeld(held []Held) []Held {
	if held == nil {
		return []Held{}
	}
	return held
}
