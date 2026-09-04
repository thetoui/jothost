package notifications

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jothost/panel/api/internal/servers"
	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/shared/validate"
)

// The notification tables, against a real database.
//
// Two properties are worth a database to check, and both are enforced by an
// index rather than by code: one event per thing that happened, and one delivery
// per event per channel. Between them they are the whole of the panel's
// protection against a full disk becoming ten thousand emails.

func setup(t *testing.T) (*Repository, string, context.Context) {
	t.Helper()

	deps := testsupport.Require(t)
	ctx := context.Background()

	server, err := servers.NewRepository(deps.Pool).Register(ctx, servers.RegisterParams{
		Hostname: "notifications-test-host",
	})
	if err != nil {
		t.Fatalf("register server: %v", err)
	}
	return NewRepository(deps.Pool), server.ID, ctx
}

func makeChannel(t *testing.T, repo *Repository, ctx context.Context,
	serverID, name string, overrides func(*CreateChannelParams),
) Channel {
	t.Helper()

	params := CreateChannelParams{
		ServerID:    serverID,
		Name:        name,
		Kind:        validate.ChannelEmail,
		Config:      map[string]any{"host": "mail.example", "from": "panel@example.com"},
		Credentials: "sealed-ciphertext",
		MinSeverity: validate.NotifyWarning,
		Enabled:     true,
	}
	if overrides != nil {
		overrides(&params)
	}

	channel, err := repo.CreateChannel(ctx, params)
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	return channel
}

func makeEvent(serverID, key string) Event {
	return Event{
		ServerID:  serverID,
		Source:    SourceMonitoring,
		Kind:      validate.EventAlertOpened,
		Severity:  validate.NotifyCritical,
		Title:     "Disk nearly full (/var): 96% is above 90%",
		Body:      "The condition has held long enough to be worth telling you about.",
		Link:      "/monitoring",
		DedupeKey: key,
	}
}

func TestAChannelNeverReturnsItsSecret(t *testing.T) {
	// An SMTP password and a bot token are both full credentials, and the
	// struct handlers serialise has no field for either.
	repo, serverID, ctx := setup(t)

	channel := makeChannel(t, repo, ctx, serverID, "Ops mail", nil)

	secret, err := repo.ChannelSecret(ctx, channel.ID)
	if err != nil {
		t.Fatalf("ChannelSecret: %v", err)
	}
	if secret != "sealed-ciphertext" {
		t.Errorf("secret = %q", secret)
	}
}

func TestAChannelMustHaveACredential(t *testing.T) {
	// Every kind here talks to something authenticated, and one with no secret
	// is one that fails at the moment it is first needed.
	repo, serverID, ctx := setup(t)

	_, err := repo.CreateChannel(ctx, CreateChannelParams{
		ServerID: serverID, Name: "no secret", Kind: validate.ChannelEmail,
		Config: map[string]any{}, MinSeverity: validate.NotifyWarning,
	})
	if err == nil {
		t.Fatal("a channel with no credentials was accepted")
	}
}

func TestTwoChannelsCannotShareAName(t *testing.T) {
	repo, serverID, ctx := setup(t)
	makeChannel(t, repo, ctx, serverID, "Ops mail", nil)

	_, err := repo.CreateChannel(ctx, CreateChannelParams{
		ServerID: serverID, Name: "ops MAIL", Kind: validate.ChannelEmail,
		Config: map[string]any{}, Credentials: "x", MinSeverity: validate.NotifyWarning,
	})
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("create = %v, want ErrNameTaken", err)
	}
}

func TestTheSameThingHappeningTwiceIsOneEvent(t *testing.T) {
	// The whole of the panel's protection against flooding. A disk sitting above
	// its threshold for a week is one event, and the monitor re-reading it every
	// minute must not turn that into ten thousand emails.
	repo, serverID, ctx := setup(t)

	first, err := repo.RecordEvent(ctx, makeEvent(serverID, "alert.opened:abc"))
	if err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	_, err = repo.RecordEvent(ctx, makeEvent(serverID, "alert.opened:abc"))
	if !errors.Is(err, ErrDuplicateEvent) {
		t.Fatalf("the second RecordEvent = %v, want ErrDuplicateEvent", err)
	}

	events, err := repo.ListEvents(ctx, serverID, 10)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 || events[0].ID != first.ID {
		t.Fatalf("events = %d, want the one that was recorded first", len(events))
	}
}

func TestDifferentThingsAreDifferentEvents(t *testing.T) {
	repo, serverID, ctx := setup(t)

	if _, err := repo.RecordEvent(ctx, makeEvent(serverID, "alert.opened:abc")); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	// The resolution of the same alert is a different thing, and both are worth
	// sending: an alert that clears itself at four in the morning is the
	// difference between getting up and going back to sleep.
	if _, err := repo.RecordEvent(ctx, makeEvent(serverID, "alert.resolved:abc")); err != nil {
		t.Fatalf("RecordEvent for the resolution: %v", err)
	}

	events, err := repo.ListEvents(ctx, serverID, 10)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("events = %d, want 2", len(events))
	}
}

func TestQueueingTheSameDeliveryTwiceIsOneDelivery(t *testing.T) {
	// A dispatcher restarted mid-run must be able to queue the same event again
	// without producing a second message.
	repo, serverID, ctx := setup(t)
	channel := makeChannel(t, repo, ctx, serverID, "Ops mail", nil)

	event, err := repo.RecordEvent(ctx, makeEvent(serverID, "alert.opened:abc"))
	if err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := repo.QueueDelivery(ctx, event.ID, channel.ID); err != nil {
			t.Fatalf("QueueDelivery (attempt %d): %v", i+1, err)
		}
	}

	deliveries, err := repo.ListDeliveries(ctx, serverID, "", 10)
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(deliveries))
	}
}

func TestClaimingADeliveryCountsTheAttempt(t *testing.T) {
	// The counter is incremented as part of claiming, so a dispatcher that
	// crashed after sending but before recording sends again once rather than
	// forever. Duplicating an alert is a much better failure than an infinite
	// loop of them.
	repo, serverID, ctx := setup(t)
	channel := makeChannel(t, repo, ctx, serverID, "Ops mail", nil)

	event, err := repo.RecordEvent(ctx, makeEvent(serverID, "alert.opened:abc"))
	if err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	if err := repo.QueueDelivery(ctx, event.ID, channel.ID); err != nil {
		t.Fatalf("QueueDelivery: %v", err)
	}

	due, err := repo.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("due = %d, want 1", len(due))
	}
	if due[0].Delivery.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", due[0].Delivery.Attempts)
	}
	// Everything needed to send, in one query: a dispatcher that had to fetch
	// the channel separately would be one query per message.
	if due[0].Channel.Name != "Ops mail" || due[0].Event.Title == "" {
		t.Errorf("the claim did not carry the channel and event: %+v", due[0])
	}
}

func TestADeliveryToADisabledChannelIsNotClaimed(t *testing.T) {
	repo, serverID, ctx := setup(t)
	channel := makeChannel(t, repo, ctx, serverID, "Ops mail", nil)

	event, err := repo.RecordEvent(ctx, makeEvent(serverID, "alert.opened:abc"))
	if err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	if err := repo.QueueDelivery(ctx, event.ID, channel.ID); err != nil {
		t.Fatalf("QueueDelivery: %v", err)
	}

	disabled := false
	if _, err := repo.UpdateChannel(ctx, channel.ID,
		UpdateChannelParams{Enabled: &disabled}); err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}

	due, err := repo.ClaimDue(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("due = %d, want none: a disabled channel receives nothing", len(due))
	}
}

func TestARescheduledDeliveryIsNotClaimedUntilItIsDue(t *testing.T) {
	repo, serverID, ctx := setup(t)
	channel := makeChannel(t, repo, ctx, serverID, "Ops mail", nil)

	event, err := repo.RecordEvent(ctx, makeEvent(serverID, "alert.opened:abc"))
	if err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	if err := repo.QueueDelivery(ctx, event.ID, channel.ID); err != nil {
		t.Fatalf("QueueDelivery: %v", err)
	}

	now := time.Now().UTC()
	due, err := repo.ClaimDue(ctx, now, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("ClaimDue: %v, %d", err, len(due))
	}
	if err := repo.Reschedule(ctx, due[0].Delivery.ID, "the relay was busy",
		now.Add(time.Hour)); err != nil {
		t.Fatalf("Reschedule: %v", err)
	}

	again, err := repo.ClaimDue(ctx, now, 10)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(again) != 0 {
		t.Fatal("a rescheduled delivery was claimed before it was due")
	}

	later, err := repo.ClaimDue(ctx, now.Add(2*time.Hour), 10)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(later) != 1 {
		t.Fatal("a rescheduled delivery was not claimed once it was due")
	}
	if later[0].Delivery.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", later[0].Delivery.Attempts)
	}
}

func TestAFailedDeliveryAlwaysSaysWhy(t *testing.T) {
	// A failure with no reason is a failure nobody can act on, and acting on it
	// is the entire point of storing it. The schema enforces it too.
	repo, serverID, ctx := setup(t)
	channel := makeChannel(t, repo, ctx, serverID, "Ops mail", nil)

	event, err := repo.RecordEvent(ctx, makeEvent(serverID, "alert.opened:abc"))
	if err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	if err := repo.QueueDelivery(ctx, event.ID, channel.ID); err != nil {
		t.Fatalf("QueueDelivery: %v", err)
	}
	due, _ := repo.ClaimDue(ctx, time.Now().UTC(), 10)

	// An empty reason is replaced rather than refused, because the alternative
	// is a delivery stuck pending forever because nothing could name its
	// failure.
	if err := repo.MarkFailed(ctx, due[0].Delivery.ID, ""); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	deliveries, err := repo.ListDeliveries(ctx, serverID, StatusFailed, 10)
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("failed deliveries = %d, want 1", len(deliveries))
	}
	if deliveries[0].LastError == nil || *deliveries[0].LastError == "" {
		t.Error("a failed delivery carries no reason")
	}
}

func TestAChannelCountsConsecutiveFailures(t *testing.T) {
	// The failure streak is what the panel has instead of a way to tell somebody
	// their notifications are broken: it cannot send that message through the
	// thing that is broken, so it counts, and the page shows the count.
	repo, serverID, ctx := setup(t)
	channel := makeChannel(t, repo, ctx, serverID, "Ops mail", nil)
	now := time.Now().UTC()

	for i := 0; i < 3; i++ {
		if err := repo.RecordChannelResult(ctx, channel.ID, false,
			"connection refused", now); err != nil {
			t.Fatalf("RecordChannelResult: %v", err)
		}
	}

	stored, err := repo.GetChannel(ctx, channel.ID)
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if stored.FailureStreak != 3 {
		t.Errorf("failure streak = %d, want 3", stored.FailureStreak)
	}
	if stored.Healthy() {
		t.Error("a channel that has failed three times in a row reported itself healthy")
	}

	// One success clears it. A channel that came back must not stay red.
	if err := repo.RecordChannelResult(ctx, channel.ID, true, "", now); err != nil {
		t.Fatalf("RecordChannelResult: %v", err)
	}
	stored, _ = repo.GetChannel(ctx, channel.ID)
	if stored.FailureStreak != 0 {
		t.Errorf("failure streak = %d after a success, want 0", stored.FailureStreak)
	}
	if !stored.Healthy() {
		t.Error("a channel that delivered successfully is not healthy")
	}
}

func TestStatsCountBrokenAndUntestedChannelsSeparately(t *testing.T) {
	// "We have never got a message through this" and "this stopped working" are
	// different facts with different fixes.
	repo, serverID, ctx := setup(t)
	now := time.Now().UTC()

	broken := makeChannel(t, repo, ctx, serverID, "Broken", nil)
	makeChannel(t, repo, ctx, serverID, "Never tested", nil)
	working := makeChannel(t, repo, ctx, serverID, "Working", nil)

	if err := repo.RecordChannelResult(ctx, broken.ID, true, "", now.Add(-time.Hour)); err != nil {
		t.Fatalf("RecordChannelResult: %v", err)
	}
	if err := repo.RecordChannelResult(ctx, broken.ID, false, "refused", now); err != nil {
		t.Fatalf("RecordChannelResult: %v", err)
	}
	if err := repo.RecordChannelResult(ctx, working.ID, true, "", now); err != nil {
		t.Fatalf("RecordChannelResult: %v", err)
	}

	stats, err := repo.Stats(ctx, serverID, now.AddDate(0, 0, -7))
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.BrokenChannels != 1 {
		t.Errorf("broken = %d, want 1", stats.BrokenChannels)
	}
	if stats.UntestedChannels != 1 {
		t.Errorf("untested = %d, want 1", stats.UntestedChannels)
	}
}

func TestDeletingAChannelKeepsTheEvents(t *testing.T) {
	// The deliveries go with it — a delivery to somewhere that no longer exists
	// describes an attempt to reach nowhere — but the record of what happened
	// on this host is not the channel's to take with it.
	repo, serverID, ctx := setup(t)
	channel := makeChannel(t, repo, ctx, serverID, "Ops mail", nil)

	event, err := repo.RecordEvent(ctx, makeEvent(serverID, "alert.opened:abc"))
	if err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	if err := repo.QueueDelivery(ctx, event.ID, channel.ID); err != nil {
		t.Fatalf("QueueDelivery: %v", err)
	}

	if err := repo.DeleteChannel(ctx, channel.ID); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}

	events, err := repo.ListEvents(ctx, serverID, 10)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("events = %d, want the one that happened to survive its channel",
			len(events))
	}

	deliveries, err := repo.ListDeliveries(ctx, serverID, "", 10)
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if len(deliveries) != 0 {
		t.Errorf("deliveries = %d, want none", len(deliveries))
	}
}

func TestPruningRemovesEventsAndTheirDeliveries(t *testing.T) {
	repo, serverID, ctx := setup(t)
	channel := makeChannel(t, repo, ctx, serverID, "Ops mail", nil)

	event, err := repo.RecordEvent(ctx, makeEvent(serverID, "alert.opened:abc"))
	if err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	if err := repo.QueueDelivery(ctx, event.ID, channel.ID); err != nil {
		t.Fatalf("QueueDelivery: %v", err)
	}

	// Nothing is old enough yet.
	removed, err := repo.PruneEvents(ctx, time.Now().UTC().AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("PruneEvents: %v", err)
	}
	if removed != 0 {
		t.Errorf("pruning removed %d recent event(s)", removed)
	}

	removed, err = repo.PruneEvents(ctx, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("PruneEvents: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	deliveries, err := repo.ListDeliveries(ctx, serverID, "", 10)
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if len(deliveries) != 0 {
		t.Error("a delivery outlived the event it delivered")
	}
}

func TestASchemaLevelGuardOnSeverity(t *testing.T) {
	repo, serverID, ctx := setup(t)

	event := makeEvent(serverID, "alert.opened:abc")
	event.Severity = "catastrophic"
	if _, err := repo.RecordEvent(ctx, event); err == nil {
		t.Fatal("a severity outside the scale was accepted")
	}
}
