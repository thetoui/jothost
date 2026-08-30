package websites

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/validate"
)

// Audit action names for subdomain work.
const (
	ActionSubdomainCreate = "subdomain.create"
	ActionSubdomainDelete = "subdomain.delete"
)

// Errors returned by subdomain operations.
var (
	// ErrNotSubdomain means the site addressed is a top-level website. The two
	// have different deletion rules, so the endpoints are kept apart rather
	// than one silently doing the other's job.
	ErrNotSubdomain = errors.New("that website is not a subdomain")
	// ErrHasSubdomains means a website still has sites beneath it.
	ErrHasSubdomains = errors.New("the website still has subdomains")
	// ErrParentNotReady means the parent site is not in a state a subdomain
	// can be added to.
	ErrParentNotReady = errors.New("the parent website is not ready")
)

// WildcardDirectory is the directory name used for a wildcard subdomain.
//
// "*.example.com" cannot be a directory name: the asterisk is a shell glob, so
// every later command touching that path — a backup, an archive, an rsync —
// would expand it into whatever else happens to be in the parent directory.
// The name on disk is therefore literal and the wildcard lives only in the
// vhost's server_name, where it means something.
const WildcardDirectory = "_wildcard"

// CreateSubdomainRequest is a request to host a site beneath another.
type CreateSubdomainRequest struct {
	ParentID string
	// Name is the label beneath the parent: "shop", or "dev.shop", or "*" for
	// a wildcard. The full hostname is derived from it and the parent's
	// domain, so a caller cannot name a site outside the parent it claims to
	// be creating under.
	Name string
	// DocumentRootMode is "nested" or "isolated". Empty means nested, which
	// keeps a subdomain's files with its parent's — the arrangement an
	// operator expects when both belong to one customer.
	DocumentRootMode string
	// PHPPoolMode is "inherit" or "dedicated". Empty means inherit: a
	// subdomain shares its parent's PHP until someone decides otherwise, which
	// is both the cheaper default and the one that needs no extra pool.
	PHPPoolMode string
	// SystemUserMode is "inherit" or "dedicated". Empty means inherit, so the
	// parent's file manager and FTP login reach the subdomain's files.
	SystemUserMode string

	Actor     string
	IPAddress string
	UserAgent string
}

// CreateSubdomain records a subdomain and queues the work to provision it.
//
// A subdomain is a website row with a parent (migration 0010), so what happens
// after this point — provisioning, PHP, certificates, files, databases — is
// the same code that serves any other site.
func (s *Service) CreateSubdomain(ctx context.Context, req CreateSubdomainRequest) (CreateResult, error) {
	parent, err := s.repo.Get(ctx, req.ParentID)
	if err != nil {
		return CreateResult{}, err
	}
	if parent.IsSubdomain() {
		return CreateResult{}, ErrNestedSubdomain
	}
	// A parent still being provisioned has no directory for a nested
	// subdomain to live in, and one being deleted is about to take it away.
	if parent.Status != StatusActive && parent.Status != StatusFailed {
		return CreateResult{}, fmt.Errorf("%w: it is %s", ErrParentNotReady, parent.Status)
	}

	domain := validate.NormalizeDomain(
		validate.SubdomainName(req.Name, parent.PrimaryDomain))
	if err := validate.Subdomain(domain, parent.PrimaryDomain); err != nil {
		return CreateResult{}, err
	}

	rootMode := req.DocumentRootMode
	if rootMode == "" {
		rootMode = validate.DocumentRootNested
	}
	if err := validate.DocumentRootMode(rootMode); err != nil {
		return CreateResult{}, err
	}

	poolMode := req.PHPPoolMode
	if poolMode == "" {
		poolMode = validate.PHPPoolInherit
	}
	if err := validate.PHPPoolMode(poolMode); err != nil {
		return CreateResult{}, err
	}

	userMode := req.SystemUserMode
	if userMode == "" {
		userMode = validate.SystemUserInherit
	}
	if err := validate.SystemUserMode(userMode); err != nil {
		return CreateResult{}, err
	}

	// A dedicated account with an inherited pool would run PHP as the parent's
	// user over files owned by the subdomain's account: every write from PHP
	// fails, and it reads as a broken application rather than a bad choice
	// made here. The database refuses the combination too; this is where the
	// user gets told why.
	if userMode == validate.SystemUserDedicated && poolMode == validate.PHPPoolInherit {
		return CreateResult{}, fmt.Errorf(
			"%w: a subdomain with its own system account needs its own PHP pool, "+
				"because the parent's pool runs as the parent's user", ErrInvalidState)
	}

	systemUser := parent.SystemUser
	if userMode == validate.SystemUserDedicated {
		systemUser, err = deriveSystemUser(DirectoryName(domain))
		if err != nil {
			return CreateResult{}, err
		}
	}

	documentRoot := SubdomainDocumentRoot(parent, domain, rootMode)

	site, err := s.repo.Create(ctx, CreateParams{
		ServerID:         s.serverID,
		PrimaryDomain:    domain,
		DocumentRoot:     documentRoot,
		SystemUser:       systemUser,
		ParentWebsiteID:  parent.ID,
		DocumentRootMode: rootMode,
		PHPPoolMode:      poolMode,
		SystemUserMode:   userMode,
	})
	if err != nil {
		return CreateResult{}, err
	}

	site, err = s.joinArrangement(ctx, site)
	if err != nil {
		return CreateResult{}, err
	}

	// The payload is built from the record rather than by hand, so an
	// inherited PHP pool is already in it: a subdomain of a PHP site serves
	// PHP from the moment it is created, which is what inheriting means.
	payload, err := s.repo.VhostPayload(ctx, site)
	if err != nil {
		return CreateResult{}, err
	}

	job, err := s.jobs.Create(ctx, jobs.CreateParams{
		Type:         jobs.TypeWebsiteCreate,
		Payload:      payload,
		CreatedBy:    req.Actor,
		ResourceType: ResourceTypeWebsite,
		ResourceID:   site.ID,
	})
	if err != nil {
		if statusErr := s.repo.SetStatus(ctx, site.ID, StatusFailed); statusErr != nil {
			s.log.Error("failed to mark unqueued subdomain as failed",
				"website_id", site.ID, logger.KeyError, statusErr.Error())
		}
		return CreateResult{}, err
	}

	s.record(ctx, req.Actor, ActionSubdomainCreate, site.ID, audit.StatusSuccess,
		map[string]any{
			"domain":             domain,
			"parent_website_id":  parent.ID,
			"parent_domain":      parent.PrimaryDomain,
			"document_root_mode": rootMode,
			"php_pool_mode":      poolMode,
			"system_user_mode":   userMode,
			"job_id":             job.ID,
		}, req.IPAddress, req.UserAgent)

	return CreateResult{Website: site, Job: job}, nil
}

// SubdomainDocumentRoot computes where a subdomain's files live.
//
// Nested puts them beside the parent's content rather than inside it. Inside
// would mean the parent serves the subdomain's files as its own URLs — so
// https://example.com/shop/config.php would hand out the subdomain's database
// password to anyone who guessed the path.
func SubdomainDocumentRoot(parent Website, domain, mode string) string {
	directory := DirectoryName(domain)

	if mode == validate.DocumentRootIsolated {
		return path.Join(SiteRoot, directory, ContentDir)
	}

	// The parent's site root is its document root's parent: /var/www/example.com
	// for a document root of /var/www/example.com/public.
	parentRoot := path.Dir(parent.DocumentRoot)
	return path.Join(parentRoot, directory, ContentDir)
}

// DirectoryName is the on-disk name for a hostname.
//
// It differs from the hostname only for a wildcard, where the asterisk would
// be a glob rather than a character.
func DirectoryName(domain string) string {
	if !validate.IsWildcard(domain) {
		return domain
	}
	return WildcardDirectory + "." + strings.TrimPrefix(domain, validate.WildcardPrefix)
}

// DeleteSubdomain queues removal of a subdomain.
//
// It is separate from Delete because the two answer differently on the one
// question that matters: whether the system account goes with the site. A
// subdomain that inherits its parent's account must never take it away —
// deleting a subdomain would otherwise leave the parent's files owned by a
// user that no longer exists, and the parent serving 403 to every visitor.
func (s *Service) DeleteSubdomain(ctx context.Context, req DeleteRequest) (jobs.Job, error) {
	site, err := s.repo.Get(ctx, req.WebsiteID)
	if err != nil {
		return jobs.Job{}, err
	}
	if !site.IsSubdomain() {
		return jobs.Job{}, ErrNotSubdomain
	}
	if site.Status == StatusDeleting {
		return jobs.Job{}, fmt.Errorf("%w: it is already being deleted", ErrInvalidState)
	}

	if err := s.repo.SetStatus(ctx, site.ID, StatusDeleting); err != nil {
		return jobs.Job{}, err
	}

	job, err := s.jobs.Create(ctx, jobs.CreateParams{
		Type: jobs.TypeWebsiteDelete,
		Payload: map[string]any{
			"domain":        site.PrimaryDomain,
			"document_root": site.DocumentRoot,
			"system_user":   site.SystemUser,
			// The account is removed only when this subdomain owns it.
			"remove_user": !site.InheritsSystemUser(),
		},
		CreatedBy:    req.Actor,
		ResourceType: ResourceTypeWebsite,
		ResourceID:   site.ID,
	})
	if err != nil {
		if statusErr := s.repo.SetStatus(ctx, site.ID, site.Status); statusErr != nil {
			s.log.Error("failed to restore subdomain status after a queue failure",
				"website_id", site.ID, logger.KeyError, statusErr.Error())
		}
		return jobs.Job{}, err
	}

	s.record(ctx, req.Actor, ActionSubdomainDelete, site.ID, audit.StatusSuccess,
		map[string]any{"domain": site.PrimaryDomain, "job_id": job.ID},
		req.IPAddress, req.UserAgent)

	return job, nil
}

// ListSubdomains returns the sites beneath a website.
func (s *Service) ListSubdomains(ctx context.Context, parentID string) ([]Website, error) {
	parent, err := s.repo.Get(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if parent.IsSubdomain() {
		// Not an error: a subdomain simply has none, and answering with an
		// empty list is more useful to a page than a 400 would be.
		return []Website{}, nil
	}
	return s.repo.ListSubdomains(ctx, parent.ID)
}
