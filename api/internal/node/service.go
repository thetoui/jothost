package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/websites"
	"github.com/jothost/panel/shared/validate"
)

// Audit action names for Node.js work.
const (
	ActionAppCreate      = "node.app.create"
	ActionAppUpdate      = "node.app.update"
	ActionAppDelete      = "node.app.delete"
	ActionAppStart       = "node.app.start"
	ActionAppStop        = "node.app.stop"
	ActionAppRestart     = "node.app.restart"
	ActionEnvChange      = "node.app.environment"
	ActionEnvReveal      = "node.app.environment.reveal"
	ActionRuntimeInstall = "node.install"
	ActionRuntimeRemove  = "node.uninstall"

	ResourceTypeApp = "node_app"
)

// Errors returned by the service.
var (
	// ErrNoServer means no host is registered, so there is nothing to act on.
	ErrNoServer = errors.New("no server is registered, so applications cannot be managed")
	// ErrRuntimeUnavailable means the host has no Node.js.
	ErrRuntimeUnavailable = errors.New(
		"no Node.js runtime is installed on this server; install one first")
	// ErrWebsiteNotReady means the site cannot host an application yet.
	ErrWebsiteNotReady = errors.New(
		"this website is not in a state that allows running an application")
	// ErrRuntimeInUse means an application still depends on the runtime.
	ErrRuntimeInUse = errors.New(
		"a Node.js application still uses this runtime; remove the applications first")
	// ErrWebsiteHasPHP means the site already serves PHP.
	ErrWebsiteHasPHP = errors.New(
		"this website serves PHP; turn PHP off before running a Node.js application on it, " +
			"because a site is served by one or the other")
)

// Agent is the subset of the agent client this service uses.
type Agent interface {
	NodeVersions(ctx context.Context, requestID string) (agentclient.NodeVersionsResult, error)
	NodeDeploy(ctx context.Context, requestID string, req agentclient.NodeAppRequest) error
	NodeRemove(ctx context.Context, requestID string, req agentclient.NodeAppRequest) error
	NodeStart(ctx context.Context, requestID string, req agentclient.NodeAppRequest) (agentclient.NodeStatus, error)
	NodeStop(ctx context.Context, requestID string, req agentclient.NodeAppRequest) (agentclient.NodeStatus, error)
	NodeRestart(ctx context.Context, requestID string, req agentclient.NodeAppRequest) (agentclient.NodeStatus, error)
	NodeStatusOf(ctx context.Context, requestID string, req agentclient.NodeAppRequest) (agentclient.NodeStatus, error)
	NodeAppLogs(ctx context.Context, requestID string, req agentclient.NodeAppRequest, lines int) (agentclient.NodeLogs, error)
	NodeInstallDependencies(ctx context.Context, requestID string, req agentclient.NodeAppRequest) error
	NodeInstall(ctx context.Context, requestID, pkg string) (agentclient.NodeVersion, error)
	NodeUninstall(ctx context.Context, requestID, pkg string) error
	UpdateWebsite(ctx context.Context, requestID string, payload map[string]any) error
}

// Service coordinates application records with the processes that realise them.
//
// Synchronous, like the database manager and for the same reason: starting a
// process is fast, and the response should say what actually happened rather
// than that the intent was recorded. The one slow operation — installing
// dependencies — is the exception, and it says so.
type Service struct {
	repo     *Repository
	websites *websites.Repository
	agent    Agent
	audit    *audit.Recorder
	log      *slog.Logger
	serverID string
}

// ServiceOptions configure a Service.
type ServiceOptions struct {
	Repository *Repository
	Websites   *websites.Repository
	Agent      Agent
	Audit      *audit.Recorder
	Log        *slog.Logger
	ServerID   string
}

// NewService builds a Service.
func NewService(opts ServiceOptions) *Service {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		repo:     opts.Repository,
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

// Versions reports what the host runs and could run.
func (s *Service) Versions(ctx context.Context, requestID string) (agentclient.NodeVersionsResult, error) {
	return s.agent.NodeVersions(ctx, requestID)
}

// InstallRuntime adds a Node.js release line to the host.
//
// The package must be one the Agent offered. Checking here rather than
// trusting the caller means a request naming something else never reaches a
// package manager running as root, even though the Agent checks again.
func (s *Service) InstallRuntime(ctx context.Context, requestID, pkg string, actor Actor) (agentclient.NodeVersion, error) {
	offered, err := s.agent.NodeVersions(ctx, requestID)
	if err != nil {
		return agentclient.NodeVersion{}, err
	}

	known := false
	for _, offer := range offered.Offers {
		if offer.Package == pkg {
			known = true
			break
		}
	}
	if !known {
		return agentclient.NodeVersion{}, fmt.Errorf(
			"%w: this host does not offer %q", ErrRuntimeUnavailable, pkg)
	}

	version, err := s.agent.NodeInstall(ctx, requestID, pkg)
	if err != nil {
		return agentclient.NodeVersion{}, err
	}

	s.record(ctx, actor, ActionRuntimeInstall, "", map[string]any{
		"package": pkg, "version": version.Full,
	})
	return version, nil
}

// RemoveRuntime takes a Node.js release line off the host.
//
// Refused while any application still uses it: the Agent would do as it was
// told and every one of them would stop at its next restart, which is not a
// consequence anybody asked for.
func (s *Service) RemoveRuntime(ctx context.Context, requestID, pkg string, actor Actor) error {
	apps, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	if len(apps) > 0 {
		return fmt.Errorf("%w: %d application(s) still use it", ErrRuntimeInUse, len(apps))
	}

	if err := s.agent.NodeUninstall(ctx, requestID, pkg); err != nil {
		return err
	}

	s.record(ctx, actor, ActionRuntimeRemove, "", map[string]any{"package": pkg})
	return nil
}

// CreateRequest asks for a new application.
type CreateRequest struct {
	WebsiteID string
	Name      string
	Version   string
	Startup   string
	Port      int
	Actor     Actor
	RequestID string
}

// Create records an application, prepares it on the host, and points the
// website's vhost at it.
//
// The vhost comes last. Pointing nginx at a port nothing is listening on would
// take the site down between the two steps, and the site was serving something
// before this was asked for.
func (s *Service) Create(ctx context.Context, req CreateRequest) (App, error) {
	if s.serverID == "" {
		return App{}, ErrNoServer
	}

	site, err := s.readySite(ctx, req.WebsiteID)
	if err != nil {
		return App{}, err
	}

	version, err := s.resolveVersion(ctx, req.RequestID, req.Version)
	if err != nil {
		return App{}, err
	}

	name := strings.ToLower(strings.TrimSpace(req.Name))
	if name == "" {
		name = validate.AppNameFor(site.PrimaryDomain, shortHash(site.ID))
	}
	if err := validate.AppName(name); err != nil {
		return App{}, err
	}

	startup := strings.TrimSpace(req.Startup)
	if startup == "" {
		startup = "server.js"
	}
	if err := validate.StartupFile(startup); err != nil {
		return App{}, err
	}
	if err := validate.AppPort(req.Port); err != nil {
		return App{}, err
	}

	app, err := s.repo.Create(ctx, CreateParams{
		ServerID:  s.serverID,
		WebsiteID: site.ID,
		Name:      name,
		Version:   version,
		Root:      site.DocumentRoot,
		Startup:   startup,
		Port:      req.Port,
		Unit:      "jothost-node-" + name + ".service",
	})
	if err != nil {
		return App{}, err
	}

	if err := s.agent.NodeDeploy(ctx, req.RequestID, s.agentRequest(app, site, nil)); err != nil {
		// The record is removed rather than left in a state nothing on the
		// host matches: an application the panel lists but never prepared is
		// worse than one that was not created.
		if deleteErr := s.repo.Delete(ctx, app.ID); deleteErr != nil {
			s.log.Error("could not remove an application record after a failed deploy",
				"app", app.ID, "error", deleteErr.Error())
		}
		return App{}, err
	}

	s.record(ctx, req.Actor, ActionAppCreate, app.ID, map[string]any{
		"name": app.Name, "domain": site.PrimaryDomain, "port": app.Port,
	})
	return s.Get(ctx, req.RequestID, app.ID)
}

// Start runs an application and points the website at it.
func (s *Service) Start(ctx context.Context, requestID, id string, actor Actor) (App, error) {
	app, site, err := s.load(ctx, id)
	if err != nil {
		return App{}, err
	}

	env, err := s.repo.Environment(ctx, app.ID)
	if err != nil {
		return App{}, err
	}

	status, err := s.agent.NodeStart(ctx, requestID, s.agentRequest(app, site, env))
	if err != nil {
		s.markFailed(ctx, app.ID, err)
		return App{}, err
	}

	if err := s.repo.SetStatus(ctx, app.ID, statusFrom(status), nil); err != nil {
		return App{}, err
	}

	// The vhost is pointed at the application only once it is listening.
	// Doing it before would replace a working site with a 502.
	if status.Listening {
		if err := s.proxyWebsite(ctx, requestID, site, app.Port); err != nil {
			s.log.Error("the application started but the website was not pointed at it",
				"app", app.ID, "error", err.Error())
		}
	}

	s.record(ctx, actor, ActionAppStart, app.ID, map[string]any{"name": app.Name})
	return s.Get(ctx, requestID, app.ID)
}

// Stop ends an application and returns the website to serving files.
//
// The site is put back first. Leaving nginx pointed at a port nothing answers
// on turns a deliberate stop into a 502 for every visitor, which reads as an
// outage rather than as maintenance.
func (s *Service) Stop(ctx context.Context, requestID, id string, actor Actor) (App, error) {
	app, site, err := s.load(ctx, id)
	if err != nil {
		return App{}, err
	}

	if err := s.proxyWebsite(ctx, requestID, site, 0); err != nil {
		s.log.Warn("could not return the website to serving files before stopping",
			"app", app.ID, "error", err.Error())
	}

	status, err := s.agent.NodeStop(ctx, requestID, s.agentRequest(app, site, nil))
	if err != nil {
		return App{}, err
	}
	if err := s.repo.SetStatus(ctx, app.ID, statusFrom(status), nil); err != nil {
		return App{}, err
	}

	s.record(ctx, actor, ActionAppStop, app.ID, map[string]any{"name": app.Name})
	return s.Get(ctx, requestID, app.ID)
}

// Restart restarts an application.
func (s *Service) Restart(ctx context.Context, requestID, id string, actor Actor) (App, error) {
	app, site, err := s.load(ctx, id)
	if err != nil {
		return App{}, err
	}

	env, err := s.repo.Environment(ctx, app.ID)
	if err != nil {
		return App{}, err
	}

	status, err := s.agent.NodeRestart(ctx, requestID, s.agentRequest(app, site, env))
	if err != nil {
		s.markFailed(ctx, app.ID, err)
		return App{}, err
	}
	if err := s.repo.SetStatus(ctx, app.ID, statusFrom(status), nil); err != nil {
		return App{}, err
	}

	s.record(ctx, actor, ActionAppRestart, app.ID, map[string]any{"name": app.Name})
	return s.Get(ctx, requestID, app.ID)
}

// Delete removes an application from the host and the panel.
func (s *Service) Delete(ctx context.Context, requestID, id string, actor Actor) error {
	app, site, err := s.load(ctx, id)
	if err != nil {
		return err
	}

	if err := s.proxyWebsite(ctx, requestID, site, 0); err != nil {
		s.log.Warn("could not return the website to serving files",
			"app", app.ID, "error", err.Error())
	}

	if err := s.agent.NodeRemove(ctx, requestID, s.agentRequest(app, site, nil)); err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, app.ID); err != nil {
		return err
	}

	s.record(ctx, actor, ActionAppDelete, "", map[string]any{
		"name": app.Name, "domain": site.PrimaryDomain,
	})
	return nil
}

// Get returns one application with what the host says about it.
func (s *Service) Get(ctx context.Context, requestID, id string) (App, error) {
	app, err := s.repo.Get(ctx, id)
	if err != nil {
		return App{}, err
	}

	keys, err := s.repo.EnvKeys(ctx, app.ID)
	if err != nil {
		return App{}, err
	}
	app.Environment = keys

	site, err := s.websites.Get(ctx, app.WebsiteID)
	if err == nil {
		if status, err := s.agent.NodeStatusOf(ctx, requestID, s.agentRequest(app, site, nil)); err == nil {
			app.Runtime = runtimeMap(status)
			// What the host says is the truth; the record follows it, so a
			// process that died is not still listed as running.
			if recorded := statusFrom(status); recorded != app.Status {
				if err := s.repo.SetStatus(ctx, app.ID, recorded, nil); err == nil {
					app.Status = recorded
				}
			}
		}
	}
	return app, nil
}

// List returns every application.
func (s *Service) List(ctx context.Context) ([]App, error) {
	return s.repo.List(ctx)
}

// Logs returns the tail of an application's output.
func (s *Service) Logs(ctx context.Context, requestID, id string, lines int) (agentclient.NodeLogs, error) {
	app, site, err := s.load(ctx, id)
	if err != nil {
		return agentclient.NodeLogs{}, err
	}
	return s.agent.NodeAppLogs(ctx, requestID, s.agentRequest(app, site, nil), lines)
}

// InstallDependencies runs npm install for an application.
func (s *Service) InstallDependencies(ctx context.Context, requestID, id string, actor Actor) error {
	app, site, err := s.load(ctx, id)
	if err != nil {
		return err
	}

	if err := s.agent.NodeInstallDependencies(ctx, requestID, s.agentRequest(app, site, nil)); err != nil {
		return err
	}

	s.record(ctx, actor, ActionAppUpdate, app.ID, map[string]any{
		"name": app.Name, "action": "install dependencies",
	})
	return nil
}

// SetEnv adds or changes one environment variable.
//
// The application is not restarted. A process reads its environment once at
// startup, so a change takes effect on the next restart — and restarting
// somebody's application as a side effect of editing a setting is not the
// panel's decision to make.
func (s *Service) SetEnv(ctx context.Context, requestID, id, key, value string, actor Actor) error {
	app, site, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if err := validate.EnvKey(key); err != nil {
		return err
	}
	if err := validate.EnvValue(value); err != nil {
		return err
	}

	if err := s.repo.SetEnv(ctx, app.ID, key, value); err != nil {
		return err
	}
	if err := s.writeEnvironment(ctx, requestID, app, site); err != nil {
		return err
	}

	// The value is never in the audit record: it is the secret this whole
	// mechanism exists to protect.
	s.record(ctx, actor, ActionEnvChange, app.ID, map[string]any{
		"name": app.Name, "key": key, "action": "set",
	})
	return nil
}

// RemoveEnv deletes one environment variable.
func (s *Service) RemoveEnv(ctx context.Context, requestID, id, key string, actor Actor) error {
	app, site, err := s.load(ctx, id)
	if err != nil {
		return err
	}

	if err := s.repo.RemoveEnv(ctx, app.ID, key); err != nil {
		return err
	}
	if err := s.writeEnvironment(ctx, requestID, app, site); err != nil {
		return err
	}

	s.record(ctx, actor, ActionEnvChange, app.ID, map[string]any{
		"name": app.Name, "key": key, "action": "remove",
	})
	return nil
}

// RevealEnv returns the whole configuration, values included.
//
// Audited, like reading a database password: this returns credentials, and who
// read them has to stay answerable.
func (s *Service) RevealEnv(ctx context.Context, id string, actor Actor) (map[string]string, error) {
	app, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	env, err := s.repo.Environment(ctx, app.ID)
	if err != nil {
		return nil, err
	}

	s.record(ctx, actor, ActionEnvReveal, app.ID, map[string]any{
		"name": app.Name, "keys": len(env),
	})
	return env, nil
}

// writeEnvironment pushes the configuration to the host.
func (s *Service) writeEnvironment(ctx context.Context, requestID string,
	app App, site websites.Website,
) error {
	env, err := s.repo.Environment(ctx, app.ID)
	if err != nil {
		return err
	}
	return s.agent.NodeDeploy(ctx, requestID, s.agentRequest(app, site, env))
}

// proxyWebsite points the site's vhost at the application, or back at files.
func (s *Service) proxyWebsite(ctx context.Context, requestID string,
	site websites.Website, port int,
) error {
	return s.agent.UpdateWebsite(ctx, requestID, map[string]any{
		"domain":        site.PrimaryDomain,
		"document_root": site.DocumentRoot,
		"proxy_port":    port,
	})
}

// load reads an application and its website together.
func (s *Service) load(ctx context.Context, id string) (App, websites.Website, error) {
	app, err := s.repo.Get(ctx, id)
	if err != nil {
		return App{}, websites.Website{}, err
	}
	site, err := s.websites.Get(ctx, app.WebsiteID)
	if err != nil {
		return App{}, websites.Website{}, err
	}
	return app, site, nil
}

// agentRequest builds the payload the Agent expects.
func (s *Service) agentRequest(app App, site websites.Website, env map[string]string) agentclient.NodeAppRequest {
	return agentclient.NodeAppRequest{
		Name:    app.Name,
		Version: app.Version,
		Root:    site.DocumentRoot,
		Startup: app.Startup,
		Port:    app.Port,
		// The site's own account, so an application can reach its own files
		// and nobody else's.
		User:        site.SystemUser,
		Group:       site.SystemUser,
		Environment: env,
	}
}

// resolveVersion picks the Node.js version to run under.
func (s *Service) resolveVersion(ctx context.Context, requestID, requested string) (string, error) {
	versions, err := s.agent.NodeVersions(ctx, requestID)
	if err != nil {
		return "", err
	}
	if len(versions.Versions) == 0 {
		return "", ErrRuntimeUnavailable
	}

	if requested == "" {
		// The only one, on almost every host.
		return versions.Versions[0].Version, nil
	}
	if err := validate.NodeVersion(requested); err != nil {
		return "", err
	}

	major := validate.NodeMajor(requested)
	for _, version := range versions.Versions {
		if version.Version == major {
			return version.Version, nil
		}
	}
	return "", fmt.Errorf("%w: Node.js %s is not installed", ErrRuntimeUnavailable, requested)
}

// readySite loads a website and refuses one that cannot host an application.
func (s *Service) readySite(ctx context.Context, websiteID string) (websites.Website, error) {
	site, err := s.websites.Get(ctx, websiteID)
	if err != nil {
		return websites.Website{}, err
	}
	if site.Status != websites.StatusActive && site.Status != websites.StatusFailed {
		return websites.Website{}, fmt.Errorf("%w: it is %s", ErrWebsiteNotReady, site.Status)
	}
	// A site is served by an application or by files. Allowing both would
	// write a vhost that sends some URLs to PHP and the rest to Node.
	if site.PHPVersion != nil && *site.PHPVersion != "" {
		return websites.Website{}, ErrWebsiteHasPHP
	}
	return site, nil
}

// markFailed records that a start did not work, keeping the reason.
func (s *Service) markFailed(ctx context.Context, id string, cause error) {
	message := cause.Error()
	if err := s.repo.SetStatus(ctx, id, StatusFailed, &message); err != nil {
		s.log.Error("could not mark an application failed", "app", id, "error", err.Error())
	}
}

// statusFrom maps a host state onto a recorded one.
func statusFrom(status agentclient.NodeStatus) string {
	switch status.State {
	case "running":
		return StatusRunning
	case "failed":
		return StatusFailed
	default:
		return StatusStopped
	}
}

// runtimeMap renders a host status for the API response.
func runtimeMap(status agentclient.NodeStatus) map[string]any {
	return map[string]any{
		"state":          status.State,
		"pid":            status.PID,
		"port":           status.Port,
		"uptime_seconds": status.Uptime,
		"managed_by":     status.ManagedBy,
		"listening":      status.Listening,
		"detail":         status.Detail,
	}
}

// shortHash derives a stable suffix from an id.
func shortHash(id string) string {
	cleaned := strings.ReplaceAll(id, "-", "")
	if len(cleaned) < 6 {
		return "app"
	}
	return cleaned[:6]
}

// record writes an audit event, logging rather than failing the request.
func (s *Service) record(ctx context.Context, actor Actor, action, appID string,
	metadata map[string]any,
) {
	if s.audit == nil {
		return
	}
	event := audit.Event{
		UserID:       actor.UserID,
		Action:       action,
		ResourceType: ResourceTypeApp,
		IPAddress:    actor.IPAddress,
		UserAgent:    actor.UserAgent,
		Status:       audit.StatusSuccess,
		Metadata:     metadata,
	}
	if appID != "" {
		event.ResourceID = appID
	}
	s.audit.RecordAsync(ctx, event)
}

// Translate maps a domain error to its HTTP shape.
func Translate(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return httpx.NotFound("Node.js application not found")
	case errors.Is(err, websites.ErrNotFound):
		return httpx.NotFound("Website not found")
	case errors.Is(err, ErrDuplicateWebsite), errors.Is(err, ErrPortTaken),
		errors.Is(err, ErrDuplicateName), errors.Is(err, ErrWebsiteHasPHP):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrWebsiteNotReady):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrRuntimeInUse):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrRuntimeUnavailable), errors.Is(err, ErrNoServer):
		return httpx.Unavailable(err.Error())
	case errors.Is(err, validate.ErrInvalidAppName),
		errors.Is(err, validate.ErrInvalidNodeVersion),
		errors.Is(err, validate.ErrInvalidPort),
		errors.Is(err, validate.ErrInvalidEnvKey),
		errors.Is(err, validate.ErrInvalidStartupFile):
		return httpx.ValidationFailed(err.Error())
	case agentclient.IsUnsupported(err):
		return httpx.Unavailable("This host cannot run Node.js applications")
	default:
		var failed *agentclient.ErrOperationFailed
		if errors.As(err, &failed) {
			// The Agent's own refusal names the file, the port, or the
			// account, which is what the operator needs.
			return httpx.BadRequest(failed.Message)
		}
		return httpx.Internal(err)
	}
}
