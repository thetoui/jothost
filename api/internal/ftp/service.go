package ftp

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/shared/validate"
)

// Audit actions.
//
// Every one of these changes who can reach a customer's files over the network,
// which is exactly the kind of change an audit trail exists for. A password
// reset is audited too, and the audit record says only that one happened.
const (
	ActionCreate       = "ftp.user.create"
	ActionUpdate       = "ftp.user.update"
	ActionDelete       = "ftp.user.delete"
	ActionPassword     = "ftp.user.password"
	ActionConfigure    = "ftp.configure"
	ActionInstall      = "ftp.install"
	ActionDisconnect   = "ftp.session.disconnect"
	ResourceTypeUser   = "ftp_user"
	ResourceTypeServer = "server"
)

// Errors returned by the service.
var (
	// ErrUnavailable means the host has no FTP server.
	ErrUnavailable = errors.New("this host has no FTP server")
	// ErrWebsiteRequired means the request named no website, and an FTP account
	// with no website has no identity to map onto.
	ErrWebsiteRequired = errors.New("an FTP account belongs to a website")
	// ErrNoAccount means the website has no system account yet.
	ErrNoAccount = errors.New("this website has no system account to map the FTP user onto")
	// ErrNoCertificate means FTPS was asked for with nothing to present.
	ErrNoCertificate = errors.New("FTPS needs a website with a certificate")
	// ErrQuotaUnsupported means this build of the server cannot enforce quotas.
	ErrQuotaUnsupported = errors.New("this FTP server cannot enforce disk limits")
)

// Actor is who asked, for the audit trail.
type Actor struct {
	UserID    string
	IPAddress string
	UserAgent string
}

// Websites is what this package needs to know about the site an account belongs
// to.
//
// An interface rather than the websites repository itself: this package needs
// four fields, and depending on the whole thing would make the two impossible
// to change independently. It mirrors what the cron package does, for the same
// reason.
type Websites interface {
	LookupForFTP(ctx context.Context, id string) (WebsiteRef, error)
}

// WebsiteRef is the site an account belongs to.
type WebsiteRef struct {
	ID           string
	ServerID     string
	Domain       string
	SystemUser   string
	DocumentRoot string
	// SSLEnabled reports whether this site has a certificate FTPS could
	// present.
	SSLEnabled bool
	// CertificatePath and KeyPath are where Phase 6 put the material.
	CertificatePath string
	KeyPath         string
}

// Service manages FTP accounts.
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

// Overview is everything the FTP page shows.
type Overview struct {
	// Status is what the host reports.
	agentclient.FTPStatus
	// Users are the accounts the panel has recorded, with the usage the host
	// reported for each.
	Users []User `json:"users"`
	// Settings are the panel's settings for this host.
	Settings Settings `json:"settings"`
}

// Overview reads the whole picture.
//
// The accounts come from the panel's record and the usage from the host, joined
// here. An account the host has and the panel does not is *not* added to the
// list: the panel's record is what the next reconcile will make true, so
// showing a row the panel does not own would be showing something that is about
// to be removed.
func (s *Service) Overview(ctx context.Context, requestID string) (Overview, error) {
	overview := Overview{Users: []User{}}

	// The settings first: the host's firewall check needs the passive range,
	// which is the panel's setting rather than anything the host records.
	settings, err := s.repo.Settings(ctx, s.serverID)
	if err != nil {
		return overview, err
	}

	status, err := s.agent.FTPStatusOf(ctx, requestID,
		settings.PassiveFrom, settings.PassiveTo)
	if err != nil {
		return overview, err
	}
	overview.FTPStatus = status
	if overview.Accounts == nil {
		overview.Accounts = []agentclient.FTPAccount{}
	}
	if overview.Sessions == nil {
		overview.Sessions = []agentclient.FTPSession{}
	}

	users, err := s.repo.List(ctx, s.serverID)
	if err != nil {
		return overview, err
	}

	onHost := make(map[string]agentclient.FTPAccount, len(status.Accounts))
	for _, account := range status.Accounts {
		onHost[account.Name] = account
	}
	for i := range users {
		account, found := onHost[users[i].Username]
		if !found {
			users[i].MissingOnHost = true
			continue
		}
		users[i].UsedMB = account.UsedMB
		users[i].Locked = account.Locked
	}
	overview.Users = users
	overview.Settings = settings
	return overview, nil
}

// CreateRequest is what an operator asked for.
type CreateRequest struct {
	WebsiteID   string
	Username    string
	Password    string
	HomeSubpath string
	AccessLevel string
	QuotaMB     int
}

// CreateResult is the new account, with the password if the panel generated
// one.
type CreateResult struct {
	User User `json:"user"`
	// Password is returned exactly once, and only when the panel generated it.
	// It is not stored, so this response is the only chance to see it — the
	// page says so.
	Password string `json:"password,omitempty"`
}

// Create records an account and puts it on the host.
func (s *Service) Create(ctx context.Context, actor Actor, requestID string,
	req CreateRequest,
) (CreateResult, error) {
	var result CreateResult

	if strings.TrimSpace(req.WebsiteID) == "" {
		return result, ErrWebsiteRequired
	}
	if err := validate.FTPUsername(req.Username); err != nil {
		return result, err
	}
	if req.AccessLevel == "" {
		req.AccessLevel = validate.AccessFull
	}
	if err := validate.FTPAccess(req.AccessLevel); err != nil {
		return result, err
	}
	if err := validate.FTPQuotaMB(req.QuotaMB); err != nil {
		return result, err
	}
	subpath, err := validate.FTPHome(req.HomeSubpath)
	if err != nil {
		return result, err
	}

	// Generated when none was given, and returned once. A password the panel
	// invents is a strong one; a password an operator types under time pressure
	// is "Password1".
	password := req.Password
	generated := false
	if password == "" {
		password, err = generatePassword()
		if err != nil {
			return result, err
		}
		generated = true
	}
	if err := validate.FTPPassword(password); err != nil {
		return result, err
	}

	site, err := s.websites.LookupForFTP(ctx, req.WebsiteID)
	if err != nil {
		return result, err
	}
	if site.SystemUser == "" {
		return result, ErrNoAccount
	}

	user, err := s.repo.Create(ctx, CreateParams{
		ServerID:    site.ServerID,
		WebsiteID:   site.ID,
		Username:    req.Username,
		HomeSubpath: subpath,
		AccessLevel: req.AccessLevel,
		QuotaMB:     req.QuotaMB,
	})
	if err != nil {
		return result, err
	}

	if err := s.reconcile(ctx, requestID, map[string]string{req.Username: password}); err != nil {
		// The host refused it, so the record must go too: a row describing an
		// account nobody can log in to is a panel that lies about what exists.
		if removeErr := s.repo.Delete(ctx, user.ID); removeErr != nil {
			s.log.Error("could not remove an FTP account the host refused",
				"account", req.Username, "error", removeErr.Error())
		}
		return result, err
	}

	s.record(ctx, actor, requestID, ActionCreate, ResourceTypeUser, user.ID, map[string]any{
		"username": user.Username,
		"website":  site.Domain,
		"access":   user.AccessLevel,
		"quota_mb": user.QuotaMB,
	})

	// Re-read so the response carries the joined fields: the create returned
	// only the account's own columns, and the home directory is computed from
	// the website's document root.
	created, err := s.repo.Get(ctx, user.ID)
	if err != nil {
		return result, err
	}

	result.User = s.withHost(ctx, requestID, created)
	if generated {
		result.Password = password
	}
	return result, nil
}

// UpdateRequest is a change to an account. Nil fields are left alone.
type UpdateRequest struct {
	HomeSubpath *string
	AccessLevel *string
	QuotaMB     *int
	Suspended   *bool
	// Password sets a new one. Empty leaves it alone — it is not a field that
	// can be cleared, because an account with no password is one anybody can
	// use.
	Password string
}

// Update changes an account and reconciles the host.
func (s *Service) Update(ctx context.Context, actor Actor, requestID, id string,
	req UpdateRequest,
) (User, error) {
	existing, err := s.repo.Get(ctx, id)
	if err != nil {
		return User{}, err
	}

	params := UpdateParams{QuotaMB: req.QuotaMB, Suspended: req.Suspended}
	if req.HomeSubpath != nil {
		subpath, err := validate.FTPHome(*req.HomeSubpath)
		if err != nil {
			return User{}, err
		}
		params.HomeSubpath = &subpath
	}
	if req.AccessLevel != nil {
		if err := validate.FTPAccess(*req.AccessLevel); err != nil {
			return User{}, err
		}
		params.AccessLevel = req.AccessLevel
	}
	if req.QuotaMB != nil {
		if err := validate.FTPQuotaMB(*req.QuotaMB); err != nil {
			return User{}, err
		}
	}

	passwords := map[string]string{}
	if req.Password != "" {
		if err := validate.FTPPassword(req.Password); err != nil {
			return User{}, err
		}
		passwords[existing.Username] = req.Password
	}

	updated, err := s.repo.Update(ctx, id, params)
	if err != nil {
		return User{}, err
	}

	if err := s.reconcile(ctx, requestID, passwords); err != nil {
		return User{}, err
	}

	action := ActionUpdate
	details := map[string]any{"username": updated.Username}
	if req.Password != "" {
		// Recorded as its own action, and the record says only that it
		// happened.
		action = ActionPassword
	} else {
		details["access"] = updated.AccessLevel
		details["quota_mb"] = updated.QuotaMB
		details["suspended"] = updated.Suspended
	}
	s.record(ctx, actor, requestID, action, ResourceTypeUser, updated.ID, details)

	// Re-read for the joined fields, as the create does and for the same
	// reason.
	full, err := s.repo.Get(ctx, updated.ID)
	if err != nil {
		return User{}, err
	}
	return s.withHost(ctx, requestID, full), nil
}

// Delete removes an account from the panel and from the host.
func (s *Service) Delete(ctx context.Context, actor Actor, requestID, id string) error {
	existing, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}

	// The reconcile is what actually removes it from the host: the account is
	// gone from the panel's set, so the Agent deletes what it finds that the
	// set does not name.
	if err := s.reconcile(ctx, requestID, nil); err != nil {
		return err
	}

	s.record(ctx, actor, requestID, ActionDelete, ResourceTypeUser, id, map[string]any{
		"username": existing.Username,
		"website":  existing.WebsiteDomain,
	})
	return nil
}

// SettingsRequest is a change to the server's own options.
type SettingsRequest struct {
	PassiveFrom       *int
	PassiveTo         *int
	TLSWebsiteID      *string
	RequireTLS        *bool
	MasqueradeAddress *string
	MaxClients        *int
}

// Configure changes the server's settings.
func (s *Service) Configure(ctx context.Context, actor Actor, requestID string,
	req SettingsRequest,
) (Settings, error) {
	settings, err := s.repo.Settings(ctx, s.serverID)
	if err != nil {
		return Settings{}, err
	}

	if req.PassiveFrom != nil {
		settings.PassiveFrom = *req.PassiveFrom
	}
	if req.PassiveTo != nil {
		settings.PassiveTo = *req.PassiveTo
	}
	if err := validate.PassivePortRange(settings.PassiveFrom, settings.PassiveTo); err != nil {
		return Settings{}, err
	}
	if req.TLSWebsiteID != nil {
		settings.TLSWebsiteID = strings.TrimSpace(*req.TLSWebsiteID)
	}
	if req.RequireTLS != nil {
		settings.RequireTLS = *req.RequireTLS
	}
	if req.MasqueradeAddress != nil {
		settings.MasqueradeAddress = strings.TrimSpace(*req.MasqueradeAddress)
	}
	if req.MaxClients != nil {
		settings.MaxClients = *req.MaxClients
	}

	// Checked here rather than left to the host, because the message a panel
	// can give — "that site has no certificate" — is one an operator can act
	// on, and proftpd's would be a parser error about a file.
	if settings.RequireTLS && settings.TLSWebsiteID == "" {
		return Settings{}, ErrNoCertificate
	}
	if settings.TLSWebsiteID != "" {
		site, err := s.websites.LookupForFTP(ctx, settings.TLSWebsiteID)
		if err != nil {
			return Settings{}, err
		}
		if !site.SSLEnabled || site.CertificatePath == "" {
			return Settings{}, fmt.Errorf("%w: %s has none", ErrNoCertificate, site.Domain)
		}
	}

	saved, err := s.repo.SaveSettings(ctx, settings)
	if err != nil {
		return Settings{}, err
	}
	if err := s.reconcile(ctx, requestID, nil); err != nil {
		return Settings{}, err
	}

	s.record(ctx, actor, requestID, ActionConfigure, ResourceTypeServer, s.serverID,
		map[string]any{
			"passive_from": saved.PassiveFrom,
			"passive_to":   saved.PassiveTo,
			"require_tls":  saved.RequireTLS,
			"tls_website":  saved.TLSDomain,
		})
	return saved, nil
}

// Install puts an FTP server on the host.
func (s *Service) Install(ctx context.Context, actor Actor, requestID string) (
	agentclient.FTPStatus, error,
) {
	status, err := s.agent.FTPInstall(ctx, requestID)
	if err != nil {
		return status, err
	}
	s.record(ctx, actor, requestID, ActionInstall, ResourceTypeServer, s.serverID,
		map[string]any{"version": status.Version})
	return status, nil
}

// Sessions reports who is connected.
func (s *Service) Sessions(ctx context.Context, requestID string) (
	[]agentclient.FTPSession, error,
) {
	list, err := s.agent.FTPSessions(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if list.Sessions == nil {
		return []agentclient.FTPSession{}, nil
	}
	return list.Sessions, nil
}

// Disconnect ends one session.
func (s *Service) Disconnect(ctx context.Context, actor Actor, requestID string,
	pid int,
) error {
	if err := s.agent.FTPDisconnect(ctx, requestID, pid); err != nil {
		return err
	}
	s.record(ctx, actor, requestID, ActionDisconnect, ResourceTypeServer, s.serverID,
		map[string]any{"pid": pid})
	return nil
}

// reconcile hands the Agent the complete set of accounts and the settings.
//
// Complete, always. The Agent makes the host match what it is given, so a set
// missing an account would delete that account from the host — which is exactly
// how a delete is performed, and exactly what must not happen by accident.
func (s *Service) reconcile(ctx context.Context, requestID string,
	passwords map[string]string,
) error {
	users, err := s.repo.List(ctx, s.serverID)
	if err != nil {
		return err
	}
	settings, err := s.repo.Settings(ctx, s.serverID)
	if err != nil {
		return err
	}

	desired := agentclient.FTPDesired{
		Settings: agentclient.FTPSettings{
			PassiveFrom:       settings.PassiveFrom,
			PassiveTo:         settings.PassiveTo,
			RequireTLS:        settings.RequireTLS,
			MasqueradeAddress: settings.MasqueradeAddress,
			MaxClients:        settings.MaxClients,
		},
		Users:     make([]agentclient.FTPUser, 0, len(users)),
		Passwords: passwords,
	}

	// The certificate is looked up rather than stored, so a renewal that moved
	// nothing is invisible here and a renewal that did move something is
	// picked up on the next change.
	if settings.TLSWebsiteID != "" {
		site, err := s.websites.LookupForFTP(ctx, settings.TLSWebsiteID)
		if err != nil {
			return err
		}
		desired.Settings.TLSCertificate = site.CertificatePath
		desired.Settings.TLSKey = site.KeyPath
	}

	for _, user := range users {
		desired.Users = append(desired.Users, agentclient.FTPUser{
			Name:       user.Username,
			SystemUser: user.SystemUser,
			Home:       user.Home,
			ReadOnly:   user.AccessLevel == validate.AccessReadOnly,
			QuotaMB:    user.QuotaMB,
			Suspended:  user.Suspended,
		})
	}

	result, err := s.agent.FTPReconcile(ctx, requestID, desired)
	if err != nil {
		return err
	}
	for _, conflict := range result.Conflicts {
		if !conflict.PanelWins {
			s.log.Warn("another FTP configuration file overrides the panel",
				"directive", conflict.Directive, "file", conflict.File)
		}
	}
	if len(result.NeedPassword) > 0 {
		// Not an error: the change that was asked for succeeded. It is a
		// standing condition the operator has to resolve one account at a time,
		// and the page reports it from the status rather than from this call.
		s.log.Warn("FTP accounts on this host need a new password before they work",
			"accounts", strings.Join(result.NeedPassword, ", "))
	}
	return nil
}

// withHost fills in the usage the host reported for one account.
//
// Best effort: a failure to read it is logged and the account is returned
// without it, because "the account exists and its usage is unknown" is a better
// answer than failing a create that succeeded.
func (s *Service) withHost(ctx context.Context, requestID string, user User) User {
	status, err := s.agent.FTPStatusOf(ctx, requestID, 0, 0)
	if err != nil {
		s.log.Warn("could not read the FTP server's state", "error", err.Error())
		return user
	}
	for _, account := range status.Accounts {
		if account.Name == user.Username {
			user.UsedMB = account.UsedMB
			user.Locked = account.Locked
			break
		}
	}
	return user
}

// record writes an audit event, and never fails the operation for it.
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

// passwordAlphabet is what a generated password is drawn from.
//
// No characters that a person reads as another — no O and 0, no l and 1 and I —
// because this password is read off a screen and typed into an FTP client, and
// a character somebody mistyped is a support ticket. Punctuation is left out
// for the same reason: it is what breaks when a password is pasted through a
// client that quotes it.
const passwordAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// generatedPasswordLength is long enough that leaving punctuation out costs
// nothing: 24 characters of this alphabet is about 139 bits.
const generatedPasswordLength = 24

// generatePassword makes a password the panel will show once.
func generatePassword() (string, error) {
	var b strings.Builder
	b.Grow(generatedPasswordLength)

	limit := big.NewInt(int64(len(passwordAlphabet)))
	for i := 0; i < generatedPasswordLength; i++ {
		// crypto/rand, not math/rand: this is a credential.
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("generate an FTP password: %w", err)
		}
		b.WriteByte(passwordAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// ForWebsite returns one website's accounts, with the usage the host reported.
//
// The website's own tab uses this rather than filtering the whole host's list
// client-side, so a site with three accounts does not fetch the two hundred on
// a busy server to show them.
func (s *Service) ForWebsite(ctx context.Context, requestID, websiteID string) ([]User, error) {
	users, err := s.repo.ListForWebsite(ctx, websiteID)
	if err != nil {
		return nil, err
	}
	if len(users) == 0 {
		return []User{}, nil
	}

	// Best effort: a host that cannot be asked still has accounts worth
	// showing, and a tab that fails entirely because the usage is unknown is
	// worse than one that shows the accounts without it.
	status, err := s.agent.FTPStatusOf(ctx, requestID, 0, 0)
	if err != nil {
		s.log.Warn("could not read the FTP server's state", "error", err.Error())
		return users, nil
	}
	onHost := make(map[string]agentclient.FTPAccount, len(status.Accounts))
	for _, account := range status.Accounts {
		onHost[account.Name] = account
	}
	for i := range users {
		account, found := onHost[users[i].Username]
		if !found {
			users[i].MissingOnHost = true
			continue
		}
		users[i].UsedMB = account.UsedMB
		users[i].Locked = account.Locked
	}
	return users, nil
}
