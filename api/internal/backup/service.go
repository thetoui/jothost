package backup

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/api/internal/secrets"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/validate"
)

// Audit actions.
//
// Every one of these is on CLAUDE.md section 15's list or a near neighbour of
// it. Restoring is there because it overwrites live data; deleting is there
// because "where did last month's backups go" needs an answer that is not a
// shrug.
const (
	ActionBackupCreate      = "backup.create"
	ActionBackupDelete      = "backup.delete"
	ActionBackupRestore     = "backup.restore"
	ActionBackupVerify      = "backup.verify"
	ActionDestinationCreate = "backup.destination.create"
	ActionDestinationUpdate = "backup.destination.update"
	ActionDestinationDelete = "backup.destination.delete"
	ActionScheduleCreate    = "backup.schedule.create"
	ActionScheduleUpdate    = "backup.schedule.update"
	ActionScheduleDelete    = "backup.schedule.delete"

	ResourceTypeBackup      = "backup"
	ResourceTypeDestination = "backup_destination"
	ResourceTypeSchedule    = "backup_schedule"
)

// Job types. They mirror the Agent's operation names so a job and the audit
// record it produces line up.
const (
	JobTypeBackupCreate  = "backup.create"
	JobTypeBackupRestore = "backup.restore"
)

// The encryption context binds a ciphertext to the row it belongs to, so a
// credential moved from another destination fails to decrypt rather than
// quietly authenticating somewhere it should not.
const encryptionContext = "backup_destination"

// Errors returned by the service.
var (
	// ErrUnavailable means the host cannot take backups at all.
	ErrUnavailable = errors.New("this host cannot take backups")
	// ErrNothingToBackUp means the request named nothing that exists.
	ErrNothingToBackUp = errors.New("there is nothing to back up")
	// ErrNotRestorable means the backup is not one the panel will restore.
	ErrNotRestorable = errors.New(
		"only a backup the panel has read back and confirmed can be restored")
	// ErrNotConfirmed means a restore arrived without its confirmation.
	ErrNotConfirmed = errors.New("a restore has to be confirmed")
	// ErrInvalidDestination covers a destination the panel will not accept.
	ErrInvalidDestination = errors.New("invalid backup destination")
	// ErrDestinationUnchecked means a destination has never been reached.
	ErrDestinationUnchecked = errors.New(
		"this destination has never been reached; check it before relying on it")
	// ErrPanelNeedsServerManage means a panel backup was asked for by somebody
	// without server.manage.
	ErrPanelNeedsServerManage = errors.New(
		"a backup of the panel itself needs server.manage as well as backup.manage")
	// ErrPanelRestoreOnHost means somebody asked the panel to restore its own
	// database.
	ErrPanelRestoreOnHost = errors.New(
		"a panel backup is restored from the host, with the panel stopped: " +
			"run install.sh restore-panel (see docs/RECOVERY.md)")
)

// requirePanelAuthority refuses a panel backup to anybody without
// server.manage.
//
// backup.manage alone is granted to operators, and that role is deliberately
// withheld user management and server configuration. A panel backup holds
// every account's password hash and every stored credential; letting an
// operator take, verify, delete or schedule one would hand them everything
// their role withholds. Other backup types are unaffected.
func requirePanelAuthority(actor Actor, backupType string) error {
	if backupType == validate.BackupPanel && !actor.CanManageServer {
		return ErrPanelNeedsServerManage
	}
	return nil
}

// Actor is who asked, for the audit trail.
type Actor struct {
	UserID    string
	IPAddress string
	UserAgent string
	// CanManageServer says whether the actor holds server.manage, which a
	// panel backup needs on top of backup.manage (requirePanelAuthority).
	CanManageServer bool
}

// Sites is what this package needs to know about websites.
//
// An interface with two methods rather than a dependency on the websites
// package: a backup needs a domain, a document root, a system account and the
// databases attached to a site, and nothing else. Depending on the whole
// package would make either impossible to change without the other.
type Sites interface {
	// SiteFor returns one website's backup subject.
	SiteFor(ctx context.Context, websiteID string) (Site, error)
	// AllSites returns every website on the server.
	AllSites(ctx context.Context) ([]Site, error)
}

// Databases is what this package needs to know about databases.
type Databases interface {
	// DatabaseFor returns one database.
	DatabaseFor(ctx context.Context, databaseID string) (DatabaseRef, error)
	// DatabasesForWebsite returns the databases attached to a website.
	DatabasesForWebsite(ctx context.Context, websiteID string) ([]DatabaseRef, error)
	// AllDatabases returns every database on the server.
	AllDatabases(ctx context.Context) ([]DatabaseRef, error)
}

// Site is one website, as this package needs it.
type Site struct {
	ID           string
	Domain       string
	DocumentRoot string
	SystemUser   string
}

// DatabaseRef is one database, as this package needs it.
type DatabaseRef struct {
	ID     string
	Name   string
	Engine string
}

// Service takes, verifies, restores and prunes backups.
type Service struct {
	repo      *Repository
	jobs      *jobs.Repository
	agent     *agentclient.Client
	audit     *audit.Recorder
	crypto    *secrets.Encrypter
	sites     Sites
	databases Databases
	log       *slog.Logger
	notifier  Notifier
	serverID  string
	now       func() time.Time
}

// ServiceOptions configure a Service.
type ServiceOptions struct {
	Repository *Repository
	Jobs       *jobs.Repository
	Agent      *agentclient.Client
	Audit      *audit.Recorder
	Crypto     *secrets.Encrypter
	Sites      Sites
	Databases  Databases
	Log        *slog.Logger
	// Notifier is told when a backup fails. Nil is normal.
	Notifier Notifier
	ServerID string
	Now      func() time.Time
}

// NewService builds a Service.
func NewService(opts ServiceOptions) *Service {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		repo:      opts.Repository,
		jobs:      opts.Jobs,
		agent:     opts.Agent,
		audit:     opts.Audit,
		crypto:    opts.Crypto,
		sites:     opts.Sites,
		databases: opts.Databases,
		log:       log,
		notifier:  opts.Notifier,
		serverID:  opts.ServerID,
		now:       now,
	}
}

// Overview is what the backup page shows.
type Overview struct {
	Capabilities agentclient.BackupCapabilities `json:"capabilities"`
	Backups      []Backup                       `json:"backups"`
	Destinations []Destination                  `json:"destinations"`
	Schedules    []Schedule                     `json:"schedules"`
	Stats        Stats                          `json:"stats"`
	Types        []string                       `json:"types"`
	Kinds        []string                       `json:"destination_kinds"`
}

// Overview reads everything the page needs in one call.
func (s *Service) Overview(ctx context.Context, requestID string, limit int) (Overview, error) {
	overview := Overview{
		Types: validate.BackupTypes,
		Kinds: validate.DestinationKinds,
	}

	capabilities, err := s.agent.BackupCapabilities(ctx, requestID)
	if err != nil {
		// A host that cannot be asked is reported as one that cannot take
		// backups, with the reason. Showing an empty page instead would read as
		// "nothing is configured", which is a very different thing.
		s.log.Warn("could not read the host's backup capabilities", logger.KeyError, err.Error())
		capabilities = agentclient.BackupCapabilities{
			Available: false,
			Reason:    "the host agent could not be reached",
		}
	}
	overview.Capabilities = capabilities

	if overview.Backups, err = s.repo.ListBackups(ctx,
		ListBackupParams{ServerID: s.serverID, Limit: limit}); err != nil {
		return Overview{}, err
	}
	if overview.Destinations, err = s.repo.ListDestinations(ctx, s.serverID); err != nil {
		return Overview{}, err
	}
	if overview.Schedules, err = s.repo.ListSchedules(ctx, s.serverID); err != nil {
		return Overview{}, err
	}
	if overview.Stats, err = s.repo.Stats(ctx, s.serverID); err != nil {
		return Overview{}, err
	}
	return overview, nil
}

// ListBackups returns backups matching a filter.
func (s *Service) ListBackups(ctx context.Context, params ListBackupParams) ([]Backup, error) {
	params.ServerID = s.serverID
	return s.repo.ListBackups(ctx, params)
}

// GetBackup returns one backup.
func (s *Service) GetBackup(ctx context.Context, id string) (Backup, error) {
	return s.repo.GetBackup(ctx, id)
}

// --------------------------------------------------------- taking a backup

// CreateRequest asks for a backup.
type CreateRequest struct {
	Type          string `json:"type"`
	WebsiteID     string `json:"website_id"`
	DatabaseID    string `json:"database_id"`
	DestinationID string `json:"destination_id"`
	// IncludeDatabases controls whether a website backup carries the databases
	// attached to it. It defaults to true at the handler, because a WordPress
	// site restored without its database is a site that is broken in a more
	// confusing way than one that is simply gone.
	IncludeDatabases bool `json:"include_databases"`
}

// Create queues a backup and returns the row it will fill in.
func (s *Service) Create(ctx context.Context, req CreateRequest, actor Actor) (Backup, error) {
	if err := validate.BackupType(req.Type); err != nil {
		return Backup{}, err
	}
	if err := requirePanelAuthority(actor, req.Type); err != nil {
		return Backup{}, err
	}
	return s.create(ctx, req, actor)
}

// create queues a backup without checking who asked.
//
// The scheduler comes through here: it acts for no one in particular, and a
// panel schedule was already refused to anybody without server.manage when it
// was created, changed or run by hand.
func (s *Service) create(ctx context.Context, req CreateRequest, actor Actor) (Backup, error) {
	if err := validate.BackupType(req.Type); err != nil {
		return Backup{}, err
	}
	destination, err := s.repo.GetDestination(ctx, req.DestinationID)
	if err != nil {
		return Backup{}, err
	}

	plan, err := s.plan(ctx, req)
	if err != nil {
		return Backup{}, err
	}

	created := s.now().UTC()
	item, err := s.repo.CreateBackup(ctx, CreateBackupParams{
		ServerID:        s.serverID,
		WebsiteID:       req.WebsiteID,
		DatabaseID:      req.DatabaseID,
		Subject:         plan.Subject,
		Type:            req.Type,
		DestinationID:   destination.ID,
		DestinationName: destination.Name,
		Key:             objectKey(req.Type, plan.Subject, created),
		CreatedBy:       actor.UserID,
	})
	if err != nil {
		return Backup{}, err
	}

	if err := s.dispatch(ctx, item, actor.UserID); err != nil {
		_ = s.repo.FailBackup(ctx, item.ID, err.Error())
		return Backup{}, err
	}

	s.record(ctx, actor, ActionBackupCreate, ResourceTypeBackup, item.ID, map[string]any{
		"type":        req.Type,
		"subject":     plan.Subject,
		"destination": destination.Name,
	})
	return s.repo.GetBackup(ctx, item.ID)
}

// plan works out what goes into a backup.
type plan struct {
	Subject   string
	Sites     []agentclient.BackupSite
	Databases []agentclient.BackupDatabase
}

func (s *Service) plan(ctx context.Context, req CreateRequest) (plan, error) {
	switch req.Type {
	case validate.BackupWebsite:
		if req.WebsiteID == "" {
			return plan{}, fmt.Errorf("%w: a website backup needs a website", ErrNothingToBackUp)
		}
		site, err := s.sites.SiteFor(ctx, req.WebsiteID)
		if err != nil {
			return plan{}, err
		}
		built := plan{
			Subject: site.Domain,
			Sites: []agentclient.BackupSite{{
				Domain:       site.Domain,
				DocumentRoot: site.DocumentRoot,
				SystemUser:   site.SystemUser,
			}},
		}
		if req.IncludeDatabases {
			refs, err := s.databases.DatabasesForWebsite(ctx, req.WebsiteID)
			if err != nil {
				return plan{}, err
			}
			for _, ref := range refs {
				built.Databases = append(built.Databases,
					agentclient.BackupDatabase{Engine: ref.Engine, Name: ref.Name})
			}
		}
		return built, nil

	case validate.BackupDatabase:
		if req.DatabaseID == "" {
			return plan{}, fmt.Errorf("%w: a database backup needs a database", ErrNothingToBackUp)
		}
		ref, err := s.databases.DatabaseFor(ctx, req.DatabaseID)
		if err != nil {
			return plan{}, err
		}
		return plan{
			Subject: ref.Name,
			Databases: []agentclient.BackupDatabase{
				{Engine: ref.Engine, Name: ref.Name},
			},
		}, nil

	case validate.BackupFull:
		sites, err := s.sites.AllSites(ctx)
		if err != nil {
			return plan{}, err
		}
		refs, err := s.databases.AllDatabases(ctx)
		if err != nil {
			return plan{}, err
		}
		if len(sites) == 0 && len(refs) == 0 {
			return plan{}, fmt.Errorf(
				"%w: this server has no websites and no databases", ErrNothingToBackUp)
		}
		built := plan{Subject: "server"}
		for _, site := range sites {
			built.Sites = append(built.Sites, agentclient.BackupSite{
				Domain:       site.Domain,
				DocumentRoot: site.DocumentRoot,
				SystemUser:   site.SystemUser,
			})
		}
		for _, ref := range refs {
			built.Databases = append(built.Databases,
				agentclient.BackupDatabase{Engine: ref.Engine, Name: ref.Name})
		}
		return built, nil

	case validate.BackupPanel:
		// No sites and no databases: what a panel backup dumps is fixed by the
		// Agent's own configuration, never by the request, so the type cannot
		// be used to dump an arbitrary database under an administrator's name.
		return plan{Subject: "panel"}, nil

	default:
		return plan{}, validate.BackupType(req.Type)
	}
}

// dispatch queues the job that will take a backup.
//
// The payload holds the backup's id and nothing that identifies a credential.
// What the Agent actually needs — the destination, the sites, the databases — is
// rebuilt by ResolvePayload at dispatch, which is what keeps an S3 secret key
// out of a table that gets backed up and replicated.
func (s *Service) dispatch(ctx context.Context, item Backup, userID string) error {
	job, err := s.jobs.Create(ctx, jobs.CreateParams{
		Type:         JobTypeBackupCreate,
		Payload:      map[string]any{"backup_id": item.ID},
		CreatedBy:    userID,
		ResourceType: ResourceTypeBackup,
		ResourceID:   item.ID,
	})
	if err != nil {
		return err
	}
	return s.repo.AttachJob(ctx, item.ID, job.ID)
}

// ResolvePayload builds the payload for a backup or restore job.
//
// This is the jobs.PayloadResolver hook, and it exists so the queue never holds
// a credential. It runs at dispatch, decrypts the destination once, and the
// plaintext lives only as long as the call to the Agent.
func (s *Service) ResolvePayload(ctx context.Context, job jobs.Job) (map[string]any, error) {
	switch job.Type {
	case JobTypeBackupCreate:
		return s.createPayload(ctx, job)
	case JobTypeBackupRestore:
		return s.restorePayload(ctx, job)
	default:
		return nil, nil
	}
}

func (s *Service) createPayload(ctx context.Context, job jobs.Job) (map[string]any, error) {
	id, _ := job.Payload["backup_id"].(string)
	if id == "" {
		return nil, errors.New("this backup job names no backup")
	}
	item, err := s.repo.GetBackup(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.DestinationID == nil || item.Path == nil {
		return nil, errors.New("this backup has no destination")
	}

	destination, err := s.agentDestination(ctx, *item.DestinationID)
	if err != nil {
		return nil, err
	}

	built, err := s.plan(ctx, CreateRequest{
		Type:             item.Type,
		WebsiteID:        stringValue(item.WebsiteID),
		DatabaseID:       stringValue(item.DatabaseID),
		IncludeDatabases: true,
	})
	if err != nil {
		return nil, err
	}

	payload := map[string]any{
		"type":        item.Type,
		"key":         *item.Path,
		"subject":     item.Subject,
		"sites":       built.Sites,
		"databases":   built.Databases,
		"destination": destination,
	}
	if item.Type == validate.BackupPanel {
		// Added here, at dispatch, for the same reason the destination's
		// credentials are: the job row never holds it, and it exists in
		// plaintext only for the length of the call to the Agent.
		payload["sealing_key"] = s.sealingKey()
	}
	return payload, nil
}

// sealingKey is the key a panel backup is sealed with, as the Agent takes it.
func (s *Service) sealingKey() string {
	return hex.EncodeToString(s.crypto.PanelBackupKey())
}

// agentDestination decrypts a destination for one call to the Agent.
func (s *Service) agentDestination(ctx context.Context, id string) (
	agentclient.BackupDestination, error,
) {
	stored, err := s.repo.GetDestination(ctx, id)
	if err != nil {
		return agentclient.BackupDestination{}, err
	}

	dest := agentclient.BackupDestination{Kind: stored.Kind}
	applyConfig(&dest, stored.Config)

	if stored.HasCredentials {
		encrypted, err := s.repo.DestinationSecret(ctx, id)
		if err != nil {
			return agentclient.BackupDestination{}, err
		}
		plaintext, err := s.crypto.Decrypt(encrypted, encryptionContext+":"+id)
		if err != nil {
			// The error is deliberately not wrapped with the underlying
			// cryptographic failure, which says nothing useful and could say
			// something about the key.
			return agentclient.BackupDestination{},
				fmt.Errorf("%w: its stored credentials could not be read", ErrInvalidDestination)
		}
		var credentials credentialFields
		if err := json.Unmarshal(plaintext, &credentials); err != nil {
			return agentclient.BackupDestination{},
				fmt.Errorf("%w: its stored credentials could not be read", ErrInvalidDestination)
		}
		dest.SecretKey = credentials.SecretKey
		dest.PrivateKey = credentials.PrivateKey
	}
	return dest, nil
}

// credentialFields is what is stored encrypted.
//
// The access key is *not* here: it is an identifier, not a secret, it appears in
// every request's Authorization header anyway, and a page needs to show which
// key a destination uses. Only the halves that grant access are encrypted.
type credentialFields struct {
	SecretKey  string `json:"secret_key,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
}

// applyConfig copies a destination's non-secret configuration.
func applyConfig(dest *agentclient.BackupDestination, config map[string]any) {
	dest.Directory = configString(config, "directory")
	dest.Endpoint = configString(config, "endpoint")
	dest.Region = configString(config, "region")
	dest.Bucket = configString(config, "bucket")
	dest.Prefix = configString(config, "prefix")
	dest.AccessKey = configString(config, "access_key")
	dest.PathStyle = configBool(config, "path_style")
	dest.AllowInsecure = configBool(config, "allow_insecure")
	dest.Host = configString(config, "host")
	dest.Port = configInt(config, "port")
	dest.User = configString(config, "user")
	dest.Path = configString(config, "path")
	dest.HostKey = configString(config, "host_key")
}

func configString(config map[string]any, key string) string {
	value, _ := config[key].(string)
	return value
}

func configBool(config map[string]any, key string) bool {
	value, _ := config[key].(bool)
	return value
}

func configInt(config map[string]any, key string) int {
	switch value := config[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	default:
		return 0
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// objectKey builds the key an archive is stored under.
func objectKey(kind, subject string, at time.Time) string {
	return fmt.Sprintf("%s/%s/%s/%s.tar.gz",
		keySegment(kind), keySegment(subject),
		at.UTC().Format("2006-01-02"), at.UTC().Format("150405"))
}

// keySegment reduces a value to what validate.BackupKey accepts.
func keySegment(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_':
			builder.WriteRune(r)
		case r == '.':
			if builder.Len() > 0 && !strings.HasSuffix(builder.String(), ".") {
				builder.WriteRune('.')
			}
		default:
			builder.WriteRune('-')
		}
	}
	trimmed := strings.Trim(builder.String(), ".-")
	if trimmed == "" {
		return "unnamed"
	}
	if len(trimmed) > 80 {
		trimmed = strings.Trim(trimmed[:80], ".-")
	}
	return trimmed
}

// record writes an audit entry, logging rather than failing when it cannot.
func (s *Service) record(ctx context.Context, actor Actor, action, resourceType,
	resourceID string, details map[string]any,
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
		Metadata:     details,
	})
}
