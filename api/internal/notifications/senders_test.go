package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jothost/panel/shared/validate"
)

// The senders, against stub servers.
//
// Two things are worth testing here and the rest is plumbing: the shape of what
// goes out, and whether a failure is classified as worth retrying. Getting the
// second wrong is what turns a wrong password into a queue that never drains.

func testMessage() Message {
	return Message{
		Severity: validate.NotifyCritical,
		Title:    "Disk nearly full (/var): 96% is above 90%",
		Body:     "The condition has held long enough to be worth telling you about.",
		Link:     "/monitoring",
	}
}

// ------------------------------------------------------------- telegram

func TestTelegramSendsTheMessageAsPlainText(t *testing.T) {
	var body map[string]any
	var path string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	sender := &telegramSender{
		client: server.Client(), baseURL: server.URL,
		token: "123:ABC", chatID: "-1001234", panelURL: "https://panel.example",
	}
	if err := sender.Send(context.Background(), testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if path != "/bot123:ABC/sendMessage" {
		t.Errorf("path = %q", path)
	}
	if body["chat_id"] != "-1001234" {
		t.Errorf("chat_id = %v", body["chat_id"])
	}

	text, _ := body["text"].(string)
	if !strings.Contains(text, "Disk nearly full") {
		t.Errorf("text does not carry the title: %q", text)
	}
	if !strings.Contains(text, "https://panel.example/monitoring") {
		t.Errorf("text does not carry a link somebody can follow: %q", text)
	}

	// No parse mode. Telegram's Markdown and HTML modes reject a message with
	// an unbalanced character, and this text is composed from a host's own
	// output — a filename with an underscore would be enough to make an alert
	// fail to send.
	if _, present := body["parse_mode"]; present {
		t.Error("a parse mode was set, which makes delivery depend on the text")
	}
}

func TestTelegramTreatsARefusalAsPermanent(t *testing.T) {
	// A wrong bot token or a chat the bot was removed from stays wrong.
	// Retrying four times reaches the same answer four times and delays every
	// notification behind it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
	}))
	defer server.Close()

	sender := &telegramSender{
		client: server.Client(), baseURL: server.URL, token: "bad", chatID: "1",
	}
	err := sender.Send(context.Background(), testMessage())
	if !errors.Is(err, ErrPermanent) {
		t.Fatalf("Send = %v, want ErrPermanent", err)
	}
	if !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("the error does not carry what the service said: %v", err)
	}
}

func TestTelegramTreatsRateLimitingAsWorthRetrying(t *testing.T) {
	// The one 4xx that is not a configuration problem. Giving up on it would
	// drop a notification for being too prompt.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	sender := &telegramSender{
		client: server.Client(), baseURL: server.URL, token: "t", chatID: "1",
	}
	err := sender.Send(context.Background(), testMessage())
	if errors.Is(err, ErrPermanent) {
		t.Fatalf("rate limiting was treated as permanent: %v", err)
	}
	if !errors.Is(err, ErrSendFailed) {
		t.Fatalf("Send = %v, want ErrSendFailed", err)
	}
}

func TestTelegramTreatsAServerErrorAsWorthRetrying(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	sender := &telegramSender{
		client: server.Client(), baseURL: server.URL, token: "t", chatID: "1",
	}
	if err := sender.Send(context.Background(), testMessage()); errors.Is(err, ErrPermanent) {
		t.Fatalf("a 502 was treated as permanent: %v", err)
	}
}

func TestTelegramRefusesAChannelWithNoToken(t *testing.T) {
	_, err := newTelegramSender(Channel{Config: map[string]any{"chat_id": "1"}},
		credentialFields{}, http.DefaultClient, "")
	if !errors.Is(err, ErrPermanent) {
		t.Fatalf("newTelegramSender = %v, want ErrPermanent", err)
	}
}

// ------------------------------------------------------------------ LINE

func TestLINESendsToTheMessagingAPI(t *testing.T) {
	var body map[string]any
	var auth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := &lineSender{
		client: server.Client(), baseURL: server.URL,
		token: "channel-token", to: "U1234", panelURL: "https://panel.example",
	}
	if err := sender.Send(context.Background(), testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if auth != "Bearer channel-token" {
		t.Errorf("Authorization = %q", auth)
	}
	if body["to"] != "U1234" {
		t.Errorf("to = %v", body["to"])
	}
	messages, _ := body["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %v", messages)
	}
}

func TestLINETruncatesAMessageItWouldRefuse(t *testing.T) {
	// LINE refuses a message over 5000 characters outright. Truncating is
	// better than a delivery that fails for a reason the operator cannot see
	// from the panel.
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := &lineSender{
		client: server.Client(), baseURL: server.URL, token: "t", to: "U1",
	}
	message := testMessage()
	message.Body = strings.Repeat("x", 9000)
	if err := sender.Send(context.Background(), message); err != nil {
		t.Fatalf("Send: %v", err)
	}

	messages, _ := body["messages"].([]any)
	first, _ := messages[0].(map[string]any)
	text, _ := first["text"].(string)
	if len(text) > 5000 {
		t.Errorf("text is %d characters, which LINE would refuse", len(text))
	}
	if !strings.HasSuffix(text, "…") {
		t.Error("a truncated message does not say it was truncated")
	}
}

// ----------------------------------------------------------------- email

func TestEmailComposesHeadersAndBody(t *testing.T) {
	sender := &emailSender{
		host: "mail.example", port: 587, from: "panel@example.com",
		to: []string{"ops@example.com"}, panelURL: "https://panel.example",
	}

	raw, err := sender.compose(testMessage())
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	body := string(raw)

	for _, want := range []string{
		"From: panel@example.com\r\n",
		"To: ops@example.com\r\n",
		"Subject: [CRITICAL] Disk nearly full",
		"Content-Type: text/plain; charset=utf-8\r\n",
		"https://panel.example/monitoring",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the message does not contain %q:\n%s", want, body)
		}
	}
}

func TestEmailRefusesAHeaderCarryingALineBreak(t *testing.T) {
	// The subject is composed from a host's own output — a process name, a
	// mount point, a domain — and a newline in one of those is how a message
	// gains a header its author did not write: a Bcc, or a second message.
	sender := &emailSender{
		host: "mail.example", from: "panel@example.com", to: []string{"ops@example.com"},
	}

	message := testMessage()
	message.Title = "Disk full\r\nBcc: attacker@example.com"

	if _, err := sender.compose(message); !errors.Is(err, ErrPermanent) {
		t.Fatalf("compose = %v, want it refused as permanent", err)
	}
}

func TestEmailEscapesALineThatWouldEndTheMessage(t *testing.T) {
	// SMTP ends a body with a line containing one dot, so a body with such a
	// line would truncate the message there. Go's net/smtp does not do this.
	sender := &emailSender{
		host: "mail.example", from: "panel@example.com", to: []string{"ops@example.com"},
	}

	message := testMessage()
	message.Body = "before\n.\nafter"

	raw, err := sender.compose(message)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if !strings.Contains(string(raw), "\r\n..\r\n") {
		t.Errorf("a lone dot was not escaped:\n%s", raw)
	}
	if !strings.Contains(string(raw), "after") {
		t.Error("the message was truncated at the dot")
	}
}

func TestEmailRefusesAChannelWithNoRecipients(t *testing.T) {
	_, err := newEmailSender(Channel{Config: map[string]any{
		"host": "mail.example", "from": "panel@example.com",
	}}, credentialFields{}, "")
	if !errors.Is(err, ErrPermanent) {
		t.Fatalf("newEmailSender = %v, want ErrPermanent", err)
	}
}

// ------------------------------------------------------------ redaction

func TestATransportErrorNeverCarriesTheBotToken(t *testing.T) {
	// A Telegram URL carries the bot token in its path, and a transport error
	// containing it would put a full credential into the delivery record —
	// which is shown in the panel and kept for weeks.
	err := errors.New(
		`Post "https://api.telegram.org/bot7654321:AAH-secret/sendMessage": dial tcp: timeout`)

	cleaned := cleanNetworkError(err)
	if strings.Contains(cleaned, "AAH-secret") || strings.Contains(cleaned, "7654321") {
		t.Fatalf("the token survived redaction: %q", cleaned)
	}
	if !strings.Contains(cleaned, "timeout") {
		t.Errorf("redaction removed the useful part: %q", cleaned)
	}
}

func TestRedactionLeavesAnOrdinaryErrorReadable(t *testing.T) {
	cleaned := cleanNetworkError(errors.New("dial tcp 10.0.0.5:587: connection refused"))
	if !strings.Contains(cleaned, "connection refused") {
		t.Errorf("cleanNetworkError = %q", cleaned)
	}
}

// ------------------------------------------------------------ selection

func TestAChannelWithNoKindsListedWantsEverything(t *testing.T) {
	// The right default: a channel that silently excluded a category would be
	// one somebody believes is watching something it is not.
	channel := Channel{Enabled: true, MinSeverity: validate.NotifyInfo}
	event := Event{Kind: validate.EventBackupFailed, Severity: validate.NotifyCritical}

	if !wants(channel, event) {
		t.Fatal("a channel with no kinds listed rejected an event")
	}
}

func TestAChannelOnlyWantsWhatMeetsItsFloor(t *testing.T) {
	channel := Channel{Enabled: true, MinSeverity: validate.NotifyHigh}

	if !wants(channel, Event{Severity: validate.NotifyCritical}) {
		t.Error("a critical event did not meet a high floor")
	}
	if !wants(channel, Event{Severity: validate.NotifyHigh}) {
		t.Error("a high event did not meet a high floor")
	}
	if wants(channel, Event{Severity: validate.NotifyWarning}) {
		t.Error("a warning met a high floor")
	}
}

func TestADisabledChannelWantsNothing(t *testing.T) {
	channel := Channel{Enabled: false, MinSeverity: validate.NotifyInfo}
	if wants(channel, Event{Severity: validate.NotifyCritical}) {
		t.Fatal("a disabled channel accepted an event")
	}
}

func TestAChannelWithKindsListedWantsOnlyThose(t *testing.T) {
	channel := Channel{
		Enabled:     true,
		MinSeverity: validate.NotifyInfo,
		Kinds:       []string{validate.EventBackupFailed},
	}

	if !wants(channel, Event{
		Kind: validate.EventBackupFailed, Severity: validate.NotifyCritical,
	}) {
		t.Error("a listed kind was rejected")
	}
	if wants(channel, Event{
		Kind: validate.EventAlertOpened, Severity: validate.NotifyCritical,
	}) {
		t.Error("an unlisted kind was accepted")
	}
}

// -------------------------------------------------------------- backoff

func TestBackoffGrows(t *testing.T) {
	// Exponential rather than fixed, because the failures worth retrying are
	// outages — and one that has already lasted five minutes is more likely to
	// last another five than to end in the next thirty seconds.
	first := backoff(1)
	second := backoff(2)
	third := backoff(3)

	if !(first < second && second < third) {
		t.Errorf("backoff does not grow: %v, %v, %v", first, second, third)
	}
}

// -------------------------------------------------------------- healthy

func TestAChannelThatHasNeverDeliveredIsNotHealthy(t *testing.T) {
	// "We have never got a message through this" and "this is fine" are
	// different facts, and only one of them should be shown in green.
	channel := Channel{FailureStreak: 0}
	if channel.Healthy() {
		t.Fatal("a channel that has never delivered anything reported itself healthy")
	}
}
