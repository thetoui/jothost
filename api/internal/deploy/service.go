package deploy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/api/internal/secrets"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// JobTypeDeployRun is the queue's name for a deployment.
//
// It is the protocol operation string, because the worker dispatches by using
// the job type as the operation — one name for one thing, across the boundary.
const JobTypeDeployRun = "deploy.run"

// Audit actions.
//
// Deploying is running code on this host as a website's account, which is the
// most consequential thing in this phase and the reason every one of these is
// recorded. Turning on automatic deployment is audited separately and
// deliberately: it is the moment a person with write access to a repository
// gains the ability to run code here without touching the panel.
const (
	ActionConfigure   = "deploy.configure"
	ActionRemove      = "deploy.remove"
	ActionKeyGenerate = "deploy.key.generate"
	ActionRun         = "deploy.run"
	ActionRollback    = "deploy.rollback"
	ActionAutoEnable  = "deploy.auto.enable"
	ActionActions     = "deploy.actions"

	ResourceTypeRepository = "git_repository"
	ResourceTypeDeployment = "deployment"
)

// MinWebhookSecret is the shortest webhook secret the panel will accept.
//
// Sixteen characters. The secret is the only thing standing between an
// unauthenticated request and code running on this host, and unlike a login
// there is no rate limit and no account to lock: an attacker who has the URL
// can try signatures as fast as the network allows.
const MinWebhookSecret = 16

// Errors returned by the service.
var (
	// ErrUnavailable means this host has no git.
	ErrUnavailable = errors.New("git is not installed on this host, so nothing can be deployed")
	// ErrNoAccount means the website has no system account to deploy as.
	ErrNoAccount = errors.New("this website has no system account to deploy as")
	// ErrNoWebhook means a webhook operation was asked for on a repository
	// that has none.
	ErrNoWebhook = errors.New("this repository has no webhook configured")
	// ErrBranchIgnored means a push was for a branch this repository does not
	// deploy. It is not a failure: it is the ordinary outcome of pushing to a
	// feature branch, and it is reported as such.
	ErrBranchIgnored = errors.New("that push was for a branch this website does not deploy")
	// ErrAutoDeployOff means a verified push arrived and nothing is set to act
	// on it.
	ErrAutoDeployOff = errors.New("automatic deployment is turned off for this website")
)

// Actor is who asked, for the audit trail.
type Actor struct {
	UserID    string
	IPAddress string
	UserAgent string
}

// Websites is what this package needs to know about a site.
type Websites interface {
	LookupForDeploy(ctx context.Context, id string) (WebsiteRef, error)
}

// WebsiteRef is the site a repository deploys into.
type WebsiteRef struct {
	ID       string
	ServerID string
	Domain   string
	// SystemUser is the account everything runs as. A repository whose website
	// has none cannot be deployed, and the panel says so rather than deploying
	// as whoever the Agent is — which is root.
	SystemUser   string
	DocumentRoot string
}

// Service manages deployments.
type Service struct {
	repo     *Repository
	websites Websites
	agent    *agentclient.Client
	jobs     *jobs.Repository
	secrets  *secrets.Encrypter
	audit    *audit.Recorder
	log      *slog.Logger
	serverID string
}

// ServiceOptions configure a Service.
type ServiceOptions struct {
	Repo     *Repository
	Websites Websites
	Agent    *agentclient.Client
	Jobs     *jobs.Repository
	Secrets  *secrets.Encrypter
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
		jobs:     opts.Jobs,
		secrets:  opts.Secrets,
		audit:    opts.Audit,
		log:      log,
		serverID: opts.ServerID,
	}
}

// RepositoryView is a repository together with what the host actually has.
type RepositoryView struct {
	GitRepository
	// Website names the site, so a list does not have to be joined.
	Website string `json:"website"`
	// Actions are the steps, in order.
	Actions []Action `json:"actions"`
	// Status is the host's own view: what is checked out, whether the tree is
	// dirty, which build tools exist. The panel's record and the host
	// disagreeing is exactly what this is for.
	Status map[string]any `json:"status,omitempty"`
	// Recent is the last few deployments.
	Recent []Deployment `json:"recent,omitempty"`
	// WebhookURL is the address to paste into a forge, built from the panel's
	// own base URL. Empty when the panel does not know its own address, which
	// is reported rather than guessed.
	WebhookURL string `json:"webhook_url,omitempty"`
}

// Overview reads every repository on the host.
func (s *Service) Overview(ctx context.Context, requestID string) ([]RepositoryView, error) {
	repositories, err := s.repo.ListRepositories(ctx, s.serverID)
	if err != nil {
		return nil, err
	}

	views := make([]RepositoryView, 0, len(repositories))
	for _, repository := range repositories {
		view := RepositoryView{GitRepository: repository}
		if site, err := s.websites.LookupForDeploy(ctx, repository.WebsiteID); err == nil {
			view.Website = site.Domain
		}
		if actions, err := s.repo.ListActions(ctx, repository.ID); err == nil {
			view.Actions = actions
		}
		if recent, err := s.repo.ListDeployments(ctx, repository.ID, 5); err == nil {
			view.Recent = stripLogs(recent)
		}
		view.WebhookURL = s.webhookURL(repository.WebhookToken)
		views = append(views, view)
	}
	return views, nil
}

// Detail reads one repository, with what the host says about it.
func (s *Service) Detail(ctx context.Context, requestID, id string) (RepositoryView, error) {
	repository, err := s.repo.GetRepository(ctx, id)
	if err != nil {
		return RepositoryView{}, err
	}

	view := RepositoryView{GitRepository: repository}
	site, err := s.websites.LookupForDeploy(ctx, repository.WebsiteID)
	if err == nil {
		view.Website = site.Domain
		view.Status = s.hostStatus(ctx, requestID, site)
	}
	if actions, err := s.repo.ListActions(ctx, id); err == nil {
		view.Actions = actions
	}
	if recent, err := s.repo.ListDeployments(ctx, id, 25); err == nil {
		view.Recent = stripLogs(recent)
	}
	view.WebhookURL = s.webhookURL(repository.WebhookToken)
	return view, nil
}

// stripLogs removes the log body from a list.
//
// A build prints whatever the build printed, which regularly includes a token
// in a URL or an environment variable a script echoed. The list says whether
// each deployment worked; reading what it said is a separate request, behind a
// separate permission.
func stripLogs(deployments []Deployment) []Deployment {
	stripped := make([]Deployment, 0, len(deployments))
	for _, deployment := range deployments {
		deployment.Log = ""
		stripped = append(stripped, deployment)
	}
	return stripped
}

// hostStatus asks the Agent what this website's working tree looks like.
func (s *Service) hostStatus(ctx context.Context, requestID string, site WebsiteRef) map[string]any {
	response, err := s.agent.Do(ctx, protocol.Request{
		Operation: protocol.OperationDeployStatus,
		RequestID: requestID,
		Payload: map[string]any{
			"account":       site.SystemUser,
			"document_root": site.DocumentRoot,
		},
	})
	if err != nil {
		s.log.Warn("could not read the deployment status", "website", site.Domain, "error", err)
		return map[string]any{
			"available": false,
			"reason":    "the host agent could not be reached: " + err.Error(),
		}
	}
	return response.Data
}

// RepositoryRequest is a repository being created or changed.
type RepositoryRequest struct {
	WebsiteID            string  `json:"website_id"`
	RemoteURL            string  `json:"remote_url"`
	Branch               string  `json:"branch"`
	Provider             string  `json:"provider"`
	AutoDeploy           *bool   `json:"auto_deploy"`
	DeployScript         *string `json:"deploy_script"`
	ScriptTimeoutSeconds *int    `json:"script_timeout_seconds"`
	// WebhookSecret is set once and never returned. Empty on an update leaves
	// the existing one alone, which is what makes "change the branch" not
	// silently require retyping the secret.
	WebhookSecret string `json:"webhook_secret"`
}

// Configure records a website's repository, creating it if there is none.
func (s *Service) Configure(ctx context.Context, actor Actor, requestID string,
	req RepositoryRequest,
) (GitRepository, error) {
	site, err := s.websites.LookupForDeploy(ctx, req.WebsiteID)
	if err != nil {
		return GitRepository{}, err
	}
	if site.SystemUser == "" || site.DocumentRoot == "" {
		return GitRepository{}, ErrNoAccount
	}

	remote := strings.TrimSpace(req.RemoteURL)
	if err := validate.GitRemote(remote); err != nil {
		return GitRepository{}, err
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		branch = "main"
	}
	if err := validate.GitBranch(branch); err != nil {
		return GitRepository{}, err
	}
	provider := req.Provider
	if provider == "" {
		provider = validate.ProviderNone
	}
	if err := validate.DeployProvider(provider); err != nil {
		return GitRepository{}, err
	}

	existing, err := s.repo.RepositoryForWebsite(ctx, req.WebsiteID)
	isNew := errors.Is(err, ErrNotFound)
	if err != nil && !isNew {
		return GitRepository{}, err
	}

	record := GitRepository{
		ServerID:             s.serverID,
		WebsiteID:            req.WebsiteID,
		RemoteURL:            remote,
		Branch:               branch,
		Provider:             provider,
		AutoDeploy:           existing.AutoDeploy,
		DeployScript:         existing.DeployScript,
		ScriptTimeoutSeconds: existing.ScriptTimeoutSeconds,
	}
	if isNew {
		record.ScriptTimeoutSeconds = 600
	}
	if req.AutoDeploy != nil {
		record.AutoDeploy = *req.AutoDeploy
	}
	if req.DeployScript != nil {
		if err := validate.DeployScript(*req.DeployScript); err != nil {
			return GitRepository{}, err
		}
		record.DeployScript = *req.DeployScript
	}
	if req.ScriptTimeoutSeconds != nil {
		record.ScriptTimeoutSeconds = *req.ScriptTimeoutSeconds
	}
	if err := validate.ScriptTimeout(record.ScriptTimeoutSeconds); err != nil {
		return GitRepository{}, err
	}

	// A webhook needs both halves. A token with no secret would be an
	// unauthenticated endpoint that deploys, which is the one thing this phase
	// must never ship — so a token is only ever generated alongside a secret.
	token := ""
	if provider != validate.ProviderNone {
		hasSecret := !isNew && existing.WebhookToken != ""
		if req.WebhookSecret == "" && !hasSecret {
			return GitRepository{}, fmt.Errorf(
				"a webhook needs a secret: without one, anybody who finds the URL could " +
					"deploy this website")
		}
		if req.WebhookSecret != "" && len(req.WebhookSecret) < MinWebhookSecret {
			return GitRepository{}, fmt.Errorf(
				"a webhook secret must be at least %d characters", MinWebhookSecret)
		}
		if !hasSecret {
			token, err = newToken()
			if err != nil {
				return GitRepository{}, err
			}
		}
	}

	// The secret is encrypted against the row's own id, which is not known
	// until the row exists — so the row is written with a placeholder and the
	// real secret follows.
	//
	// The placeholder is never a usable secret: it is not ciphertext, so it
	// fails to decrypt, and a signature checked against it can never match. A
	// crash between the two writes therefore leaves a webhook that refuses
	// every push, which is the right way round for this to fail.
	placeholder := ""
	if token != "" {
		placeholder = "pending"
	}

	var saved GitRepository
	if isNew {
		saved, err = s.repo.CreateRepository(ctx, record, token, placeholder)
	} else {
		saved, err = s.repo.UpdateRepository(ctx, existing.ID, record, token, placeholder)
	}
	if err != nil {
		return GitRepository{}, err
	}

	if req.WebhookSecret != "" {
		if err := s.sealSecret(ctx, saved.ID, req.WebhookSecret); err != nil {
			return GitRepository{}, err
		}
		saved, err = s.repo.GetRepository(ctx, saved.ID)
		if err != nil {
			return GitRepository{}, err
		}
	}

	if req.AutoDeploy != nil && *req.AutoDeploy && !existing.AutoDeploy {
		s.record(ctx, actor, ActionAutoEnable, ResourceTypeRepository, saved.ID, map[string]any{
			"website": site.Domain,
			"branch":  branch,
			"note": "a push to this branch now runs code on this host without " +
				"anybody touching the panel",
		})
	}
	s.record(ctx, actor, ActionConfigure, ResourceTypeRepository, saved.ID, map[string]any{
		"website": site.Domain,
		"remote":  remote,
		"branch":  branch,
	})
	return saved, nil
}

// sealSecret encrypts a webhook secret against its own row.
func (s *Service) sealSecret(ctx context.Context, id, secret string) error {
	if s.secrets == nil {
		return fmt.Errorf("this panel has no encryption key, so a webhook secret cannot be stored")
	}
	if secret == "" {
		return nil
	}
	sealed, err := s.secrets.Encrypt([]byte(secret), id)
	if err != nil {
		return fmt.Errorf("store the webhook secret: %w", err)
	}
	_, err = s.repo.pool.Exec(ctx,
		`UPDATE git_repositories SET webhook_secret_encrypted = $2, updated_at = now()
		 WHERE id = $1::uuid`, id, sealed)
	if err != nil {
		return fmt.Errorf("store the webhook secret: %w", err)
	}
	return nil
}

// newToken produces a webhook address.
//
// 32 bytes of randomness. It is not what authenticates the request — the
// signature is — but it is what stops somebody enumerating the panel's
// repositories by trying URLs, so it is generated the same way a credential
// would be.
func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate a webhook address: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// webhookURL builds the address to paste into a forge.
func (s *Service) webhookURL(token string) string {
	if token == "" || panelURL == "" {
		return ""
	}
	return strings.TrimRight(panelURL, "/") + "/api/v1/webhooks/deploy/" + token
}

// panelURL is where this panel is reachable, for building a webhook address.
//
// Set once at startup from the same configuration Phase 20 uses for its
// notification links. Empty leaves the URL out of the reply rather than
// printing a relative path nobody can paste into GitHub.
var panelURL string

// SetPanelURL records where this panel is reachable.
func SetPanelURL(value string) { panelURL = value }

// SetActions replaces a repository's steps.
func (s *Service) SetActions(ctx context.Context, actor Actor, requestID, id string,
	kinds []string,
) ([]Action, error) {
	if _, err := s.repo.GetRepository(ctx, id); err != nil {
		return nil, err
	}

	actions := make([]Action, 0, len(kinds))
	for _, kind := range kinds {
		if err := validate.DeployAction(kind); err != nil {
			return nil, err
		}
		actions = append(actions, Action{Kind: kind, Enabled: true})
	}

	saved, err := s.repo.ReplaceActions(ctx, id, actions)
	if err != nil {
		return nil, err
	}
	s.record(ctx, actor, ActionActions, ResourceTypeRepository, id, map[string]any{
		"steps": kinds,
	})
	return saved, nil
}

// GenerateKey asks the host for a deploy key and records its public half.
func (s *Service) GenerateKey(ctx context.Context, actor Actor, requestID, id string) (GitRepository, error) {
	repository, err := s.repo.GetRepository(ctx, id)
	if err != nil {
		return GitRepository{}, err
	}
	site, err := s.websites.LookupForDeploy(ctx, repository.WebsiteID)
	if err != nil {
		return GitRepository{}, err
	}
	if site.SystemUser == "" {
		return GitRepository{}, ErrNoAccount
	}

	response, err := s.agent.Do(ctx, protocol.Request{
		Operation: protocol.OperationDeployKeyGenerate,
		RequestID: requestID,
		Payload:   map[string]any{"account": site.SystemUser},
	})
	if err != nil {
		return GitRepository{}, err
	}
	public, _ := response.Data["public_key"].(string)
	fingerprint, _ := response.Data["fingerprint"].(string)
	if public == "" {
		return GitRepository{}, fmt.Errorf("the host generated a key and returned no public half")
	}

	if err := s.repo.SaveDeployKey(ctx, id, public, fingerprint); err != nil {
		return GitRepository{}, err
	}
	s.record(ctx, actor, ActionKeyGenerate, ResourceTypeRepository, id, map[string]any{
		"website":     site.Domain,
		"fingerprint": fingerprint,
	})
	return s.repo.GetRepository(ctx, id)
}

// Remove disconnects a website from its repository.
func (s *Service) Remove(ctx context.Context, actor Actor, requestID, id string) error {
	repository, err := s.repo.GetRepository(ctx, id)
	if err != nil {
		return err
	}
	site, siteErr := s.websites.LookupForDeploy(ctx, repository.WebsiteID)

	if siteErr == nil && site.SystemUser != "" {
		if _, err := s.agent.Do(ctx, protocol.Request{
			Operation: protocol.OperationDeployUnlink,
			RequestID: requestID,
			Payload: map[string]any{
				"account":       site.SystemUser,
				"document_root": site.DocumentRoot,
				"remove_key":    true,
			},
		}); err != nil {
			// Reported and not fatal. The panel's record is what makes a
			// website deployable, so removing it is the part that must
			// succeed; a working tree left behind is untidy and harmless.
			s.log.Warn("could not unlink the working tree",
				"website", site.Domain, "error", err)
		}
	}

	if err := s.repo.DeleteRepository(ctx, id); err != nil {
		return err
	}
	s.record(ctx, actor, ActionRemove, ResourceTypeRepository, id, map[string]any{
		"website": site.Domain,
		"note":    "the deployed files are still in the document root",
	})
	return nil
}

// Deploy queues a deployment.
//
// The deployment row is created first and the job second, and that order is the
// whole of the panel's protection against two deployments at once: the unique
// partial index refuses a second row while one is pending or running, so the
// second caller is told rather than queued. A check written here in Go would be
// one two API processes could both pass.
func (s *Service) Deploy(ctx context.Context, actor Actor, requestID, id string,
	trigger, commit string,
) (Deployment, error) {
	repository, err := s.repo.GetRepository(ctx, id)
	if err != nil {
		return Deployment{}, err
	}
	site, err := s.websites.LookupForDeploy(ctx, repository.WebsiteID)
	if err != nil {
		return Deployment{}, err
	}
	if site.SystemUser == "" || site.DocumentRoot == "" {
		return Deployment{}, ErrNoAccount
	}
	if err := validate.DeployTrigger(trigger); err != nil {
		return Deployment{}, err
	}
	if commit != "" {
		if err := validate.CommitSHA(commit); err != nil {
			return Deployment{}, err
		}
	}

	deployment, err := s.repo.StartDeployment(ctx, Deployment{
		RepositoryID:   id,
		WebsiteID:      repository.WebsiteID,
		Trigger:        trigger,
		Branch:         repository.Branch,
		PreviousCommit: repository.CurrentCommit,
	})
	if err != nil {
		return Deployment{}, err
	}

	job, err := s.jobs.Create(ctx, jobs.CreateParams{
		Type: JobTypeDeployRun,
		// The deployment's id and the commit, and nothing else. What the Agent
		// needs is rebuilt at dispatch by ResolvePayload, so the queue never
		// holds the deployment script — which is text an operator wrote and
		// which the queue has no reason to carry.
		Payload:      map[string]any{"deployment_id": deployment.ID, "commit": commit},
		CreatedBy:    actor.UserID,
		ResourceType: ResourceTypeDeployment,
		ResourceID:   deployment.ID,
	})
	if err != nil {
		// The row exists and nothing will run it, which would hold the index
		// for ever. Finished here rather than left, so the website can be
		// deployed again.
		s.failDeployment(ctx, deployment.ID, "the deployment could not be queued: "+err.Error())
		return Deployment{}, err
	}
	if err := s.attachJob(ctx, deployment.ID, job.ID); err != nil {
		s.log.Warn("could not attach a job to a deployment", "error", err)
	}

	action := ActionRun
	if trigger == validate.TriggerRollback {
		action = ActionRollback
	}
	s.record(ctx, actor, action, ResourceTypeDeployment, deployment.ID, map[string]any{
		"website": site.Domain,
		"branch":  repository.Branch,
		"trigger": trigger,
		"commit":  commit,
	})

	deployment.JobID = job.ID
	return deployment, nil
}

// attachJob records which job is running a deployment.
func (s *Service) attachJob(ctx context.Context, deploymentID, jobID string) error {
	_, err := s.repo.pool.Exec(ctx,
		`UPDATE deployments SET job_id = $2 WHERE id = $1::uuid`, deploymentID, jobID)
	return err
}

// failDeployment closes a deployment that never ran.
func (s *Service) failDeployment(ctx context.Context, id, reason string) {
	if err := s.repo.FinishDeployment(ctx, id, Deployment{
		Status: "failed",
		Log:    reason,
	}); err != nil {
		s.log.Error("could not close a deployment that never started",
			"deployment", id, "error", err)
	}
}

// ResolvePayload builds the Agent's payload at dispatch.
//
// This is the jobs.PayloadResolver hook. The queue row holds the deployment's
// id; everything the Agent needs — the account, the document root, the remote,
// the steps, the script — is rebuilt here, from rows that are the truth at the
// moment the work actually starts rather than at the moment it was asked for.
func (s *Service) ResolvePayload(ctx context.Context, job jobs.Job) (map[string]any, error) {
	if job.Type != JobTypeDeployRun {
		return nil, nil
	}
	id, _ := job.Payload["deployment_id"].(string)
	if id == "" {
		return nil, errors.New("this deployment job names no deployment")
	}
	commit, _ := job.Payload["commit"].(string)

	deployment, err := s.repo.GetDeployment(ctx, id)
	if err != nil {
		return nil, err
	}
	repository, err := s.repo.GetRepository(ctx, deployment.RepositoryID)
	if err != nil {
		return nil, err
	}
	site, err := s.websites.LookupForDeploy(ctx, repository.WebsiteID)
	if err != nil {
		return nil, err
	}
	if site.SystemUser == "" || site.DocumentRoot == "" {
		return nil, ErrNoAccount
	}

	actions, err := s.repo.ListActions(ctx, repository.ID)
	if err != nil {
		return nil, err
	}
	steps := make([]map[string]any, 0, len(actions))
	for _, action := range actions {
		if action.Enabled {
			steps = append(steps, map[string]any{"kind": action.Kind})
		}
	}

	return map[string]any{
		"account":                site.SystemUser,
		"document_root":          site.DocumentRoot,
		"remote":                 repository.RemoteURL,
		"branch":                 repository.Branch,
		"commit":                 commit,
		"actions":                steps,
		"script":                 repository.DeployScript,
		"script_timeout_seconds": repository.ScriptTimeoutSeconds,
		"use_deploy_key":         strings.HasPrefix(repository.RemoteURL, "git@") || strings.HasPrefix(repository.RemoteURL, "ssh://"),
		"rollback_on_failure":    deployment.PreviousCommit != "",
	}, nil
}

// JobFinished records how a deployment ended.
//
// This is the jobs.Observer hook. It runs after the job row is already updated,
// so a failure here leaves an accurate job and a stale deployment rather than
// the reverse.
func (s *Service) JobFinished(ctx context.Context, job jobs.Job, state jobs.State,
	result map[string]any, failure string,
) {
	if job.Type != JobTypeDeployRun {
		return
	}
	id, _ := job.Payload["deployment_id"].(string)
	if id == "" {
		return
	}

	deployment := Deployment{Status: "failed"}
	if state == jobs.StateSuccess {
		deployment.Status = "success"
	}

	if result != nil {
		deployment.CommitSHA, _ = result["commit"].(string)
		deployment.CommitMessage, _ = result["message"].(string)
		deployment.CommitAuthor, _ = result["author"].(string)
		deployment.Log, _ = result["log"].(string)
		deployment.LogTruncated, _ = result["log_truncated"].(bool)
		deployment.RolledBack, _ = result["rolled_back"].(bool)
		deployment.RollbackError, _ = result["rollback_error"].(string)
		if code, ok := result["exit_code"].(float64); ok {
			exit := int(code)
			deployment.ExitCode = &exit
		}
		if duration, ok := result["duration_ms"].(float64); ok {
			ms := int(duration)
			deployment.DurationMS = &ms
		}
		if succeeded, ok := result["succeeded"].(bool); ok && !succeeded {
			deployment.Status = "failed"
		}
	}
	if failure != "" {
		if deployment.Log != "" {
			deployment.Log += "\n"
		}
		deployment.Log += failure
	}

	if err := s.repo.FinishDeployment(ctx, id, deployment); err != nil {
		s.log.Error("could not record a deployment's outcome",
			"deployment", id, "error", err)
		return
	}

	// What the website is running now. Recorded on success *and* on a rollback:
	// after a rollback the site is on the previous commit, and a panel that
	// went on showing the failed one would be wrong about what is deployed.
	if deployment.CommitSHA != "" && (deployment.Status == "success" || deployment.RolledBack) {
		existing, err := s.repo.GetDeployment(ctx, id)
		if err == nil {
			if err := s.repo.RecordDeployed(ctx, existing.RepositoryID,
				deployment.CommitSHA, existing.Branch); err != nil {
				s.log.Warn("could not record what is deployed", "error", err)
			}
		}
	}
}

// DeploymentLog reads one deployment's output.
//
// Its own method rather than a field on the list, because a build prints
// whatever the build printed — a token in a URL, an environment variable a
// script echoed — and reading that needs deploy.manage while seeing that a
// deployment failed does not.
func (s *Service) DeploymentLog(ctx context.Context, id string) (Deployment, error) {
	return s.repo.GetDeployment(ctx, id)
}

// ReleaseStale closes deployments that were running when the panel stopped.
func (s *Service) ReleaseStale(ctx context.Context) {
	released, err := s.repo.ReleaseStale(ctx)
	if err != nil {
		s.log.Error("could not release stale deployments", "error", err)
		return
	}
	if released > 0 {
		s.log.Warn("closed deployments that were running when the panel stopped",
			"count", released)
	}
}

// record writes an audit entry.
func (s *Service) record(ctx context.Context, actor Actor, action, resourceType,
	resourceID string, metadata map[string]any,
) {
	if s.audit == nil {
		return
	}
	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       actor.UserID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		IPAddress:    actor.IPAddress,
		UserAgent:    actor.UserAgent,
		Status:       "success",
		Metadata:     metadata,
	})
}

// timeNow is the clock, replaceable in tests.
var timeNow = time.Now
