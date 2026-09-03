package updates

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/shared/validate"
)

// Audit actions.
//
// Applying updates restarts daemons and changes what version of software a
// customer's site runs on, which is exactly what an audit trail is for. A check
// is not audited: reading is not a change, and a six-hourly automatic check
// would drown the log it is supposed to make readable.
const (
	ActionApply     = "update.apply"
	ActionRevert    = "update.revert"
	ActionConfigure = "update.configure"

	ResourceTypeServer = "server"
	ResourceTypeRun    = "update_run"
)

// Errors returned by the service.
var (
	// ErrUnavailable means the host has no package manager the panel drives.
	ErrUnavailable = errors.New("this host has no package manager the panel can use")
	// ErrAlreadyRunning means an update is in flight. Two at once is lock
	// contention at best and a half-applied set of packages at worst.
	ErrAlreadyRunning = errors.New("an update is already running on this host")
	// ErrNothingToDo means an apply was asked for with nothing outstanding.
	ErrNothingToDo = errors.New("there is nothing to apply")
	// ErrSecurityUnknown means a security-only apply was asked of a host that
	// cannot tell security updates apart.
	ErrSecurityUnknown = errors.New(
		"this host's package manager does not mark security updates, so " +
			"there is no security-only set to apply")
	// ErrExcluded means every requested package is on the panel's exclusion
	// list.
	ErrExcluded = errors.New("every package asked for is excluded from automatic updates")
)

// Actor is who asked, for the audit trail.
type Actor struct {
	UserID    string
	IPAddress string
	UserAgent string
}

// Runtimes is what this package needs to know about the language runtimes the
// panel manages.
//
// An interface, and only two methods: the PHP and Node update views are the
// same package list filtered to the packages those phases installed, and
// depending on either package outright would make this one impossible to change
// without them.
type Runtimes interface {
	// PHPPackagePrefixes returns the package name prefixes PHP is installed
	// under on this host, such as "php84".
	PHPPackagePrefixes(ctx context.Context) []string
	// NodePackageNames returns the package names the Node.js runtime uses.
	NodePackageNames(ctx context.Context) []string
}

// Service reads and applies this host's updates.
type Service struct {
	repo     *Repository
	agent    *agentclient.Client
	audit    *audit.Recorder
	runtimes Runtimes
	log      *slog.Logger
	serverID string

	// applying guards against two upgrades at once within this process. The
	// database check catches the same thing across restarts; this catches the
	// race between two requests arriving together, which the database check
	// alone would not.
	applying sync.Mutex
}

// ServiceOptions configure a Service.
type ServiceOptions struct {
	Repo     *Repository
	Agent    *agentclient.Client
	Audit    *audit.Recorder
	Runtimes Runtimes
	Log      *slog.Logger
	ServerID string
}

// NewService builds a Service.
func NewService(opts ServiceOptions) *Service {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		repo:     opts.Repo,
		agent:    opts.Agent,
		audit:    opts.Audit,
		runtimes: opts.Runtimes,
		log:      log,
		serverID: opts.ServerID,
	}
}

// Overview is everything the updates page shows.
type Overview struct {
	// Check is the panel's cached reading. It may be old; CheckedAt says how
	// old, and Succeeded says whether it can be believed at all.
	Check Check `json:"check"`
	// HasCheck is false on a host nobody has checked yet, which is different
	// from a host with nothing outstanding.
	HasCheck bool     `json:"has_check"`
	Settings Settings `json:"settings"`
	// Running is the update in flight, if there is one.
	Running *Run `json:"running,omitempty"`
	// Runtime is the subset of the pending list belonging to the language
	// runtimes this panel manages.
	Runtime RuntimeUpdates `json:"runtime"`
	// Recent is the last few applications.
	Recent []Run `json:"recent"`
}

// RuntimeUpdates is the pending list, filtered to what the panel installed.
//
// It is a view rather than a separate check: a PHP update is a package update,
// and asking the package manager twice would be two answers that can disagree.
// What this adds is the joining — the panel knows which packages are PHP
// because Phase 5 installed them.
type RuntimeUpdates struct {
	PHP  []Package `json:"php"`
	Node []Package `json:"node"`
}

// Overview reads the whole picture.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	overview := Overview{Recent: []Run{}}

	settings, err := s.repo.Settings(ctx, s.serverID)
	if err != nil {
		return overview, err
	}
	overview.Settings = settings

	check, err := s.repo.LatestCheck(ctx, s.serverID)
	switch {
	case err == nil:
		overview.Check = check
		overview.HasCheck = true
		overview.Runtime = s.runtimeView(ctx, check)
	case errors.Is(err, ErrNoCheck):
		// Not an error: a host nobody has checked yet is an ordinary state, and
		// it is not the same as a host with nothing outstanding.
		overview.Check = Check{Packages: []Package{}, Held: []Held{}}
	default:
		return overview, err
	}

	if running, found, err := s.repo.RunningRun(ctx, s.serverID); err != nil {
		return overview, err
	} else if found {
		overview.Running = &running
	}

	runs, err := s.repo.ListRuns(ctx, s.serverID, 10)
	if err != nil {
		return overview, err
	}
	overview.Recent = runs
	return overview, nil
}

// runtimeView picks the PHP and Node packages out of a check.
func (s *Service) runtimeView(ctx context.Context, check Check) RuntimeUpdates {
	view := RuntimeUpdates{PHP: []Package{}, Node: []Package{}}
	if s.runtimes == nil {
		return view
	}

	prefixes := s.runtimes.PHPPackagePrefixes(ctx)
	nodeNames := s.runtimes.NodePackageNames(ctx)

	for _, pkg := range check.Packages {
		for _, prefix := range prefixes {
			if strings.HasPrefix(pkg.Name, prefix) {
				view.PHP = append(view.PHP, pkg)
				break
			}
		}
		for _, name := range nodeNames {
			if pkg.Name == name {
				view.Node = append(view.Node, pkg)
				break
			}
		}
	}
	return view
}

// CheckNow asks the host what it has waiting and records the answer.
//
// The answer is recorded whatever it says, including a failure: a check that
// could not reach the repositories is a fact about this host, and storing it is
// what lets the page distinguish "nothing outstanding" from "not known".
func (s *Service) CheckNow(ctx context.Context, requestID string) (Check, error) {
	report, err := s.agent.UpdatesCheck(ctx, requestID)
	if err != nil {
		if agentclient.IsUnsupported(err) {
			return Check{}, ErrUnavailable
		}
		return Check{}, err
	}

	check := Check{
		ServerID:       s.serverID,
		Manager:        report.Manager,
		Succeeded:      report.Checked,
		Reason:         report.Reason,
		SecurityKnown:  report.SecurityKnown,
		Unavailable:    report.Unavailable,
		Stale:          report.Stale,
		RebootRequired: report.RebootRequired,
		Packages:       make([]Package, 0, len(report.Packages)),
		Held:           make([]Held, 0, len(report.Held)),
		CheckedAt:      report.CheckedAt,
	}
	if check.CheckedAt.IsZero() {
		check.CheckedAt = time.Now().UTC()
	}

	for _, pkg := range report.Packages {
		check.Packages = append(check.Packages, Package{
			Name: pkg.Name, Installed: pkg.Installed,
			Available: pkg.Available, Security: pkg.Security, Origin: pkg.Origin,
		})
		if pkg.Security {
			check.SecurityCount++
		}
	}
	for _, held := range report.Held {
		check.Held = append(check.Held, Held{
			Name: held.Name, Installed: held.Installed,
			Available: held.Available, Reason: held.Reason,
		})
	}
	check.PackageCount = len(check.Packages)
	check.HeldCount = len(check.Held)

	saved, err := s.repo.SaveCheck(ctx, check)
	if err != nil {
		return Check{}, err
	}
	if err := s.repo.MarkChecked(ctx, s.serverID, saved.CheckedAt); err != nil {
		s.log.Warn("could not record when updates were checked", "error", err.Error())
	}
	// The history answers "when did this update appear", which needs a few
	// readings rather than every one the panel ever made.
	if err := s.repo.PruneChecks(ctx, s.serverID, 30); err != nil {
		s.log.Warn("could not prune the update history", "error", err.Error())
	}
	return saved, nil
}

// ApplyRequest is what to install.
type ApplyRequest struct {
	// Packages names what to apply. Empty means everything outstanding.
	Packages []string
	// SecurityOnly applies the pending security fixes and nothing else.
	SecurityOnly bool
	// Trigger says who asked, for the history.
	Trigger string
}

// Apply installs updates and records what moved.
func (s *Service) Apply(ctx context.Context, actor Actor, requestID string,
	req ApplyRequest,
) (Run, error) {
	if !s.applying.TryLock() {
		return Run{}, ErrAlreadyRunning
	}
	defer s.applying.Unlock()

	if _, found, err := s.repo.RunningRun(ctx, s.serverID); err != nil {
		return Run{}, err
	} else if found {
		return Run{}, ErrAlreadyRunning
	}

	trigger := req.Trigger
	if trigger == "" {
		trigger = TriggerManual
	}

	packages, err := s.resolve(ctx, req)
	if err != nil {
		return Run{}, err
	}

	run, err := s.repo.StartRun(ctx, s.serverID, trigger, packages, actor.UserID)
	if err != nil {
		return Run{}, err
	}

	result, applyErr := s.agent.UpdatesApply(ctx, requestID, packages)
	finished, err := s.finish(ctx, run.ID, result, applyErr)
	if err != nil {
		return Run{}, err
	}

	if err := s.repo.MarkRun(ctx, s.serverID, time.Now().UTC()); err != nil {
		s.log.Warn("could not record when updates were applied", "error", err.Error())
	}

	s.record(ctx, actor, requestID, ActionApply, ResourceTypeRun, finished.ID, map[string]any{
		"trigger":   trigger,
		"requested": packages,
		"changed":   len(finished.Changes),
		"status":    finished.Status,
	})

	// The list the panel holds describes a host that has just changed, so it is
	// refreshed rather than left showing what was outstanding a moment ago.
	if _, err := s.CheckNow(ctx, requestID); err != nil {
		s.log.Warn("could not re-check updates after applying", "error", err.Error())
	}

	if applyErr != nil {
		return finished, applyErr
	}
	return finished, nil
}

// resolve turns a request into the exact package list to hand the Agent.
func (s *Service) resolve(ctx context.Context, req ApplyRequest) ([]string, error) {
	settings, err := s.repo.Settings(ctx, s.serverID)
	if err != nil {
		return nil, err
	}

	// An explicit list is taken as given, minus anything excluded.
	if len(req.Packages) > 0 {
		for _, name := range req.Packages {
			if err := validate.PackageName(name); err != nil {
				return nil, err
			}
		}
		kept := exclude(req.Packages, settings.Excluded)
		if len(kept) == 0 {
			return nil, ErrExcluded
		}
		return kept, nil
	}

	check, err := s.repo.LatestCheck(ctx, s.serverID)
	if err != nil {
		if errors.Is(err, ErrNoCheck) {
			return nil, fmt.Errorf(
				"%w: this host has not been checked yet, so there is no list to apply",
				ErrNothingToDo)
		}
		return nil, err
	}
	if !check.Succeeded {
		return nil, fmt.Errorf(
			"%w: the last check did not work, so what is outstanding is not known — %s",
			ErrNothingToDo, check.Reason)
	}

	if req.SecurityOnly {
		if !check.SecurityKnown {
			return nil, ErrSecurityUnknown
		}
		names := make([]string, 0, len(check.Packages))
		for _, pkg := range check.Packages {
			if pkg.Security {
				names = append(names, pkg.Name)
			}
		}
		names = exclude(names, settings.Excluded)
		if len(names) == 0 {
			return nil, ErrNothingToDo
		}
		return names, nil
	}

	if len(check.Packages) == 0 {
		return nil, ErrNothingToDo
	}

	// Everything, unless the operator has excluded something — in which case
	// the list has to be named explicitly, because "upgrade everything" has no
	// way to say "except this".
	if len(settings.Excluded) == 0 {
		return []string{}, nil
	}
	names := make([]string, 0, len(check.Packages))
	for _, pkg := range check.Packages {
		names = append(names, pkg.Name)
	}
	names = exclude(names, settings.Excluded)
	if len(names) == 0 {
		return nil, ErrExcluded
	}
	return names, nil
}

// finish records how an application ended.
func (s *Service) finish(ctx context.Context, runID string,
	result agentclient.UpdateResult, applyErr error,
) (Run, error) {
	status := StatusSucceeded
	failure := ""
	if applyErr != nil {
		status = StatusFailed
		failure = agentclient.Message(applyErr)
		if failure == "" {
			failure = applyErr.Error()
		}
	}

	changes := make([]Change, 0, len(result.Changed))
	for _, change := range result.Changed {
		changes = append(changes, Change{Name: change.Name, From: change.From, To: change.To})
	}
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].Name < changes[j].Name })

	return s.repo.FinishRun(ctx, runID, status, changes, result.Output,
		failure, result.RebootRequired)
}

// Revert puts one package back to an earlier version.
//
// It is not a rollback, and the panel does not call it one. The Agent asks the
// package manager whether that exact version can still be installed and refuses
// when it cannot — which, on a host whose repositories only carry the current
// version, is the usual answer.
func (s *Service) Revert(ctx context.Context, actor Actor, requestID, name, version string) (
	Run, error,
) {
	if err := validate.PackageName(name); err != nil {
		return Run{}, err
	}
	if err := validate.PackageVersion(version); err != nil {
		return Run{}, err
	}

	if !s.applying.TryLock() {
		return Run{}, ErrAlreadyRunning
	}
	defer s.applying.Unlock()

	if _, found, err := s.repo.RunningRun(ctx, s.serverID); err != nil {
		return Run{}, err
	} else if found {
		return Run{}, ErrAlreadyRunning
	}

	run, err := s.repo.StartRun(ctx, s.serverID, TriggerRevert,
		[]string{name + " " + version}, actor.UserID)
	if err != nil {
		return Run{}, err
	}

	result, revertErr := s.agent.UpdatesRevert(ctx, requestID, name, version)
	finished, err := s.finish(ctx, run.ID, result, revertErr)
	if err != nil {
		return Run{}, err
	}

	s.record(ctx, actor, requestID, ActionRevert, ResourceTypeRun, finished.ID, map[string]any{
		"package": name,
		"version": version,
		"status":  finished.Status,
	})

	if _, err := s.CheckNow(ctx, requestID); err != nil {
		s.log.Warn("could not re-check updates after reverting", "error", err.Error())
	}

	if revertErr != nil {
		return finished, revertErr
	}
	return finished, nil
}

// Configure saves when and whether the panel applies updates by itself.
func (s *Service) Configure(ctx context.Context, actor Actor, requestID string,
	settings Settings,
) (Settings, error) {
	settings.ServerID = s.serverID

	if settings.Policy == "" {
		settings.Policy = validate.UpdatesOff
	}
	if err := validate.UpdatePolicy(settings.Policy); err != nil {
		return Settings{}, err
	}
	if settings.CheckIntervalHours == 0 {
		settings.CheckIntervalHours = 6
	}
	if settings.CheckIntervalHours < 1 || settings.CheckIntervalHours > 168 {
		return Settings{}, fmt.Errorf(
			"%w: the check interval must be between 1 hour and a week",
			validate.ErrInvalidUpdateWindow)
	}
	if err := validate.UpdateWindow(settings.DayOfWeek, settings.Hour, settings.Minute); err != nil {
		return Settings{}, err
	}
	for _, name := range settings.Excluded {
		if err := validate.PackageName(name); err != nil {
			return Settings{}, err
		}
	}

	saved, err := s.repo.SaveSettings(ctx, settings)
	if err != nil {
		return Settings{}, err
	}

	s.record(ctx, actor, requestID, ActionConfigure, ResourceTypeServer, s.serverID,
		map[string]any{
			"policy":   saved.Policy,
			"window":   fmt.Sprintf("day %d at %02d:%02d", saved.DayOfWeek, saved.Hour, saved.Minute),
			"excluded": saved.Excluded,
		})
	return saved, nil
}

// History returns a host's update runs.
func (s *Service) History(ctx context.Context, limit int) ([]Run, error) {
	return s.repo.ListRuns(ctx, s.serverID, limit)
}

// record writes an audit event.
func (s *Service) record(ctx context.Context, actor Actor, requestID, action,
	resourceType, resourceID string, details map[string]any,
) {
	if s.audit == nil {
		return
	}
	if details == nil {
		details = map[string]any{}
	}
	details["request_id"] = requestID

	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       actor.UserID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		IPAddress:    actor.IPAddress,
		UserAgent:    actor.UserAgent,
		Status:       "success",
		Metadata:     details,
	})
}

// exclude removes the named packages from a list.
func exclude(names, excluded []string) []string {
	if len(excluded) == 0 {
		return names
	}
	skip := make(map[string]bool, len(excluded))
	for _, name := range excluded {
		skip[name] = true
	}
	kept := make([]string, 0, len(names))
	for _, name := range names {
		if !skip[name] {
			kept = append(kept, name)
		}
	}
	return kept
}
