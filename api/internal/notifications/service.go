package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/secrets"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/validate"
)

// Audit actions.
//
// Configuring a channel is audited because changing where a panel sends its
// alerts is how somebody quietly stops them arriving — and the audit trail is
// the only record of that which does not depend on the channel itself.
const (
	ActionChannelCreate = "notification.channel.create"
	ActionChannelUpdate = "notification.channel.update"
	ActionChannelDelete = "notification.channel.delete"
	ActionChannelTest   = "notification.channel.test"

	ResourceTypeChannel = "notification_channel"
)

// The encryption context binds a ciphertext to the row it belongs to, so a
// credential moved from another channel fails to decrypt rather than quietly
// authenticating somewhere it should not.
const encryptionContext = "notification_channel"

// Errors returned by the service.
var (
	// ErrInvalidChannel covers a channel the panel will not accept.
	ErrInvalidChannel = errors.New("invalid notification channel")
	// ErrNoChannels means nothing is configured to receive notifications.
	ErrNoChannels = errors.New("no notification channel is configured")
)

// Actor is who asked, for the audit trail.
type Actor struct {
	UserID    string
	IPAddress string
	UserAgent string
}

// credentialFields is what is stored encrypted.
//
// Password for SMTP, Token for a Telegram bot token or a LINE channel access
// token. The SMTP *username* is not here: it is an identifier rather than a
// secret, and a page needs to show which account a channel sends as.
type credentialFields struct {
	Password string `json:"password,omitempty"`
	Token    string `json:"token,omitempty"`
}

// Service configures channels and raises events.
type Service struct {
	repo     *Repository
	audit    *audit.Recorder
	crypto   *secrets.Encrypter
	log      *slog.Logger
	http     *http.Client
	serverID string
	panelURL string
	now      func() time.Time
}

// ServiceOptions configure a Service.
type ServiceOptions struct {
	Repository *Repository
	Audit      *audit.Recorder
	Crypto     *secrets.Encrypter
	Log        *slog.Logger
	HTTPClient *http.Client
	ServerID   string
	// PanelURL is where a notification's link points. Empty leaves links out
	// rather than sending a relative path nobody can follow.
	PanelURL string
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
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: httpTimeout}
	}
	return &Service{
		repo:     opts.Repository,
		audit:    opts.Audit,
		crypto:   opts.Crypto,
		log:      log,
		http:     client,
		serverID: opts.ServerID,
		panelURL: opts.PanelURL,
		now:      now,
	}
}

// ------------------------------------------------------------------ events

// Emit raises an event and queues it to every channel that wants it.
//
// It is the whole of the interface the rest of the panel uses, and it is
// deliberately one method. A phase that raises an event should not have to know
// what a channel is, how many there are, or whether any of them work.
//
// A duplicate is not an error and not a log line. The callers are loops that run
// every minute, and "this alert is still open" arriving sixty times an hour is
// the normal case — the whole point of the dedupe key is that the second one
// costs nothing.
func (s *Service) Emit(ctx context.Context, event Event) {
	if s == nil || s.repo == nil {
		return
	}

	event.ServerID = s.serverID
	if err := validate.DedupeKey(event.DedupeKey); err != nil {
		s.log.Error("an event was raised with an unusable dedupe key",
			"kind", event.Kind, logger.KeyError, err.Error())
		return
	}
	if err := validate.NotifySeverity(event.Severity); err != nil {
		s.log.Error("an event was raised with an unknown severity",
			"kind", event.Kind, logger.KeyError, err.Error())
		return
	}

	stored, err := s.repo.RecordEvent(ctx, event)
	if errors.Is(err, ErrDuplicateEvent) {
		return
	}
	if err != nil {
		s.log.Error("could not record a notification event",
			"kind", event.Kind, logger.KeyError, err.Error())
		return
	}

	channels, err := s.repo.ListChannels(ctx, s.serverID)
	if err != nil {
		// The event is stored either way. A dispatcher restart will not pick it
		// up — nothing was queued — but the record of what happened survives,
		// which is better than losing both.
		s.log.Error("could not read the notification channels",
			logger.KeyError, err.Error())
		return
	}

	queued := 0
	for _, channel := range channels {
		if !wants(channel, stored) {
			continue
		}
		if err := s.repo.QueueDelivery(ctx, stored.ID, channel.ID); err != nil {
			s.log.Error("could not queue a notification",
				"channel", channel.Name, logger.KeyError, err.Error())
			continue
		}
		queued++
	}

	s.log.Info("notification raised",
		"kind", stored.Kind, "severity", stored.Severity,
		"title", stored.Title, "channels", queued)
}

// wants reports whether a channel should receive an event.
//
// A disabled channel receives nothing. A channel with no kinds listed receives
// every kind, which is the right default: a channel that silently excluded a
// category would be one somebody believes is watching something it is not.
func wants(channel Channel, event Event) bool {
	if !channel.Enabled {
		return false
	}
	if !validate.NotifyAtLeast(event.Severity, channel.MinSeverity) {
		return false
	}
	if len(channel.Kinds) == 0 {
		return true
	}
	for _, kind := range channel.Kinds {
		if kind == event.Kind {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- overview

// Overview is what the notifications page shows.
type Overview struct {
	Channels   []Channel     `json:"channels"`
	Deliveries []Delivery    `json:"deliveries"`
	Events     []Event       `json:"events"`
	Stats      DeliveryStats `json:"stats"`

	Kinds      []string `json:"channel_kinds"`
	EventKinds []string `json:"event_kinds"`
	Severities []string `json:"severities"`
}

// Overview reads everything the page needs.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	overview := Overview{
		Kinds:      validate.ChannelKinds,
		EventKinds: validate.EventKinds,
		Severities: validate.NotifySeverities,
	}

	var err error
	if overview.Channels, err = s.repo.ListChannels(ctx, s.serverID); err != nil {
		return Overview{}, err
	}
	if overview.Deliveries, err = s.repo.ListDeliveries(ctx, s.serverID, "", 50); err != nil {
		return Overview{}, err
	}
	if overview.Events, err = s.repo.ListEvents(ctx, s.serverID, 30); err != nil {
		return Overview{}, err
	}
	// A week, because that is roughly how long somebody would take to notice
	// silence on their own.
	since := s.now().UTC().AddDate(0, 0, -7)
	if overview.Stats, err = s.repo.Stats(ctx, s.serverID, since); err != nil {
		return Overview{}, err
	}
	return overview, nil
}

// Deliveries returns the delivery record.
func (s *Service) Deliveries(ctx context.Context, status string, limit int) (
	[]Delivery, error,
) {
	return s.repo.ListDeliveries(ctx, s.serverID, status, limit)
}

// ---------------------------------------------------------------- channels

// ChannelInput is a channel as a request describes it.
type ChannelInput struct {
	Name string `json:"name"`
	Kind string `json:"kind"`

	// Email.
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"`
	Username string `json:"username"`
	Password string `json:"password"`
	From     string `json:"from"`
	// AllowInsecure accepts a plain-SMTP relay that is not on this machine.
	// It is never a default and never inferred from the host.
	AllowInsecure bool     `json:"allow_insecure"`
	To            []string `json:"to"`

	// Telegram and LINE.
	Token     string `json:"token"`
	ChatID    string `json:"chat_id"`
	Recipient string `json:"recipient"`

	MinSeverity string   `json:"min_severity"`
	Kinds       []string `json:"kinds"`
	Enabled     *bool    `json:"enabled"`
}

// CreateChannel validates and stores a channel.
func (s *Service) CreateChannel(ctx context.Context, input ChannelInput, actor Actor) (
	Channel, error,
) {
	config, credentials, err := s.buildChannel(input, true)
	if err != nil {
		return Channel{}, err
	}

	// Sealed against a placeholder first, then resealed once the row's id is
	// known. One extra write, and it is what keeps the binding exact: a
	// ciphertext bound to nothing could be copied into another channel and
	// would still decrypt.
	sealed, err := s.encrypt(credentials, "pending")
	if err != nil {
		return Channel{}, err
	}

	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	severity := input.MinSeverity
	if severity == "" {
		severity = validate.NotifyWarning
	}

	channel, err := s.repo.CreateChannel(ctx, CreateChannelParams{
		ServerID:    s.serverID,
		Name:        strings.TrimSpace(input.Name),
		Kind:        input.Kind,
		Config:      config,
		Credentials: sealed,
		MinSeverity: severity,
		Kinds:       input.Kinds,
		Enabled:     enabled,
	})
	if err != nil {
		return Channel{}, err
	}

	bound, err := s.encrypt(credentials, channel.ID)
	if err != nil {
		return Channel{}, err
	}
	if _, err := s.repo.UpdateChannel(ctx, channel.ID,
		UpdateChannelParams{Credentials: &bound}); err != nil {
		return Channel{}, err
	}

	s.record(ctx, actor, ActionChannelCreate, channel.ID,
		map[string]any{"name": channel.Name, "kind": channel.Kind})
	return s.repo.GetChannel(ctx, channel.ID)
}

// UpdateChannel changes a channel.
//
// The kind cannot change: a channel that became a different kind would keep the
// delivery history of the old one, and "this has worked for months" would be a
// claim about somewhere else entirely.
func (s *Service) UpdateChannel(ctx context.Context, id string, input ChannelInput,
	actor Actor,
) (Channel, error) {
	existing, err := s.repo.GetChannel(ctx, id)
	if err != nil {
		return Channel{}, err
	}
	input.Kind = existing.Kind

	// A secret is only required when there is not one already: a page that never
	// received the password cannot send it back, and demanding it would mean
	// re-entering an SMTP password to change a recipient.
	config, credentials, err := s.buildChannel(input, false)
	if err != nil {
		return Channel{}, err
	}

	params := UpdateChannelParams{Config: config}
	if name := strings.TrimSpace(input.Name); name != "" {
		params.Name = &name
	}
	if input.MinSeverity != "" {
		params.MinSeverity = &input.MinSeverity
	}
	if input.Kinds != nil {
		params.Kinds = input.Kinds
	}
	params.Enabled = input.Enabled

	if credentials != (credentialFields{}) {
		sealed, err := s.encrypt(credentials, id)
		if err != nil {
			return Channel{}, err
		}
		params.Credentials = &sealed
	}

	channel, err := s.repo.UpdateChannel(ctx, id, params)
	if err != nil {
		return Channel{}, err
	}

	// Changing where a channel points invalidates what the panel knew about
	// reaching it. Leaving the old success mark would be the panel vouching for
	// somewhere it has never delivered.
	if err := s.repo.RecordChannelResult(ctx, id, false,
		"this channel has changed since it last delivered anything; test it",
		s.now().UTC()); err != nil {
		return Channel{}, err
	}
	// The streak is a count of *failures*, and a configuration change is not
	// one. It is reset so a working channel that was edited does not appear
	// broken.
	if err := s.repo.ResetStreak(ctx, id); err != nil {
		return Channel{}, err
	}

	s.record(ctx, actor, ActionChannelUpdate, id, map[string]any{
		"name": channel.Name, "kind": channel.Kind,
		"credentials_changed": params.Credentials != nil,
	})
	return s.repo.GetChannel(ctx, id)
}

// DeleteChannel removes a channel.
func (s *Service) DeleteChannel(ctx context.Context, id string, actor Actor) error {
	channel, err := s.repo.GetChannel(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteChannel(ctx, id); err != nil {
		return err
	}
	s.record(ctx, actor, ActionChannelDelete, id,
		map[string]any{"name": channel.Name, "kind": channel.Kind})
	return nil
}

// Test sends a message through one channel, now, and reports what happened.
//
// This is the equivalent of Phase 14's destination check, and it exists for the
// same reason: a channel nobody has ever delivered through looks like protection
// and is not. It is synchronous rather than queued, because the whole value is
// that somebody is watching when it fails.
func (s *Service) Test(ctx context.Context, id string, actor Actor) (Channel, error) {
	channel, err := s.repo.GetChannel(ctx, id)
	if err != nil {
		return Channel{}, err
	}

	message := Message{
		Severity: validate.NotifyInfo,
		Title:    "Test notification from JotHost Panel",
		Body: "If you are reading this, this channel works. It was sent by hand " +
			"from the notifications page, not by anything going wrong.",
		Link: "/notifications",
	}

	sendErr := s.send(ctx, channel, message)
	now := s.now().UTC()

	detail := ""
	if sendErr != nil {
		detail = sendErr.Error()
	}
	if err := s.repo.RecordChannelResult(ctx, id, sendErr == nil, detail, now); err != nil {
		return Channel{}, err
	}

	s.record(ctx, actor, ActionChannelTest, id, map[string]any{
		"name": channel.Name, "kind": channel.Kind, "ok": sendErr == nil,
		"detail": detail,
	})

	// The failure is returned on the channel rather than as an error: "we could
	// not reach it, and here is what the server said" is the answer, and it
	// belongs on the channel where the page shows it.
	return s.repo.GetChannel(ctx, id)
}

// send delivers one message through one channel.
func (s *Service) send(ctx context.Context, channel Channel, message Message) error {
	credentials, err := s.decrypt(ctx, channel)
	if err != nil {
		return err
	}
	sender, err := s.senderFor(channel, credentials)
	if err != nil {
		return err
	}
	return sender.Send(ctx, message)
}

// buildChannel validates an input and splits it into config and secret.
func (s *Service) buildChannel(input ChannelInput, requireSecret bool) (
	map[string]any, credentialFields, error,
) {
	if err := validate.ChannelName(input.Name); err != nil {
		return nil, credentialFields{}, err
	}
	if err := validate.ChannelKind(input.Kind); err != nil {
		return nil, credentialFields{}, err
	}
	if input.MinSeverity != "" {
		if err := validate.NotifySeverity(input.MinSeverity); err != nil {
			return nil, credentialFields{}, err
		}
	}
	for _, kind := range input.Kinds {
		if err := validate.EventKind(kind); err != nil {
			return nil, credentialFields{}, err
		}
	}

	config := map[string]any{}
	credentials := credentialFields{}

	switch input.Kind {
	case validate.ChannelEmail:
		if err := validate.SMTPHost(input.Host); err != nil {
			return nil, credentialFields{}, err
		}
		port := input.Port
		if port == 0 {
			port = 587
		}
		if err := validate.SMTPPort(port); err != nil {
			return nil, credentialFields{}, err
		}
		security := input.Security
		if security == "" {
			security = validate.SMTPStartTLS
		}
		if err := validate.SMTPSecurity(security, input.Host, input.AllowInsecure); err != nil {
			return nil, credentialFields{}, err
		}

		from, err := validate.EmailAddress(input.From)
		if err != nil {
			return nil, credentialFields{}, err
		}
		if len(input.To) == 0 {
			return nil, credentialFields{}, fmt.Errorf(
				"%w: an email channel needs at least one recipient", ErrInvalidChannel)
		}
		recipients := make([]string, 0, len(input.To))
		for _, address := range input.To {
			parsed, err := validate.EmailAddress(address)
			if err != nil {
				return nil, credentialFields{}, err
			}
			recipients = append(recipients, parsed)
		}

		// An unencrypted connection may carry a message; it may never carry a
		// password.
		//
		// These are two different concessions and only the first is
		// defensible. A relay on a private network that accepts mail from the
		// local network by address is an ordinary arrangement; sending it a
		// password in the clear is giving the password away, and the alert it
		// was protecting is the least of what is lost.
		//
		// Go's smtp.PlainAuth refuses this too, and refusing it here means the
		// operator is told at the moment they configure it rather than by a
		// terse failure the first night something goes wrong.
		if security == validate.SMTPPlain && !isLoopback(input.Host) &&
			(input.Password != "" || strings.TrimSpace(input.Username) != "") {
			return nil, credentialFields{}, fmt.Errorf(
				"%w: a password cannot be sent over an unencrypted connection. "+
					"Use TLS, or leave the username and password empty if the relay "+
					"accepts mail from this machine without them",
				ErrInvalidChannel)
		}

		config["host"] = strings.TrimSpace(input.Host)
		config["port"] = port
		config["security"] = security
		config["allow_insecure"] = input.AllowInsecure
		config["username"] = strings.TrimSpace(input.Username)
		config["from"] = from
		config["to"] = toAnySlice(recipients)
		credentials.Password = input.Password

	case validate.ChannelTelegram:
		if err := validate.ChatRecipient(input.ChatID); err != nil {
			return nil, credentialFields{}, err
		}
		if requireSecret || input.Token != "" {
			if err := validate.BotToken(input.Token); err != nil {
				return nil, credentialFields{}, err
			}
		}
		config["chat_id"] = input.ChatID
		credentials.Token = input.Token

	case validate.ChannelLINE:
		if err := validate.ChatRecipient(input.Recipient); err != nil {
			return nil, credentialFields{}, err
		}
		if requireSecret || input.Token != "" {
			if err := validate.BotToken(input.Token); err != nil {
				return nil, credentialFields{}, err
			}
		}
		config["to"] = input.Recipient
		credentials.Token = input.Token
	}

	if requireSecret && credentials == (credentialFields{}) {
		if input.Kind != validate.ChannelEmail {
			return nil, credentialFields{}, fmt.Errorf(
				"%w: this channel needs a token", ErrInvalidChannel)
		}
		// An email channel with no password is legitimate — an internal relay
		// that accepts mail from this machine by address needs none — and the
		// schema still requires *something* encrypted, so an empty credential
		// set is sealed rather than left absent. What is stored is the fact
		// that there is no password, which is different from the column being
		// blank because nobody set it.
		credentials.Password = ""
	}
	return config, credentials, nil
}

// encrypt seals a channel's secret against its row.
func (s *Service) encrypt(credentials credentialFields, id string) (string, error) {
	encoded, err := json.Marshal(credentials)
	if err != nil {
		return "", fmt.Errorf("encode channel credentials: %w", err)
	}
	sealed, err := s.crypto.Encrypt(encoded, encryptionContext+":"+id)
	if err != nil {
		return "", fmt.Errorf("encrypt channel credentials: %w", err)
	}
	return sealed, nil
}

// decrypt reads a channel's secret for the length of one send.
func (s *Service) decrypt(ctx context.Context, channel Channel) (credentialFields, error) {
	sealed, err := s.repo.ChannelSecret(ctx, channel.ID)
	if err != nil {
		return credentialFields{}, err
	}
	if sealed == "" {
		return credentialFields{}, fmt.Errorf(
			"%w: this channel has no stored credentials", ErrPermanent)
	}

	plaintext, err := s.crypto.Decrypt(sealed, encryptionContext+":"+channel.ID)
	if err != nil {
		// Deliberately not wrapped with the cryptographic failure, which says
		// nothing useful and could say something about the key.
		return credentialFields{}, fmt.Errorf(
			"%w: this channel's stored credentials could not be read", ErrPermanent)
	}

	var credentials credentialFields
	if err := json.Unmarshal(plaintext, &credentials); err != nil {
		return credentialFields{}, fmt.Errorf(
			"%w: this channel's stored credentials could not be read", ErrPermanent)
	}
	return credentials, nil
}

func toAnySlice(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// record writes an audit entry.
func (s *Service) record(ctx context.Context, actor Actor, action, resourceID string,
	details map[string]any,
) {
	if s.audit == nil {
		return
	}
	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       actor.UserID,
		Action:       action,
		ResourceType: ResourceTypeChannel,
		ResourceID:   resourceID,
		IPAddress:    actor.IPAddress,
		UserAgent:    actor.UserAgent,
		Status:       "success",
		Metadata:     details,
	})
}

// isLoopback reports whether a host names this machine.
//
// Duplicated from shared/validate rather than exported from it, because there
// it is an implementation detail of one rule and here it is a second, different
// rule about the same host. Two callers with two reasons is not shared logic.
func isLoopback(host string) bool {
	trimmed := strings.TrimSpace(host)
	if trimmed == "localhost" {
		return true
	}
	if ip := net.ParseIP(trimmed); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
