package security

import (
	"context"

	"github.com/jothost/panel/api/internal/updates"
	"github.com/jothost/panel/api/internal/websites"
)

// The adapters between this package's small interfaces and the repositories
// that answer them.
//
// They live here rather than in the packages being adapted because the
// dependency belongs this way round: the Security Center needs to know what a
// website and an update check are, and neither needs to know it is scanned.

// WebsiteRepository is the part of the websites repository this needs.
type WebsiteRepository interface {
	List(ctx context.Context, params websites.ListParams) ([]websites.Website, error)
}

// WebsiteAdapter answers the Websites interface.
type WebsiteAdapter struct {
	repo WebsiteRepository
}

// NewWebsiteAdapter builds a WebsiteAdapter.
func NewWebsiteAdapter(repo WebsiteRepository) *WebsiteAdapter {
	return &WebsiteAdapter{repo: repo}
}

// List returns the sites whose certificate coverage is worth checking.
//
// Subdomains are included, because a subdomain is a website row with its own
// vhost and its own visitors, and one served over plain HTTP is exactly as
// exposed as a top-level site would be.
//
// A site still being created or on its way out is skipped: neither is serving
// anything yet, and reporting "no certificate" for a site that is three seconds
// old would put a finding on the page for every site anybody makes.
func (a *WebsiteAdapter) List(ctx context.Context) ([]Site, error) {
	found, err := a.repo.List(ctx, websites.ListParams{
		IncludeSubdomains: true,
		Limit:             websites.MaxListLimit,
	})
	if err != nil {
		return nil, err
	}

	sites := make([]Site, 0, len(found))
	for _, site := range found {
		if site.Status == "creating" || site.Status == "deleting" {
			continue
		}
		sites = append(sites, Site{ID: site.ID, Domain: site.PrimaryDomain})
	}
	return sites, nil
}

// UpdateRepository is the part of the updates repository this needs.
type UpdateRepository interface {
	LatestCheck(ctx context.Context, serverID string) (updates.Check, error)
}

// UpdateAdapter answers the Updates interface.
type UpdateAdapter struct {
	repo     UpdateRepository
	serverID string
}

// NewUpdateAdapter builds an UpdateAdapter.
func NewUpdateAdapter(repo UpdateRepository, serverID string) *UpdateAdapter {
	return &UpdateAdapter{repo: repo, serverID: serverID}
}

// LatestCheck returns what the host last said about its outstanding packages.
//
// Succeeded is carried through untouched, and it is the field that matters. A
// check that could not reach the repositories produces an empty package list
// that reads exactly like a host with nothing to do — which is why Phase 21
// stores the distinction, and why throwing it away here would undo that work.
func (a *UpdateAdapter) LatestCheck(ctx context.Context) (UpdateCheck, error) {
	check, err := a.repo.LatestCheck(ctx, a.serverID)
	if err != nil {
		return UpdateCheck{}, err
	}
	return UpdateCheck{
		Succeeded:      check.Succeeded,
		Reason:         check.Reason,
		SecurityKnown:  check.SecurityKnown,
		SecurityCount:  check.SecurityCount,
		PackageCount:   check.PackageCount,
		RebootRequired: check.RebootRequired,
	}, nil
}
