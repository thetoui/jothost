package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/validate"
)

// Restoring, verifying, deleting, and the destinations and schedules that go
// with them.

// Notifier is told when a backup fails.
//
// Only failures. A backup that succeeded is not news, and a channel that
// reported every nightly success is one whose messages nobody opens — including
// the night one of them says the opposite.
type Notifier interface {
	BackupFailed(ctx context.Context, backupID, subject, reason string)
}

// RestoreRequest asks for a backup to be put back.
type RestoreRequest struct {
	// Confirm must be the backup's own id.
	//
	// CLAUDE.md section 18 requires a restore to be confirmed explicitly, and a
	// boolean would not be one: a "confirm": true is something a script sets
	// once and forgets. Typing the id of the thing about to overwrite a live
	// site is a confirmation of *that* restore.
	Confirm string `json:"confirm"`
	// Sites and Databases narrow what is put back. Empty means everything the
	// archive holds that this panel can place.
	Sites     []string `json:"sites"`
	Databases []string `json:"databases"`
	// KeepPrevious leaves the displaced files on disk after a successful
	// restore, instead of deleting them.
	KeepPrevious bool `json:"keep_previous"`
}

// Restore queues putting a backup back.
func (s *Service) Restore(ctx context.Context, id string, req RestoreRequest, actor Actor) (
	jobs.Job, error,
) {
	item, err := s.repo.GetBackup(ctx, id)
	if err != nil {
		return jobs.Job{}, err
	}
	// Refused before the confirmation is even read, and to administrators
	// too. A panel replacing its own database while it is running on it is
	// the circularity docs/PANEL_BACKUP.md is designed around.
	if item.Type == validate.BackupPanel {
		return jobs.Job{}, ErrPanelRestoreOnHost
	}
	if strings.TrimSpace(req.Confirm) != item.ID {
		return jobs.Job{}, fmt.Errorf(
			"%w: send the backup's own id as the confirmation, because this overwrites live data",
			ErrNotConfirmed)
	}
	if !item.Restorable() {
		return jobs.Job{}, ErrNotRestorable
	}

	payload := map[string]any{
		"backup_id":     item.ID,
		"sites":         req.Sites,
		"databases":     req.Databases,
		"keep_previous": req.KeepPrevious,
	}
	job, err := s.jobs.Create(ctx, jobs.CreateParams{
		Type:         JobTypeBackupRestore,
		Payload:      payload,
		CreatedBy:    actor.UserID,
		ResourceType: ResourceTypeBackup,
		ResourceID:   item.ID,
	})
	if err != nil {
		return jobs.Job{}, err
	}

	s.record(ctx, actor, ActionBackupRestore, ResourceTypeBackup, item.ID, map[string]any{
		"subject":       item.Subject,
		"type":          item.Type,
		"destination":   item.DestinationName,
		"keep_previous": req.KeepPrevious,
		"job_id":        job.ID,
	})
	return job, nil
}

// restorePayload builds the Agent's restore payload at dispatch.
//
// What is restored is worked out here, from the archive's *manifest* rather
// than from the panel's current idea of what exists. A site deleted since the
// backup was taken has no row to look up, and refusing to restore it would make
// the panel useless in the one case a restore is most needed.
func (s *Service) restorePayload(ctx context.Context, job jobs.Job) (map[string]any, error) {
	id, _ := job.Payload["backup_id"].(string)
	if id == "" {
		return nil, errors.New("this restore job names no backup")
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

	manifest, err := manifestOf(item)
	if err != nil {
		return nil, err
	}

	wantedSites := stringSet(job.Payload["sites"])
	wantedDatabases := stringSet(job.Payload["databases"])

	sites := []agentclient.BackupSite{}
	for _, entry := range manifest.Sites {
		if len(wantedSites) > 0 && !wantedSites[entry.Domain] {
			continue
		}
		root := entry.DocumentRoot
		user := entry.SystemUser
		// If the site still exists, its *current* document root wins. A site
		// somebody moved must be restored where it lives now, not where it was
		// when the archive was written.
		if current, err := s.sites.SiteFor(ctx, stringValue(item.WebsiteID)); err == nil &&
			strings.EqualFold(current.Domain, entry.Domain) {
			root = current.DocumentRoot
			user = current.SystemUser
		}
		sites = append(sites, agentclient.BackupSite{
			Domain:       entry.Domain,
			DocumentRoot: root,
			SystemUser:   user,
		})
	}

	databases := []agentclient.BackupDatabase{}
	for _, member := range manifest.Databases {
		if len(wantedDatabases) > 0 && !wantedDatabases[member.Name] {
			continue
		}
		databases = append(databases,
			agentclient.BackupDatabase{Engine: member.Engine, Name: member.Name})
	}

	keepPrevious, _ := job.Payload["keep_previous"].(bool)

	return map[string]any{
		"key":           *item.Path,
		"checksum":      stringValue(item.Checksum),
		"sites":         sites,
		"databases":     databases,
		"keep_previous": keepPrevious,
		"destination":   destination,
	}, nil
}

// manifestOf decodes the manifest stored with a backup.
func manifestOf(item Backup) (agentclient.BackupManifest, error) {
	if len(item.Manifest) == 0 {
		return agentclient.BackupManifest{},
			errors.New("this backup has no manifest, so there is no record of what is in it")
	}
	encoded, err := json.Marshal(item.Manifest)
	if err != nil {
		return agentclient.BackupManifest{}, fmt.Errorf("encode manifest: %w", err)
	}
	var manifest agentclient.BackupManifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return agentclient.BackupManifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	return manifest, nil
}

// stringSet turns a payload list into a lookup.
func stringSet(value any) map[string]bool {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	set := map[string]bool{}
	for _, item := range items {
		if text, ok := item.(string); ok && text != "" {
			set[text] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// ---------------------------------------------------------------- verify

// Verify reads a stored backup back and records what it found.
//
// This is the operation that makes a backup listing worth reading a month
// later. Bit rot, a bucket somebody emptied, a retention rule on the storage
// provider's side: all three leave the panel's record saying "verified" from
// the day it was written, and only asking again finds them.
func (s *Service) Verify(ctx context.Context, id string, requestID string, actor Actor) (
	agentclient.BackupVerifyResult, error,
) {
	item, err := s.repo.GetBackup(ctx, id)
	if err != nil {
		return agentclient.BackupVerifyResult{}, err
	}
	if err := requirePanelAuthority(actor, item.Type); err != nil {
		return agentclient.BackupVerifyResult{}, err
	}
	if item.DestinationID == nil || item.Path == nil {
		return agentclient.BackupVerifyResult{}, fmt.Errorf(
			"%w: this backup never reached a destination", ErrNotRestorable)
	}

	destination, err := s.agentDestination(ctx, *item.DestinationID)
	if err != nil {
		return agentclient.BackupVerifyResult{}, err
	}

	var size int64
	if item.Size != nil {
		size = *item.Size
	}
	// A sealed archive is verified by opening it, which is what proves the key
	// it will be restored with still opens it - not only that its bytes arrived.
	sealingKey := ""
	if item.Type == validate.BackupPanel {
		sealingKey = s.sealingKey()
	}
	result, err := s.agent.BackupVerify(ctx, requestID, *item.Path,
		stringValue(item.Checksum), size, destination, sealingKey)
	if err != nil {
		return agentclient.BackupVerifyResult{}, err
	}

	if err := s.repo.RecordVerification(ctx, item.ID, result.OK, result.Detail); err != nil {
		return result, err
	}

	s.record(ctx, actor, ActionBackupVerify, ResourceTypeBackup, item.ID, map[string]any{
		"ok":      result.OK,
		"detail":  result.Detail,
		"subject": item.Subject,
	})
	return result, nil
}

// ---------------------------------------------------------------- delete

// Delete removes a backup from its destination and from the panel's record.
//
// The order is deliberate: the archive first, the row second. A row removed
// before the archive would leave an object nothing knows about, accruing
// storage charges forever with no way to find it again. An archive removed
// before the row leaves, in the worst case, a row saying something is there
// that is not — which the next verify reports.
func (s *Service) Delete(ctx context.Context, id, requestID string, actor Actor) error {
	item, err := s.repo.GetBackup(ctx, id)
	if err != nil {
		return err
	}
	if err := requirePanelAuthority(actor, item.Type); err != nil {
		return err
	}

	if item.DestinationID != nil && item.Path != nil {
		destination, err := s.agentDestination(ctx, *item.DestinationID)
		if err != nil {
			return err
		}
		if err := s.agent.BackupDelete(ctx, requestID, *item.Path, destination); err != nil {
			return err
		}
	}

	if err := s.repo.DeleteBackup(ctx, item.ID); err != nil {
		return err
	}

	s.record(ctx, actor, ActionBackupDelete, ResourceTypeBackup, item.ID, map[string]any{
		"subject":     item.Subject,
		"type":        item.Type,
		"destination": item.DestinationName,
	})
	return nil
}

// ---------------------------------------------------------- destinations

// DestinationInput is a destination as a request describes it.
type DestinationInput struct {
	Name string `json:"name"`
	Kind string `json:"kind"`

	Directory string `json:"directory"`

	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	PathStyle bool   `json:"path_style"`
	// AllowInsecure accepts a plain-http endpoint that is not on this
	// machine. It is a field of its own rather than something inferred from
	// the URL, so that accepting it is a decision somebody made.
	AllowInsecure bool `json:"allow_insecure"`

	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	Path       string `json:"path"`
	PrivateKey string `json:"private_key"`
	HostKey    string `json:"host_key"`
}

// CreateDestination validates and stores a destination.
func (s *Service) CreateDestination(ctx context.Context, input DestinationInput, actor Actor) (
	Destination, error,
) {
	config, credentials, err := s.buildDestination(input, true)
	if err != nil {
		return Destination{}, err
	}

	encrypted := ""
	if credentials != (credentialFields{}) {
		// Encrypted with a placeholder context first, then re-encrypted once
		// the row's id is known. That is one extra write, and it is what keeps
		// the binding to the row exact: a ciphertext bound to nothing could be
		// copied into another destination and would still decrypt.
		encrypted, err = s.encryptCredentials(credentials, "pending")
		if err != nil {
			return Destination{}, err
		}
	}

	dest, err := s.repo.CreateDestination(ctx, CreateDestinationParams{
		ServerID:    s.serverID,
		Name:        strings.TrimSpace(input.Name),
		Kind:        input.Kind,
		Config:      config,
		Credentials: encrypted,
	})
	if err != nil {
		return Destination{}, err
	}

	if encrypted != "" {
		bound, err := s.encryptCredentials(credentials, dest.ID)
		if err != nil {
			return Destination{}, err
		}
		if _, err := s.repo.UpdateDestination(ctx, dest.ID,
			UpdateDestinationParams{Credentials: &bound}); err != nil {
			return Destination{}, err
		}
	}

	s.record(ctx, actor, ActionDestinationCreate, ResourceTypeDestination, dest.ID,
		map[string]any{"name": dest.Name, "kind": dest.Kind})

	return s.repo.GetDestination(ctx, dest.ID)
}

// UpdateDestination changes a destination.
//
// The kind cannot change, so the existing one is used for validation. A
// destination that became a different kind would keep the backups pointing at
// it while describing somewhere they are not.
func (s *Service) UpdateDestination(ctx context.Context, id string, input DestinationInput,
	actor Actor,
) (Destination, error) {
	existing, err := s.repo.GetDestination(ctx, id)
	if err != nil {
		return Destination{}, err
	}
	input.Kind = existing.Kind

	// A secret is only required when there is not one already: a page that
	// never received the key cannot send it back, and demanding it would mean
	// re-entering an S3 secret to change a bucket name.
	config, credentials, err := s.buildDestination(input, !existing.HasCredentials)
	if err != nil {
		return Destination{}, err
	}

	params := UpdateDestinationParams{Config: config}
	if name := strings.TrimSpace(input.Name); name != "" {
		params.Name = &name
	}
	if credentials != (credentialFields{}) {
		encrypted, err := s.encryptCredentials(credentials, id)
		if err != nil {
			return Destination{}, err
		}
		params.Credentials = &encrypted
	}

	dest, err := s.repo.UpdateDestination(ctx, id, params)
	if err != nil {
		return Destination{}, err
	}

	// Changing where a destination points invalidates what the panel knew about
	// reaching it. Leaving the old "reachable" mark would be the panel vouching
	// for somewhere it has never been.
	if err := s.repo.RecordDestinationCheck(ctx, id, false,
		"this destination has changed since it was last checked"); err != nil {
		return Destination{}, err
	}

	s.record(ctx, actor, ActionDestinationUpdate, ResourceTypeDestination, id,
		map[string]any{"name": dest.Name, "kind": dest.Kind,
			"credentials_changed": params.Credentials != nil})

	return s.repo.GetDestination(ctx, id)
}

// buildDestination validates an input and splits it into config and secret.
func (s *Service) buildDestination(input DestinationInput, requireSecret bool) (
	map[string]any, credentialFields, error,
) {
	if err := validate.DestinationName(input.Name); err != nil {
		return nil, credentialFields{}, err
	}
	if err := validate.DestinationKind(input.Kind); err != nil {
		return nil, credentialFields{}, err
	}

	config := map[string]any{}
	credentials := credentialFields{}

	switch input.Kind {
	case validate.DestinationLocal:
		if err := validate.LocalBackupRoot(input.Directory); err != nil {
			return nil, credentialFields{}, err
		}
		config["directory"] = input.Directory

	case validate.DestinationS3:
		if err := validate.S3Endpoint(input.Endpoint, input.AllowInsecure); err != nil {
			return nil, credentialFields{}, err
		}
		if err := validate.S3Bucket(input.Bucket); err != nil {
			return nil, credentialFields{}, err
		}
		if err := validate.S3Region(input.Region); err != nil {
			return nil, credentialFields{}, err
		}
		if input.Prefix != "" {
			if err := validate.BackupKey(strings.Trim(input.Prefix, "/")); err != nil {
				return nil, credentialFields{}, err
			}
		}
		if input.AccessKey == "" {
			return nil, credentialFields{}, fmt.Errorf(
				"%w: an S3 destination needs an access key", ErrInvalidDestination)
		}
		if requireSecret && input.SecretKey == "" {
			return nil, credentialFields{}, fmt.Errorf(
				"%w: an S3 destination needs a secret key", ErrInvalidDestination)
		}
		config["endpoint"] = input.Endpoint
		config["bucket"] = input.Bucket
		config["region"] = input.Region
		config["prefix"] = strings.Trim(input.Prefix, "/")
		config["access_key"] = input.AccessKey
		config["path_style"] = input.PathStyle
		config["allow_insecure"] = input.AllowInsecure
		credentials.SecretKey = input.SecretKey

	case validate.DestinationSFTP:
		if err := validate.SFTPHost(input.Host); err != nil {
			return nil, credentialFields{}, err
		}
		port := input.Port
		if port == 0 {
			port = 22
		}
		if err := validate.SFTPPort(port); err != nil {
			return nil, credentialFields{}, err
		}
		if err := validate.SFTPUser(input.User); err != nil {
			return nil, credentialFields{}, err
		}
		if input.Path != "" {
			if err := validate.LocalBackupRoot(input.Path); err != nil {
				return nil, credentialFields{}, err
			}
		}
		if strings.TrimSpace(input.HostKey) == "" {
			return nil, credentialFields{}, fmt.Errorf(
				"%w: an SFTP destination needs the server's host key, or there is no way to "+
					"tell the intended server from whatever answers",
				ErrInvalidDestination)
		}
		if requireSecret && strings.TrimSpace(input.PrivateKey) == "" {
			return nil, credentialFields{}, fmt.Errorf(
				"%w: an SFTP destination needs a private key", ErrInvalidDestination)
		}
		config["host"] = input.Host
		config["port"] = port
		config["user"] = input.User
		config["path"] = input.Path
		config["host_key"] = strings.TrimSpace(input.HostKey)
		credentials.PrivateKey = input.PrivateKey
	}

	return config, credentials, nil
}

// encryptCredentials seals a destination's secret against its row.
func (s *Service) encryptCredentials(credentials credentialFields, id string) (string, error) {
	encoded, err := json.Marshal(credentials)
	if err != nil {
		return "", fmt.Errorf("encode destination credentials: %w", err)
	}
	sealed, err := s.crypto.Encrypt(encoded, encryptionContext+":"+id)
	if err != nil {
		return "", fmt.Errorf("encrypt destination credentials: %w", err)
	}
	return sealed, nil
}

// CheckDestination writes a small object to a destination and reads it back.
func (s *Service) CheckDestination(ctx context.Context, id, requestID string) (Destination, error) {
	destination, err := s.agentDestination(ctx, id)
	if err != nil {
		return Destination{}, err
	}

	detail := ""
	ok := true
	if err := s.agent.BackupCheckDestination(ctx, requestID, destination); err != nil {
		ok = false
		detail = err.Error()
	}
	if err := s.repo.RecordDestinationCheck(ctx, id, ok, detail); err != nil {
		return Destination{}, err
	}
	return s.repo.GetDestination(ctx, id)
}

// DeleteDestination removes a destination.
func (s *Service) DeleteDestination(ctx context.Context, id string, actor Actor) error {
	dest, err := s.repo.GetDestination(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteDestination(ctx, id); err != nil {
		return err
	}
	s.record(ctx, actor, ActionDestinationDelete, ResourceTypeDestination, id,
		map[string]any{"name": dest.Name, "kind": dest.Kind})
	return nil
}

// ------------------------------------------------------------- schedules

// ScheduleInput is a schedule as a request describes it.
type ScheduleInput struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	WebsiteID     string `json:"website_id"`
	DatabaseID    string `json:"database_id"`
	DestinationID string `json:"destination_id"`
	Hour          int    `json:"hour"`
	Minute        int    `json:"minute"`
	DayOfWeek     int    `json:"day_of_week"`
	RetentionDays int    `json:"retention_days"`
	KeepLast      int    `json:"keep_last"`
	Enabled       *bool  `json:"enabled"`
}

// CreateSchedule validates and stores a schedule.
func (s *Service) CreateSchedule(ctx context.Context, input ScheduleInput, actor Actor) (
	Schedule, error,
) {
	if err := validate.ScheduleName(input.Name); err != nil {
		return Schedule{}, err
	}
	if err := validate.BackupType(input.Type); err != nil {
		return Schedule{}, err
	}
	// The gate for every run this schedule will make: the scheduler acts for
	// no one, so a panel schedule is refused here rather than at 3am.
	if err := requirePanelAuthority(actor, input.Type); err != nil {
		return Schedule{}, err
	}
	if err := validate.ScheduleTime(input.Hour, input.Minute); err != nil {
		return Schedule{}, err
	}
	if err := validate.ScheduleDay(input.DayOfWeek); err != nil {
		return Schedule{}, err
	}
	if err := validate.RetentionDays(input.RetentionDays); err != nil {
		return Schedule{}, err
	}
	if err := validate.KeepLast(input.KeepLast); err != nil {
		return Schedule{}, err
	}
	if input.Type == validate.BackupWebsite && input.WebsiteID == "" {
		return Schedule{}, fmt.Errorf("%w: a website schedule needs a website", ErrNothingToBackUp)
	}
	if input.Type == validate.BackupDatabase && input.DatabaseID == "" {
		return Schedule{}, fmt.Errorf("%w: a database schedule needs a database", ErrNothingToBackUp)
	}
	if _, err := s.repo.GetDestination(ctx, input.DestinationID); err != nil {
		return Schedule{}, err
	}

	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}

	schedule, err := s.repo.CreateSchedule(ctx, CreateScheduleParams{
		ServerID:      s.serverID,
		Name:          strings.TrimSpace(input.Name),
		Type:          input.Type,
		WebsiteID:     input.WebsiteID,
		DatabaseID:    input.DatabaseID,
		DestinationID: input.DestinationID,
		Hour:          input.Hour,
		Minute:        input.Minute,
		DayOfWeek:     input.DayOfWeek,
		RetentionDays: input.RetentionDays,
		KeepLast:      input.KeepLast,
		Enabled:       enabled,
	})
	if err != nil {
		return Schedule{}, err
	}

	s.record(ctx, actor, ActionScheduleCreate, ResourceTypeSchedule, schedule.ID,
		map[string]any{"name": schedule.Name, "type": schedule.Type,
			"hour": schedule.Hour, "minute": schedule.Minute})
	return schedule, nil
}

// UpdateSchedule changes a schedule.
func (s *Service) UpdateSchedule(ctx context.Context, id string, input ScheduleInput,
	actor Actor,
) (Schedule, error) {
	existing, err := s.repo.GetSchedule(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	// Changing a panel schedule - where it sends the archive, how long it
	// keeps them - is as sensitive as creating one.
	if err := requirePanelAuthority(actor, existing.Type); err != nil {
		return Schedule{}, err
	}

	params := UpdateScheduleParams{}
	if name := strings.TrimSpace(input.Name); name != "" {
		if err := validate.ScheduleName(name); err != nil {
			return Schedule{}, err
		}
		params.Name = &name
	}
	if input.DestinationID != "" {
		if _, err := s.repo.GetDestination(ctx, input.DestinationID); err != nil {
			return Schedule{}, err
		}
		params.DestinationID = &input.DestinationID
	}
	if err := validate.ScheduleTime(input.Hour, input.Minute); err != nil {
		return Schedule{}, err
	}
	params.Hour = &input.Hour
	params.Minute = &input.Minute
	if err := validate.ScheduleDay(input.DayOfWeek); err != nil {
		return Schedule{}, err
	}
	params.DayOfWeek = &input.DayOfWeek
	if input.RetentionDays != 0 {
		if err := validate.RetentionDays(input.RetentionDays); err != nil {
			return Schedule{}, err
		}
		params.RetentionDays = &input.RetentionDays
	}
	if input.KeepLast != 0 {
		if err := validate.KeepLast(input.KeepLast); err != nil {
			return Schedule{}, err
		}
		params.KeepLast = &input.KeepLast
	}
	params.Enabled = input.Enabled

	schedule, err := s.repo.UpdateSchedule(ctx, id, params)
	if err != nil {
		return Schedule{}, err
	}

	s.record(ctx, actor, ActionScheduleUpdate, ResourceTypeSchedule, id,
		map[string]any{"name": schedule.Name, "enabled": schedule.Enabled})
	return schedule, nil
}

// DeleteSchedule removes a schedule, leaving the backups it took.
func (s *Service) DeleteSchedule(ctx context.Context, id string, actor Actor) error {
	schedule, err := s.repo.GetSchedule(ctx, id)
	if err != nil {
		return err
	}
	if err := requirePanelAuthority(actor, schedule.Type); err != nil {
		return err
	}
	if err := s.repo.DeleteSchedule(ctx, id); err != nil {
		return err
	}
	s.record(ctx, actor, ActionScheduleDelete, ResourceTypeSchedule, id,
		map[string]any{"name": schedule.Name})
	return nil
}

// RunSchedule takes a schedule's backup now.
func (s *Service) RunSchedule(ctx context.Context, id string, actor Actor) (Backup, error) {
	schedule, err := s.repo.GetSchedule(ctx, id)
	if err != nil {
		return Backup{}, err
	}
	if err := requirePanelAuthority(actor, schedule.Type); err != nil {
		return Backup{}, err
	}
	return s.runSchedule(ctx, schedule, actor)
}

// runSchedule queues a schedule's backup, whether asked for by hand or by the
// scheduler.
func (s *Service) runSchedule(ctx context.Context, schedule Schedule, actor Actor) (Backup, error) {
	item, err := s.create(ctx, CreateRequest{
		Type:             schedule.Type,
		WebsiteID:        stringValue(schedule.WebsiteID),
		DatabaseID:       stringValue(schedule.DatabaseID),
		DestinationID:    schedule.DestinationID,
		IncludeDatabases: true,
	}, actor)
	if err != nil {
		if recordErr := s.repo.RecordScheduleRun(ctx, schedule.ID, "failed", ""); recordErr != nil {
			s.log.Error("failed to record a schedule run", logger.KeyError, recordErr.Error())
		}
		return Backup{}, err
	}

	if err := s.repo.attachSchedule(ctx, item.ID, schedule.ID); err != nil {
		return Backup{}, err
	}
	if err := s.repo.RecordScheduleRun(ctx, schedule.ID, "running", item.ID); err != nil {
		return Backup{}, err
	}
	return s.repo.GetBackup(ctx, item.ID)
}

// ------------------------------------------------------------- retention

// Prune removes the backups a schedule's retention no longer keeps.
//
// It runs only after a backup has completed *and verified*. CLAUDE.md section
// 18 forbids overwriting a valid backup before verifying the new one, and this
// is where that rule lives: a run that failed, or that could not be read back,
// prunes nothing — so a week of failing backups leaves a week of old ones
// rather than deleting its way to an empty destination.
func (s *Service) Prune(ctx context.Context, schedule Schedule, requestID string) (int, error) {
	cutoff := s.now().UTC().AddDate(0, 0, -schedule.RetentionDays)
	prunable, err := s.repo.Prunable(ctx, PrunableParams{
		ScheduleID: schedule.ID,
		OlderThan:  cutoff,
		KeepLast:   schedule.KeepLast,
	})
	if err != nil {
		return 0, err
	}

	removed := 0
	for _, item := range prunable {
		if item.DestinationID == nil || item.Path == nil {
			// Nothing was ever written, so there is nothing to remove from a
			// destination; the row goes.
			if err := s.repo.DeleteBackup(ctx, item.ID); err != nil {
				return removed, err
			}
			removed++
			continue
		}

		destination, err := s.agentDestination(ctx, *item.DestinationID)
		if err != nil {
			s.log.Warn("could not prune a backup: its destination could not be read",
				"backup_id", item.ID, logger.KeyError, err.Error())
			continue
		}
		if err := s.agent.BackupDelete(ctx, requestID, *item.Path, destination); err != nil {
			// A destination that cannot be reached must not cause the row to
			// be deleted: that would leave an object nothing knows about,
			// costing money forever with no way to find it.
			s.log.Warn("could not prune a backup from its destination",
				"backup_id", item.ID, logger.KeyError, err.Error())
			continue
		}
		if err := s.repo.DeleteBackup(ctx, item.ID); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// ----------------------------------------------------------- job outcomes

// JobFinished updates a backup row when its job reaches a terminal state.
//
// This is the jobs.Observer hook. It runs after the job row is already written,
// so a failure here leaves an accurate job and a stale backup rather than the
// reverse.
func (s *Service) JobFinished(ctx context.Context, job jobs.Job, state jobs.State,
	result map[string]any, failure string,
) {
	if job.ResourceType == nil || *job.ResourceType != ResourceTypeBackup {
		return
	}
	if job.ResourceID == nil {
		return
	}
	id := *job.ResourceID

	switch job.Type {
	case JobTypeBackupCreate:
		s.backupFinished(ctx, id, state, result, failure)
	case JobTypeBackupRestore:
		// A restore changes nothing about the backup row: the archive is the
		// same archive whether or not it was put back. The job carries the
		// outcome, which is where somebody looks for it.
		if state != jobs.StateSuccess {
			s.log.Warn("a restore failed", "backup_id", id, "reason", failure)
		}
	}
}

func (s *Service) backupFinished(ctx context.Context, id string, state jobs.State,
	result map[string]any, failure string,
) {
	if state != jobs.StateSuccess {
		reason := failure
		if reason == "" {
			reason = "the backup did not finish"
		}
		if err := s.repo.FailBackup(ctx, id, reason); err != nil {
			s.log.Error("failed to record a failed backup", logger.KeyError, err.Error())
		}
		s.markScheduleOutcome(ctx, id, "failed")
		s.notifyFailure(ctx, id, reason)
		return
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		s.log.Error("could not read a backup result", logger.KeyError, err.Error())
		return
	}
	var outcome agentclient.BackupResult
	if err := json.Unmarshal(encoded, &outcome); err != nil {
		s.log.Error("could not read a backup result", logger.KeyError, err.Error())
		return
	}

	manifest := map[string]any{}
	if raw, ok := result["manifest"].(map[string]any); ok {
		manifest = raw
		// The per-file list is dropped before the manifest is stored. An
		// archive of a WordPress site has forty thousand entries, and keeping
		// them per backup would make this table larger than the data it
		// describes. The digests stay in the archive, where a verify reads
		// them; the counts and the summaries are what a page shows.
		delete(manifest, "files")
	}

	if err := s.repo.CompleteBackup(ctx, id, CompleteBackupParams{
		Path:         outcome.Key,
		Size:         outcome.Size,
		Checksum:     outcome.Checksum,
		Verified:     outcome.Verified,
		VerifyDetail: outcome.VerifyDetail,
		Manifest:     manifest,
	}); err != nil {
		s.log.Error("failed to record a completed backup", logger.KeyError, err.Error())
		return
	}

	status := "completed"
	if !outcome.Verified {
		status = "failed"
		// A backup that was written and could not be read back is the failure
		// this phase exists to catch, so it is notified like any other — the
		// bytes may be there and the panel has no basis for saying so.
		detail := outcome.VerifyDetail
		if detail == "" {
			detail = "the archive could not be read back from its destination"
		}
		s.notifyFailure(ctx, id, detail)
	}
	s.markScheduleOutcome(ctx, id, status)

	// Retention runs only now, once the new backup has been read back and
	// matched. This ordering is the whole of CLAUDE.md section 18.
	if !outcome.Verified {
		return
	}
	s.pruneFor(ctx, id)
}

// markScheduleOutcome records a run's result against its schedule.
func (s *Service) markScheduleOutcome(ctx context.Context, backupID, status string) {
	item, err := s.repo.GetBackup(ctx, backupID)
	if err != nil || item.ScheduleID == nil {
		return
	}
	if err := s.repo.RecordScheduleRun(ctx, *item.ScheduleID, status, backupID); err != nil {
		s.log.Error("failed to record a schedule run", logger.KeyError, err.Error())
	}
}

// pruneFor applies retention after a schedule's backup verified.
func (s *Service) pruneFor(ctx context.Context, backupID string) {
	item, err := s.repo.GetBackup(ctx, backupID)
	if err != nil || item.ScheduleID == nil {
		return
	}
	schedule, err := s.repo.GetSchedule(ctx, *item.ScheduleID)
	if err != nil {
		return
	}

	pruneCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()

	removed, err := s.Prune(pruneCtx, schedule, "prune_"+schedule.ID)
	if err != nil {
		s.log.Error("retention failed", "schedule_id", schedule.ID, logger.KeyError, err.Error())
		return
	}
	if removed > 0 {
		s.log.Info("retention removed old backups",
			"schedule", schedule.Name, "removed", removed,
			"retention_days", schedule.RetentionDays, "keep_last", schedule.KeepLast)
	}
}

// notifyFailure tells the notifier about a backup that did not work.
//
// The backup is read back rather than passed in, because the subject — which
// site or database this was a copy of — is what somebody reads in the message,
// and only the row has it after a website has been deleted.
func (s *Service) notifyFailure(ctx context.Context, id, reason string) {
	if s.notifier == nil {
		return
	}
	item, err := s.repo.GetBackup(ctx, id)
	if err != nil {
		// Notifying with no subject would be worse than not notifying: "a
		// backup failed" with nothing naming which one is a message that costs
		// the reader more than it gives them.
		s.log.Error("could not read a failed backup to notify about it",
			logger.KeyError, err.Error())
		return
	}
	s.notifier.BackupFailed(ctx, item.ID, item.Subject, reason)
}
