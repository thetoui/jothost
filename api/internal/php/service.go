package php

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"

	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/api/internal/websites"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/validate"
)

// Audit action names for PHP work.
const (
	ActionPHPInstall    = "php.install"
	ActionPHPUninstall  = "php.uninstall"
	ActionWebsitePHPSet = "website.php_set"
	ResourceTypeVersion = "php_version"
	ResourceTypeWebsite = "website"
	SocketDir           = "/run/php-fpm"
)

// Errors returned by the service.
var (
	// ErrVersionUnavailable means the host does not have that PHP.
	ErrVersionUnavailable = errors.New("that PHP version is not installed on this server")
	// ErrInstallUnsupported means the host cannot install packages.
	ErrInstallUnsupported = errors.New("this server cannot install PHP versions")
	// ErrWebsiteNotReady means the site is mid-change and cannot take another.
	ErrWebsiteNotReady = errors.New("this website is not in a state that allows changing PHP")
)

// Service coordinates PHP records with the work that realises them.
type Service struct {
	repo     *Repository
	websites *websites.Repository
	jobs     *jobs.Repository
	audit    *audit.Recorder
	log      *slog.Logger
}

// ServiceOptions configure a Service.
type ServiceOptions struct {
	Repository *Repository
	Websites   *websites.Repository
	Jobs       *jobs.Repository
	Audit      *audit.Recorder
	Log        *slog.Logger
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
		jobs:     opts.Jobs,
		audit:    opts.Audit,
		log:      log,
	}
}

// Actor identifies who asked, for the audit trail.
type Actor struct {
	UserID    string
	IPAddress string
	UserAgent string
}

// Install queues installation of a PHP version.
func (s *Service) Install(ctx context.Context, version string, actor Actor) (jobs.Job, error) {
	version = validate.NormalizePHPVersion(version)
	if err := validate.PHPVersion(version); err != nil {
		return jobs.Job{}, err
	}

	if err := s.repo.SetVersionStatus(ctx, version, StatusInstalling); err != nil {
		return jobs.Job{}, err
	}

	job, err := s.jobs.Create(ctx, jobs.CreateParams{
		Type:         jobs.TypePHPInstall,
		Payload:      map[string]any{"version": version},
		CreatedBy:    actor.UserID,
		ResourceType: ResourceTypeVersion,
		// The resource is the version string rather than a row id: the job
		// table's resource_id is a UUID, and a version has no natural one, so
		// the payload carries it and the type says what it means.
	})
	if err != nil {
		if statusErr := s.repo.SetVersionStatus(ctx, version, StatusFailed); statusErr != nil {
			s.log.Error("failed to reset php version status after a queue failure",
				"version", version, logger.KeyError, statusErr.Error())
		}
		return jobs.Job{}, err
	}

	s.record(ctx, actor, ActionPHPInstall, "", map[string]any{
		"version": version, "job_id": job.ID,
	})
	return job, nil
}

// Uninstall queues removal of a PHP version.
//
// A version websites still run is refused rather than removed: taking it off
// the host would break every one of those sites, and the panel would have
// caused an outage on a request that looked routine.
func (s *Service) Uninstall(ctx context.Context, version string, actor Actor) (jobs.Job, error) {
	version = validate.NormalizePHPVersion(version)
	if err := validate.PHPVersion(version); err != nil {
		return jobs.Job{}, err
	}

	inUse, err := s.repo.CountWebsitesUsing(ctx, version)
	if err != nil {
		return jobs.Job{}, err
	}
	if inUse > 0 {
		return jobs.Job{}, fmt.Errorf("%w: %d website(s) still run PHP %s",
			ErrVersionInUse, inUse, version)
	}

	if err := s.repo.SetVersionStatus(ctx, version, StatusRemoving); err != nil {
		return jobs.Job{}, err
	}

	job, err := s.jobs.Create(ctx, jobs.CreateParams{
		Type:         jobs.TypePHPUninstall,
		Payload:      map[string]any{"version": version},
		CreatedBy:    actor.UserID,
		ResourceType: ResourceTypeVersion,
	})
	if err != nil {
		if statusErr := s.repo.SetVersionStatus(ctx, version, StatusAvailable); statusErr != nil {
			s.log.Error("failed to reset php version status after a queue failure",
				"version", version, logger.KeyError, statusErr.Error())
		}
		return jobs.Job{}, err
	}

	s.record(ctx, actor, ActionPHPUninstall, "", map[string]any{
		"version": version, "job_id": job.ID,
	})
	return job, nil
}

// SetRequest changes a website's PHP.
type SetRequest struct {
	WebsiteID string
	// Version is the PHP to run, or empty to make the site static again.
	Version string
	Settings
	Actor Actor
}

// Settings are the php.ini values a site may choose.
//
// Pointers distinguish "leave this alone" from "set it to the zero value",
// which a plain struct cannot express.
type Settings struct {
	MemoryLimit       *string
	UploadMaxFilesize *string
	MaxExecutionTime  *int
	OPcacheEnabled    *bool
	MaxChildren       *int
}

// SetVersion changes which PHP a website runs and queues the work.
//
// Both the pool and the vhost have to change together: a pool without a
// matching fastcgi_pass is never reached, and a fastcgi_pass to a pool that
// does not exist returns 502. One job does both.
func (s *Service) SetVersion(ctx context.Context, req SetRequest) (jobs.Job, error) {
	site, err := s.websites.Get(ctx, req.WebsiteID)
	if err != nil {
		return jobs.Job{}, err
	}

	// A site mid-provision has no directories yet, and one mid-delete is going
	// away. Queuing a pool for either produces work that cannot succeed.
	if site.Status != websites.StatusActive && site.Status != websites.StatusFailed {
		return jobs.Job{}, fmt.Errorf("%w: it is %s", ErrWebsiteNotReady, site.Status)
	}

	version := validate.NormalizePHPVersion(req.Version)
	if version == "" {
		return s.disablePHP(ctx, site, req.Actor)
	}

	if err := validate.PHPVersion(version); err != nil {
		return jobs.Job{}, err
	}

	// The version must actually be on the host. Writing a pool for a version
	// that is not installed produces a site that 502s on its first request,
	// and the cause is far from obvious at that point.
	known, err := s.repo.GetVersion(ctx, version)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return jobs.Job{}, fmt.Errorf("%w: PHP %s", ErrVersionUnavailable, version)
		}
		return jobs.Job{}, err
	}
	if !known.Installed {
		return jobs.Job{}, fmt.Errorf("%w: PHP %s", ErrVersionUnavailable, version)
	}

	settings, err := s.resolveSettings(ctx, site.ID, req.Settings)
	if err != nil {
		return jobs.Job{}, err
	}

	poolName := validate.PHPPoolNameFor(site.SystemUser)
	// The version is part of the socket name so switching versions never has
	// two FPM masters contending for one path. This must match the Agent's
	// php.SocketPathFor exactly, or the panel would record a socket nginx is
	// not passing to.
	socketPath := path.Join(SocketDir,
		poolName+"-"+validate.PHPVersionCompact(version)+".sock")

	// The record is written before the job so the panel can show what the site
	// is becoming while the work runs.
	if _, err := s.repo.UpsertPool(ctx, UpsertParams{
		WebsiteID:         site.ID,
		PHPVersion:        version,
		PoolName:          poolName,
		SocketPath:        socketPath,
		MemoryLimit:       settings.MemoryLimit,
		UploadMaxFilesize: settings.UploadMaxFilesize,
		MaxExecutionTime:  settings.MaxExecutionTime,
		OPcacheEnabled:    settings.OPcacheEnabled,
		MaxChildren:       settings.MaxChildren,
	}); err != nil {
		return jobs.Job{}, err
	}
	if err := s.repo.SetWebsiteVersion(ctx, site.ID, version); err != nil {
		return jobs.Job{}, err
	}

	// This operation rewrites the whole vhost, so the payload starts from the
	// site's complete serving state. Assembling it here from the fields PHP
	// happens to care about is what used to drop a site's certificate when its
	// PHP version was changed.
	payload, err := s.websites.VhostPayload(ctx, site)
	if err != nil {
		return jobs.Job{}, err
	}
	// A site is served by an application or by PHP, never both: a vhost
	// carrying both would send some URLs to FPM and the rest to the
	// application depending on the path. The renderer refuses that
	// configuration, so refusing here is the difference between a clear
	// message and a job that fails on the host.
	if _, proxied := payload["proxy_port"]; proxied {
		return jobs.Job{}, fmt.Errorf(
			"%w: it is served by a Node.js application; stop the application first",
			ErrWebsiteNotReady)
	}
	// The site's *current* socket is not the one this job installs, and the
	// operation derives the vhost's socket from the pool it is about to write.
	// Sending both would be two answers to one question.
	delete(payload, "php_socket")
	payload["version"] = version
	payload["pool_name"] = poolName
	payload["socket_path"] = socketPath
	payload["memory_limit"] = settings.MemoryLimit
	payload["upload_max_filesize"] = settings.UploadMaxFilesize
	payload["max_execution_time"] = settings.MaxExecutionTime
	payload["opcache_enabled"] = settings.OPcacheEnabled
	payload["max_children"] = settings.MaxChildren

	job, err := s.jobs.Create(ctx, jobs.CreateParams{
		Type:         jobs.TypeWebsitePHPSet,
		Payload:      payload,
		CreatedBy:    req.Actor.UserID,
		ResourceType: ResourceTypeWebsite,
		ResourceID:   site.ID,
	})
	if err != nil {
		return jobs.Job{}, err
	}

	s.record(ctx, req.Actor, ActionWebsitePHPSet, site.ID, map[string]any{
		"domain": site.PrimaryDomain, "version": version, "job_id": job.ID,
	})
	return job, nil
}

// disablePHP turns a site back into a static one.
func (s *Service) disablePHP(ctx context.Context, site websites.Website, actor Actor) (jobs.Job, error) {
	existing, err := s.repo.GetPool(ctx, site.ID)
	if err != nil && !errors.Is(err, ErrPoolNotFound) {
		return jobs.Job{}, err
	}
	if errors.Is(err, ErrPoolNotFound) {
		return jobs.Job{}, fmt.Errorf("%w: it does not run PHP", ErrPoolNotFound)
	}

	if err := s.repo.SetWebsiteVersion(ctx, site.ID, ""); err != nil {
		return jobs.Job{}, err
	}

	payload, err := s.websites.VhostPayload(ctx, site)
	if err != nil {
		return jobs.Job{}, err
	}
	// The pool row is already gone from the record, so the payload carries no
	// socket: the vhost is rewritten as a static site, which is what turning
	// PHP off means. The certificate and the aliases stay.
	delete(payload, "php_socket")
	payload["version"] = existing.PHPVersion
	payload["pool_name"] = existing.PoolName

	job, err := s.jobs.Create(ctx, jobs.CreateParams{
		Type:         jobs.TypeWebsitePHPUnset,
		Payload:      payload,
		CreatedBy:    actor.UserID,
		ResourceType: ResourceTypeWebsite,
		ResourceID:   site.ID,
	})
	if err != nil {
		return jobs.Job{}, err
	}

	s.record(ctx, actor, ActionWebsitePHPSet, site.ID, map[string]any{
		"domain": site.PrimaryDomain, "version": "", "job_id": job.ID,
	})
	return job, nil
}

// resolvedSettings are the values a pool is written with.
type resolvedSettings struct {
	MemoryLimit       string
	UploadMaxFilesize string
	MaxExecutionTime  int
	OPcacheEnabled    bool
	MaxChildren       int
}

// Defaults applied when a site chooses nothing. They match the Agent's own
// defaults; the two must agree or a site's stored settings would not describe
// what is actually running.
const (
	DefaultMemoryLimit       = "256M"
	DefaultUploadMaxFilesize = "64M"
	DefaultMaxExecutionTime  = 30
)

// resolveSettings merges a request over what the site already has.
//
// A PATCH that changes only memory_limit must not silently reset every other
// value to a default, so unset fields keep whatever the pool already holds.
func (s *Service) resolveSettings(ctx context.Context, websiteID string, requested Settings) (resolvedSettings, error) {
	current := resolvedSettings{
		MemoryLimit:       DefaultMemoryLimit,
		UploadMaxFilesize: DefaultUploadMaxFilesize,
		MaxExecutionTime:  DefaultMaxExecutionTime,
		OPcacheEnabled:    true,
	}

	existing, err := s.repo.GetPool(ctx, websiteID)
	switch {
	case err == nil:
		if existing.MemoryLimit != nil {
			current.MemoryLimit = *existing.MemoryLimit
		}
		if existing.UploadMaxFilesize != nil {
			current.UploadMaxFilesize = *existing.UploadMaxFilesize
		}
		if existing.MaxExecutionTime != nil {
			current.MaxExecutionTime = *existing.MaxExecutionTime
		}
		if existing.MaxChildren != nil {
			current.MaxChildren = *existing.MaxChildren
		}
		current.OPcacheEnabled = existing.OPcacheEnabled
	case errors.Is(err, ErrPoolNotFound):
		// First time: the defaults above stand.
	default:
		return resolvedSettings{}, err
	}

	if requested.MemoryLimit != nil {
		current.MemoryLimit = *requested.MemoryLimit
	}
	if requested.UploadMaxFilesize != nil {
		current.UploadMaxFilesize = *requested.UploadMaxFilesize
	}
	if requested.MaxExecutionTime != nil {
		current.MaxExecutionTime = *requested.MaxExecutionTime
	}
	if requested.OPcacheEnabled != nil {
		current.OPcacheEnabled = *requested.OPcacheEnabled
	}
	if requested.MaxChildren != nil {
		current.MaxChildren = *requested.MaxChildren
	}

	// Validated here, before anything is stored, so a bad value is a request
	// error rather than a job that fails on the host minutes later.
	if err := validate.PHPMemoryLimit(current.MemoryLimit); err != nil {
		return resolvedSettings{}, err
	}
	if err := validate.PHPUploadSize(current.UploadMaxFilesize); err != nil {
		return resolvedSettings{}, err
	}
	if err := validate.PHPExecutionTime(current.MaxExecutionTime); err != nil {
		return resolvedSettings{}, err
	}
	return current, nil
}

// record writes an audit event, logging rather than failing the request.
func (s *Service) record(ctx context.Context, actor Actor, action, resourceID string, metadata map[string]any) {
	if s.audit == nil {
		return
	}
	resourceType := ResourceTypeVersion
	if resourceID != "" {
		resourceType = ResourceTypeWebsite
	}

	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       actor.UserID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		IPAddress:    actor.IPAddress,
		UserAgent:    actor.UserAgent,
		Status:       audit.StatusSuccess,
		Metadata:     metadata,
	})
}
