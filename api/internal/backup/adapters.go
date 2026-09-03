package backup

import (
	"context"
	"fmt"

	"github.com/jothost/panel/api/internal/databases"
	"github.com/jothost/panel/api/internal/websites"
)

// The adapters between this package's two small interfaces and the repositories
// that answer them.
//
// They live here rather than in the websites and databases packages because the
// dependency belongs this way round: backups need to know what a website is, and
// a website does not need to know it is backed up. Putting the adapter in the
// other package would make it impossible to change either without the other.

// SiteRepository is the part of the websites repository this needs.
type SiteRepository interface {
	Get(ctx context.Context, id string) (websites.Website, error)
	List(ctx context.Context, params websites.ListParams) ([]websites.Website, error)
}

// SiteAdapter answers the Sites interface from the websites repository.
type SiteAdapter struct {
	repo SiteRepository
}

// NewSiteAdapter builds a SiteAdapter.
func NewSiteAdapter(repo SiteRepository) *SiteAdapter { return &SiteAdapter{repo: repo} }

// SiteFor returns one website.
func (a *SiteAdapter) SiteFor(ctx context.Context, websiteID string) (Site, error) {
	if websiteID == "" {
		return Site{}, fmt.Errorf("%w: no website was named", ErrNothingToBackUp)
	}
	site, err := a.repo.Get(ctx, websiteID)
	if err != nil {
		return Site{}, err
	}
	return Site{
		ID:           site.ID,
		Domain:       site.PrimaryDomain,
		DocumentRoot: site.DocumentRoot,
		SystemUser:   site.SystemUser,
	}, nil
}

// AllSites returns every website, subdomains included.
//
// Subdomains are included where the websites page excludes them, and that is
// deliberate: a subdomain is a website row with its own document root, and a
// full backup that skipped them would silently leave out a whole class of site.
// A page that hides them is making a listing readable; a backup that hides them
// is losing data.
func (a *SiteAdapter) AllSites(ctx context.Context) ([]Site, error) {
	found, err := a.repo.List(ctx, websites.ListParams{
		IncludeSubdomains: true,
		Limit:             websites.MaxListLimit,
	})
	if err != nil {
		return nil, err
	}

	sites := make([]Site, 0, len(found))
	for _, site := range found {
		// A site still being created has no document root on disk yet, and one
		// being deleted is on its way out. Backing up either would archive a
		// half-built directory and record it as a copy of a working site.
		if site.Status == "creating" || site.Status == "deleting" {
			continue
		}
		// A subdomain that shares its parent's document root would be archived
		// twice under two names, and restored twice over the same files. One
		// copy of a directory per backup.
		if site.DocumentRootMode != nil && *site.DocumentRootMode == "shared" {
			continue
		}
		sites = append(sites, Site{
			ID:           site.ID,
			Domain:       site.PrimaryDomain,
			DocumentRoot: site.DocumentRoot,
			SystemUser:   site.SystemUser,
		})
	}
	return sites, nil
}

// DatabaseRepository is the part of the databases repository this needs.
type DatabaseRepository interface {
	Get(ctx context.Context, id string) (databases.Database, error)
	List(ctx context.Context) ([]databases.Database, error)
}

// DatabaseAdapter answers the Databases interface.
type DatabaseAdapter struct {
	repo DatabaseRepository
}

// NewDatabaseAdapter builds a DatabaseAdapter.
func NewDatabaseAdapter(repo DatabaseRepository) *DatabaseAdapter {
	return &DatabaseAdapter{repo: repo}
}

// DatabaseFor returns one database.
func (a *DatabaseAdapter) DatabaseFor(ctx context.Context, databaseID string) (DatabaseRef, error) {
	if databaseID == "" {
		return DatabaseRef{}, fmt.Errorf("%w: no database was named", ErrNothingToBackUp)
	}
	database, err := a.repo.Get(ctx, databaseID)
	if err != nil {
		return DatabaseRef{}, err
	}
	return DatabaseRef{ID: database.ID, Name: database.Name, Engine: database.Engine}, nil
}

// DatabasesForWebsite returns the databases attached to a website.
func (a *DatabaseAdapter) DatabasesForWebsite(ctx context.Context, websiteID string) (
	[]DatabaseRef, error,
) {
	all, err := a.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	refs := []DatabaseRef{}
	for _, database := range all {
		if database.WebsiteID == nil || *database.WebsiteID != websiteID {
			continue
		}
		if database.Status != "active" {
			continue
		}
		refs = append(refs, DatabaseRef{
			ID: database.ID, Name: database.Name, Engine: database.Engine,
		})
	}
	return refs, nil
}

// AllDatabases returns every database on the server.
func (a *DatabaseAdapter) AllDatabases(ctx context.Context) ([]DatabaseRef, error) {
	all, err := a.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	refs := []DatabaseRef{}
	for _, database := range all {
		if database.Status != "active" {
			continue
		}
		refs = append(refs, DatabaseRef{
			ID: database.ID, Name: database.Name, Engine: database.Engine,
		})
	}
	return refs, nil
}
