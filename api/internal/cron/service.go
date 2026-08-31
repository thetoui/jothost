package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/shared/validate"
)

// Audit actions. Every one of these changes what will run on the host without
// anybody watching, which is exactly the kind of change an audit trail exists
// for.
const (
	ActionCreate       = "cron.create"
	ActionUpdate       = "cron.update"
	ActionDelete       = "cron.delete"
	ActionRun          = "cron.run"
	ResourceTypeCron   = "cron_job"
	ResourceTypeServer = "server"
)

// Errors returned by the service.
var (
	// ErrUnavailable means the host cannot schedule anything.
	ErrUnavailable = errors.New("this host has no cron daemon")
	// ErrWebsiteRequired means the request named no website, and a job with no
	// website has no account to run as.
	ErrWebsiteRequired = errors.New("a scheduled job belongs to a website")
	// ErrNoAccount means the website has no system account yet.
	ErrNoAccount = errors.New("this website has no system account to run jobs as")
	// ErrNoPHP means the site's PHP version has no command-line interpreter.
	ErrNoPHP = errors.New("this host cannot run a PHP script from a schedule")
	// ErrInvalidTarget means the script path or URL is not usable.
	ErrInvalidTarget = errors.New("invalid job target")
)

// Website is what the service needs to know about the site a job belongs to.
//
// An interface over the websites repository rather than the repository itself:
// this package needs three fields, and depending on the whole thing would make
// the two packages impossible to change independently.
type Websites interface {
	// LookupForCron returns the site's domain, system account, document root
	// and PHP version.
	LookupForCron(ctx context.Context, id string) (WebsiteRef, error)
}

// WebsiteRef is the site a job runs for.
type WebsiteRef struct {
	ID           string
	ServerID     string
	Domain       string
	SystemUser   string
	DocumentRoot string
	// PHPVersion is empty on a site that serves no PHP.
	PHPVersion string
}

// Service manages scheduled jobs.
type Service struct {
	repo     *Repository
	websites Websites
	agent    *agentclient.Client
	audit    *audit.Recorder
	log      *slog.Logger
	serverID string
}

// ServiceOptions configure a Service.
type ServiceOptions struct {
	Repo     *Repository
	Websites Websites
	Agent    *agentclient.Client
	Audit    *audit.Recorder
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
		websites: opts.Websites,
		agent:    opts.Agent,
		audit:    opts.Audit,
		log:      log,
		serverID: opts.ServerID,
	}
}

// Actor identifies who asked, for the audit trail.
type Actor struct {
	UserID    string
	IPAddress string
	UserAgent string
}

// Input is a create or update request, already decoded.
type Input struct {
	WebsiteID string
	Name      string
	Type      string
	Schedule  string
	// Target is a script path, a URL, or a command line, depending on Type.
	Target  string
	Enabled *bool
}

// List returns the host's jobs, with their next run times filled in.
func (s *Service) List(ctx context.Context) ([]Job, error) {
	jobs, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	return withNextRun(jobs), nil
}

// ListForWebsite returns one site's jobs.
func (s *Service) ListForWebsite(ctx context.Context, websiteID string) ([]Job, error) {
	jobs, err := s.repo.ListForWebsite(ctx, websiteID)
	if err != nil {
		return nil, err
	}
	return withNextRun(jobs), nil
}

// Get returns one job.
func (s *Service) Get(ctx context.Context, id string) (Job, error) {
	job, err := s.repo.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	return withNextRun([]Job{job})[0], nil
}

// Create records a job and writes it to the host.
func (s *Service) Create(ctx context.Context, requestID string, input Input, actor Actor) (Job, error) {
	site, command, err := s.prepare(ctx, requestID, input)
	if err != nil {
		return Job{}, err
	}

	schedule, err := validate.Schedule(input.Schedule)
	if err != nil {
		return Job{}, err
	}

	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}

	job, err := s.repo.Create(ctx, CreateParams{
		ServerID:  site.ServerID,
		WebsiteID: site.ID,
		Name:      strings.TrimSpace(input.Name),
		Type:      input.Type,
		Schedule:  schedule,
		Target:    strings.TrimSpace(input.Target),
		Command:   command,
		Enabled:   enabled,
	})
	if err != nil {
		return Job{}, err
	}

	// The host is written after the record exists, and a failure there rolls
	// the record back: a job the panel lists but the host has never heard of is
	// worse than a create that failed, because nobody goes looking for it.
	if err := s.sync(ctx, requestID, site.ServerID, site.SystemUser); err != nil {
		if delErr := s.repo.Delete(ctx, job.ID); delErr != nil {
			s.log.Error("a job could not be written to the host and its record could not be removed",
				"job", job.ID, "error", delErr.Error())
		}
		return Job{}, err
	}

	s.record(ctx, actor, ActionCreate, job.ID, audit.StatusSuccess, map[string]any{
		"name": job.Name, "schedule": job.Schedule, "website": site.Domain,
		"account": site.SystemUser, "type": job.Type,
	})
	return withNextRun([]Job{job})[0], nil
}

// Update changes a job and rewrites the host's crontab.
func (s *Service) Update(ctx context.Context, requestID, id string, input Input, actor Actor) (Job, error) {
	existing, err := s.repo.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}

	site, err := s.websites.LookupForCron(ctx, existing.WebsiteID)
	if err != nil {
		return Job{}, err
	}

	params := UpdateParams{Enabled: input.Enabled}
	if name := strings.TrimSpace(input.Name); name != "" {
		if err := validate.JobName(name); err != nil {
			return Job{}, err
		}
		params.Name = &name
	}
	if input.Schedule != "" {
		schedule, err := validate.Schedule(input.Schedule)
		if err != nil {
			return Job{}, err
		}
		params.Schedule = &schedule
	}
	if target := strings.TrimSpace(input.Target); target != "" {
		// The type cannot change — a PHP job and a URL job are different
		// things — so the command is rebuilt with the type the job already has.
		command, err := s.buildCommand(ctx, requestID, existing.Type, target, site)
		if err != nil {
			return Job{}, err
		}
		params.Target = &target
		params.Command = &command
	}

	job, err := s.repo.Update(ctx, id, params)
	if err != nil {
		return Job{}, err
	}

	if err := s.sync(ctx, requestID, job.ServerID, site.SystemUser); err != nil {
		return Job{}, err
	}

	s.record(ctx, actor, ActionUpdate, job.ID, audit.StatusSuccess, map[string]any{
		"name": job.Name, "schedule": job.Schedule, "enabled": job.Enabled,
	})
	return withNextRun([]Job{job})[0], nil
}

// Delete removes a job from the panel and from the host.
func (s *Service) Delete(ctx context.Context, requestID, id string, actor Actor) error {
	job, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}

	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}

	// The crontab is rewritten from what is left, so the deleted job vanishes
	// by not being in the set.
	if err := s.sync(ctx, requestID, job.ServerID, job.SystemUser); err != nil {
		s.record(ctx, actor, ActionDelete, id, audit.StatusFailure, map[string]any{
			"name": job.Name, "reason": agentclient.Message(err),
		})
		return err
	}

	// The job's collected output goes with it. A log nothing in the panel can
	// name would sit in the log viewer for ever.
	if err := s.agent.CronLogRemove(ctx, requestID, id); err != nil {
		s.log.Warn("a deleted job's log could not be removed",
			"job", id, "error", err.Error())
	}

	s.record(ctx, actor, ActionDelete, id, audit.StatusSuccess, map[string]any{
		"name": job.Name, "website": job.WebsiteDomain,
	})
	return nil
}

// Run executes a job now and records what happened.
func (s *Service) Run(ctx context.Context, requestID, id string, actor Actor) (agentclient.CronRunResult, error) {
	job, err := s.repo.Get(ctx, id)
	if err != nil {
		return agentclient.CronRunResult{}, err
	}
	if job.SystemUser == "" {
		return agentclient.CronRunResult{}, ErrNoAccount
	}

	result, err := s.agent.CronRun(ctx, requestID, job.SystemUser, agentclient.CronJob{
		ID:       job.ID,
		Name:     job.Name,
		Schedule: job.Schedule,
		Command:  job.Command,
		Enabled:  job.Enabled,
	})
	if err != nil {
		s.record(ctx, actor, ActionRun, id, audit.StatusFailure, map[string]any{
			"name": job.Name, "reason": agentclient.Message(err),
		})
		return agentclient.CronRunResult{}, err
	}

	if err := s.repo.RecordRun(ctx, id, result.Status, result.ExitCode, result.DurationMS); err != nil {
		s.log.Warn("a job ran but its outcome could not be recorded",
			"job", id, "error", err.Error())
	}

	s.record(ctx, actor, ActionRun, id, audit.StatusSuccess, map[string]any{
		"name": job.Name, "exit_code": result.ExitCode, "outcome": result.Status,
	})
	return result, nil
}

// RemoveForWebsite drops every job a website had, on the host and in the panel.
//
// Called when a site is deleted. The rows go with the site through the
// foreign key; this is what stops the entries running until the account is
// removed underneath them.
func (s *Service) RemoveForWebsite(ctx context.Context, requestID, serverID, account string) error {
	if account == "" {
		return nil
	}
	return s.agent.CronRemove(ctx, requestID, account)
}

// prepare validates an input and builds the command it becomes.
func (s *Service) prepare(ctx context.Context, requestID string, input Input) (WebsiteRef, string, error) {
	if strings.TrimSpace(input.WebsiteID) == "" {
		return WebsiteRef{}, "", ErrWebsiteRequired
	}
	if err := validate.JobName(input.Name); err != nil {
		return WebsiteRef{}, "", err
	}
	if err := validate.JobType(input.Type); err != nil {
		return WebsiteRef{}, "", err
	}

	site, err := s.websites.LookupForCron(ctx, input.WebsiteID)
	if err != nil {
		return WebsiteRef{}, "", err
	}
	if site.SystemUser == "" {
		return WebsiteRef{}, "", ErrNoAccount
	}

	command, err := s.buildCommand(ctx, requestID, input.Type, strings.TrimSpace(input.Target), site)
	if err != nil {
		return WebsiteRef{}, "", err
	}
	return site, command, nil
}

// buildCommand turns what the operator entered into the command line that will
// be written into the crontab.
//
// Two of the three types are built here from a validated part, which is the
// point of having types at all: a PHP job is an interpreter the panel chose and
// a path inside the site's own directory, and a URL job is a fetch of an
// address that has been parsed. Neither can become anything else, whatever is
// typed into the field. The third is a command line, and is treated as one.
func (s *Service) buildCommand(ctx context.Context, requestID, jobType, target string,
	site WebsiteRef,
) (string, error) {
	if target == "" {
		return "", fmt.Errorf("%w: a target is required", ErrInvalidTarget)
	}

	switch jobType {
	case validate.JobPHP:
		script, err := scriptPath(site.DocumentRoot, target)
		if err != nil {
			return "", err
		}
		binary, err := s.phpBinary(ctx, requestID, site.PHPVersion)
		if err != nil {
			return "", err
		}
		// No shell involved in what this becomes: an interpreter path the panel
		// resolved and a file path inside the site's own document root.
		return binary + " " + script, nil

	case validate.JobURL:
		address, err := fetchURL(target)
		if err != nil {
			return "", err
		}
		// -f so an HTTP error is a failed job rather than a success that saved
		// an error page; -sS so the log holds the error and not a progress
		// meter; --max-time so a hung endpoint cannot leave curl running until
		// the next run starts.
		return "curl -fsS --max-time 300 " + address, nil

	case validate.JobCommand:
		if err := validate.Command(target); err != nil {
			return "", err
		}
		return target, nil

	default:
		return "", fmt.Errorf("%w: %q", validate.ErrInvalidJobType, jobType)
	}
}

// scriptPath resolves a script inside the site's document root.
//
// The path is relative and stays relative: a job that could name an absolute
// path could name /etc/shadow, and while the account could not read it, the
// panel should not be the thing that offered to try.
func scriptPath(documentRoot, target string) (string, error) {
	if documentRoot == "" {
		return "", fmt.Errorf("%w: this website has no document root", ErrInvalidTarget)
	}
	if strings.HasPrefix(target, "/") {
		return "", fmt.Errorf("%w: the script path is relative to the site's directory",
			ErrInvalidTarget)
	}
	if strings.ContainsAny(target, "\n\r\x00%") {
		return "", fmt.Errorf("%w: the script path contains a character cron cannot carry",
			ErrInvalidTarget)
	}
	// Spaces would need quoting in the crontab line, and a quoted path is one
	// more thing that can be wrong for no benefit: a PHP script called
	// "my cron.php" can be renamed.
	if strings.ContainsAny(target, " \t\"'\\$`|&;<>()*?[]") {
		return "", fmt.Errorf("%w: a script path may not contain spaces or shell characters",
			ErrInvalidTarget)
	}

	// Refused rather than normalised. Anchoring the path with Clean("/"+target)
	// would keep "../../../etc/passwd.php" inside the document root — it would
	// become <root>/etc/passwd.php — so nothing escapes either way. But an
	// operator who typed that gets a job pointing somewhere they did not name,
	// failing later with a message about a file they never asked for. Saying no
	// is both safer and clearer.
	for _, segment := range strings.Split(target, "/") {
		if segment == ".." {
			return "", fmt.Errorf("%w: the script path may not contain \"..\"",
				ErrInvalidTarget)
		}
	}

	cleaned := path.Clean("/" + target)
	if !strings.HasSuffix(strings.ToLower(cleaned), ".php") {
		return "", fmt.Errorf("%w: a PHP job runs a .php file", ErrInvalidTarget)
	}
	return path.Join(documentRoot, cleaned), nil
}

// fetchURL checks the address a URL job will fetch.
func fetchURL(target string) (string, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return "", fmt.Errorf("%w: that is not a URL", ErrInvalidTarget)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("%w: a URL job fetches http or https", ErrInvalidTarget)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("%w: the URL has no host", ErrInvalidTarget)
	}
	// The address goes into a crontab line unquoted, so anything a shell would
	// read as syntax is refused rather than escaped. A URL that needs those
	// characters can be shortened or given to a command job deliberately.
	if strings.ContainsAny(target, " \t\"'\\$`|&;<>()\n\r%") {
		return "", fmt.Errorf("%w: the URL contains a character that is not safe unquoted",
			ErrInvalidTarget)
	}
	return target, nil
}

// phpBinary resolves the command-line interpreter for a site's PHP version.
func (s *Service) phpBinary(ctx context.Context, requestID, version string) (string, error) {
	if version == "" {
		return "", fmt.Errorf("%w: this website has no PHP version set", ErrNoPHP)
	}

	versions, err := s.agent.PHPVersions(ctx, requestID)
	if err != nil {
		return "", err
	}
	for _, installed := range versions.Versions {
		if installed.Version != version {
			continue
		}
		if installed.CLIPath == "" {
			return "", fmt.Errorf(
				"%w: PHP %s is installed for the web server but has no command-line interpreter",
				ErrNoPHP, version)
		}
		return installed.CLIPath, nil
	}
	return "", fmt.Errorf("%w: PHP %s is not installed on this host", ErrNoPHP, version)
}

// sync hands the Agent the complete set of jobs for one account.
func (s *Service) sync(ctx context.Context, requestID, serverID, account string) error {
	jobs, err := s.repo.ListForAccount(ctx, serverID, account)
	if err != nil {
		return err
	}

	payload := make([]agentclient.CronJob, 0, len(jobs))
	for _, job := range jobs {
		payload = append(payload, agentclient.CronJob{
			ID:       job.ID,
			Name:     job.Name,
			Schedule: job.Schedule,
			Command:  job.Command,
			Enabled:  job.Enabled,
		})
	}

	return s.agent.CronApply(ctx, requestID, account, payload)
}

// withNextRun fills in when each job will next fire.
func withNextRun(jobs []Job) []Job {
	now := time.Now().UTC()
	for i := range jobs {
		if !jobs[i].Enabled {
			// A disabled job has no next run, and showing one would say it is
			// about to happen.
			continue
		}
		if next, ok := validate.NextRun(jobs[i].Schedule, now); ok {
			at := next
			jobs[i].NextRunAt = &at
		}
	}
	return jobs
}

// record writes an audit event, logging rather than failing the request.
func (s *Service) record(ctx context.Context, actor Actor, action, jobID, status string,
	metadata map[string]any,
) {
	if s.audit == nil {
		return
	}
	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       actor.UserID,
		Action:       action,
		ResourceType: ResourceTypeCron,
		ResourceID:   jobID,
		IPAddress:    actor.IPAddress,
		UserAgent:    actor.UserAgent,
		Status:       status,
		Metadata:     metadata,
	})
}
