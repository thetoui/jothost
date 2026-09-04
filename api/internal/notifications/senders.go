package notifications

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/jothost/panel/shared/validate"
)

// The three senders.
//
// # Why the endpoints are compiled in
//
// Telegram and LINE each publish one API host, and those are the constants
// below. There is no configuration that changes them and no "webhook" channel
// at all, because a notification channel that accepts a URL is a request forger
// sitting inside the panel — pointed at whatever an admin account can be talked
// into typing, from a machine that sits on the private network next to every
// site it hosts and can reach a cloud metadata endpoint.
//
// Email is the exception, and it is a real one: its host is the operator's own
// mail server. That is a thing they already run rather than a thing they were
// persuaded to point at, and there is no way to send mail without it.
//
// # Why the standard library
//
// net/smtp and net/http are enough for all three. The API module already has
// dependencies, so this is not the Agent's no-dependencies rule — it is that a
// mail library and two API clients would be three more things to keep current
// for four requests between them.

// The published endpoints. Not configurable.
const (
	telegramAPI = "https://api.telegram.org"
	lineAPI     = "https://api.line.me"
)

// Timeouts. Generous enough for a slow relay, short enough that a dead one does
// not hold a dispatcher worker while alerts queue behind it.
const (
	smtpTimeout = 30 * time.Second
	httpTimeout = 30 * time.Second
)

// maxResponseBytes bounds what a sender reads back from an API.
//
// The body is only ever used to explain a failure, and it is somebody else's
// text going into this panel's delivery record.
const maxResponseBytes = 4 << 10

// Errors senders return.
var (
	// ErrSendFailed wraps a refusal from the far end. It is worth
	// distinguishing from a transport failure because one is worth retrying and
	// the other usually is not — a wrong password stays wrong.
	ErrSendFailed = errors.New("the notification was not accepted")
	// ErrPermanent means retrying will not help. The dispatcher gives up
	// immediately rather than backing off four times to reach the same answer.
	ErrPermanent = errors.New("the notification cannot be delivered as configured")
)

// Message is what a sender delivers.
type Message struct {
	Severity string
	Title    string
	Body     string
	// Link is a panel path. A notification that says something is wrong without
	// saying where to look costs the reader more than it gives them.
	Link string
}

// Sender delivers one message through one channel.
type Sender interface {
	Send(ctx context.Context, message Message) error
}

// senderFor builds the sender for a channel.
func (s *Service) senderFor(channel Channel, credentials credentialFields) (Sender, error) {
	switch channel.Kind {
	case validate.ChannelEmail:
		return newEmailSender(channel, credentials, s.panelURL)
	case validate.ChannelTelegram:
		return newTelegramSender(channel, credentials, s.http, s.panelURL)
	case validate.ChannelLINE:
		return newLINESender(channel, credentials, s.http, s.panelURL)
	default:
		return nil, fmt.Errorf("%w: %s", ErrPermanent, channel.Kind)
	}
}

// ------------------------------------------------------------------ email

type emailSender struct {
	host     string
	port     int
	security string
	username string
	password string
	from     string
	to       []string
	panelURL string
}

func newEmailSender(channel Channel, credentials credentialFields, panelURL string) (
	Sender, error,
) {
	config := channel.Config
	sender := &emailSender{
		host:     configString(config, "host"),
		port:     configInt(config, "port"),
		security: configString(config, "security"),
		username: configString(config, "username"),
		password: credentials.Password,
		from:     configString(config, "from"),
		to:       configStrings(config, "to"),
		panelURL: panelURL,
	}
	if sender.port == 0 {
		sender.port = 587
	}
	if sender.security == "" {
		sender.security = validate.SMTPStartTLS
	}
	if len(sender.to) == 0 || sender.from == "" || sender.host == "" {
		return nil, fmt.Errorf("%w: this channel has no mail server, sender or recipients",
			ErrPermanent)
	}
	return sender, nil
}

// Send delivers one message over SMTP.
func (e *emailSender) Send(ctx context.Context, message Message) error {
	body, err := e.compose(message)
	if err != nil {
		return err
	}

	address := net.JoinHostPort(e.host, strconv.Itoa(e.port))
	dialer := &net.Dialer{Timeout: smtpTimeout}

	var conn net.Conn
	if e.security == validate.SMTPImplicit {
		// TLS from the first byte. The ServerName is the configured host, so a
		// certificate for something else fails here rather than being accepted
		// because it was presented.
		conn, err = tls.DialWithDialer(dialer, "tcp", address,
			&tls.Config{ServerName: e.host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return fmt.Errorf("%w: %s", ErrSendFailed, cleanNetworkError(err))
	}
	defer func() { _ = conn.Close() }()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(smtpTimeout))
	}

	client, err := smtp.NewClient(conn, e.host)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrSendFailed, cleanNetworkError(err))
	}
	defer func() { _ = client.Close() }()

	if e.security == validate.SMTPStartTLS {
		ok, _ := client.Extension("STARTTLS")
		if !ok {
			// Refused rather than silently continuing in the clear. A channel
			// configured for STARTTLS that quietly sent plaintext would be the
			// worst outcome available: the operator asked for encryption and
			// believes they have it.
			return fmt.Errorf(
				"%w: the mail server does not offer STARTTLS, and this channel is "+
					"configured to require it", ErrPermanent)
		}
		if err := client.StartTLS(&tls.Config{
			ServerName: e.host, MinVersion: tls.VersionTLS12,
		}); err != nil {
			return fmt.Errorf("%w: %s", ErrSendFailed, cleanNetworkError(err))
		}
	}

	if e.username != "" && e.password != "" {
		// PlainAuth refuses to send credentials over an unencrypted connection
		// unless the host is localhost, which is exactly the rule this panel
		// wants and is why it is not replaced with something more permissive.
		auth := smtp.PlainAuth("", e.username, e.password, e.host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("%w: the mail server rejected the credentials: %s",
				ErrPermanent, cleanNetworkError(err))
		}
	}

	if err := client.Mail(e.from); err != nil {
		return fmt.Errorf("%w: the mail server rejected the sender %q: %s",
			ErrPermanent, e.from, cleanNetworkError(err))
	}
	for _, recipient := range e.to {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("%w: the mail server rejected the recipient %q: %s",
				ErrPermanent, recipient, cleanNetworkError(err))
		}
	}

	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("%w: %s", ErrSendFailed, cleanNetworkError(err))
	}
	if _, err := writer.Write(body); err != nil {
		return fmt.Errorf("%w: %s", ErrSendFailed, cleanNetworkError(err))
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("%w: the mail server rejected the message: %s",
			ErrSendFailed, cleanNetworkError(err))
	}
	return client.Quit()
}

// compose builds the message.
//
// Every header value is checked for a line break before it goes in. The subject
// is composed from a host's own output — a process name, a mount point, a
// domain — and a newline in one of those is how a message gains a header its
// author did not write: a Bcc, or a second message entirely.
func (e *emailSender) compose(message Message) ([]byte, error) {
	subject := "[" + strings.ToUpper(message.Severity) + "] " + message.Title
	if err := validate.HeaderText(subject); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrPermanent, err)
	}
	for _, header := range append([]string{e.from}, e.to...) {
		if err := validate.HeaderText(header); err != nil {
			return nil, fmt.Errorf("%w: %s", ErrPermanent, err)
		}
	}

	var buffer bytes.Buffer
	fmt.Fprintf(&buffer, "From: %s\r\n", e.from)
	fmt.Fprintf(&buffer, "To: %s\r\n", strings.Join(e.to, ", "))
	fmt.Fprintf(&buffer, "Subject: %s\r\n", subject)
	fmt.Fprintf(&buffer, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	buffer.WriteString("MIME-Version: 1.0\r\n")
	buffer.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	// So a mail client threads a resolution with the alert it resolves rather
	// than starting a second conversation about the same disk.
	buffer.WriteString("Auto-Submitted: auto-generated\r\n")
	buffer.WriteString("\r\n")

	buffer.WriteString(dotStuff(message.Body))
	if message.Link != "" && e.panelURL != "" {
		fmt.Fprintf(&buffer, "\r\n\r\n%s%s\r\n", strings.TrimRight(e.panelURL, "/"),
			message.Link)
	}
	buffer.WriteString("\r\n")
	return buffer.Bytes(), nil
}

// dotStuff escapes a line consisting of a single dot.
//
// SMTP ends a message body with a line containing one dot, so a body that
// contains such a line would truncate the message there. Go's net/smtp does not
// do this for you.
func dotStuff(body string) string {
	normalised := strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(normalised, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, ".") {
			lines[i] = "." + line
		}
	}
	return strings.Join(lines, "\r\n")
}

// --------------------------------------------------------------- telegram

type telegramSender struct {
	client   *http.Client
	baseURL  string
	token    string
	chatID   string
	panelURL string
}

func newTelegramSender(channel Channel, credentials credentialFields,
	client *http.Client, panelURL string,
) (Sender, error) {
	sender := &telegramSender{
		client:   client,
		baseURL:  telegramAPI,
		token:    credentials.Token,
		chatID:   configString(channel.Config, "chat_id"),
		panelURL: panelURL,
	}
	if sender.token == "" || sender.chatID == "" {
		return nil, fmt.Errorf("%w: this channel has no bot token or chat", ErrPermanent)
	}
	return sender, nil
}

func (t *telegramSender) Send(ctx context.Context, message Message) error {
	payload := map[string]any{
		"chat_id": t.chatID,
		"text":    plainText(message, t.panelURL),
		// No parse mode. Telegram's Markdown and HTML modes both reject a
		// message with an unbalanced character in it, and the text here is
		// composed from a host's own output — a filename with an underscore
		// would be enough to make an alert fail to send.
		"disable_web_page_preview": true,
	}

	// The token is in the path, which is Telegram's design. It is why the URL
	// is never logged and why a transport error has its query and path stripped
	// before it reaches the delivery record.
	url := t.baseURL + "/bot" + t.token + "/sendMessage"
	return postJSON(ctx, t.client, url, nil, payload, "Telegram")
}

// ------------------------------------------------------------------- LINE

type lineSender struct {
	client   *http.Client
	baseURL  string
	token    string
	to       string
	panelURL string
}

func newLINESender(channel Channel, credentials credentialFields,
	client *http.Client, panelURL string,
) (Sender, error) {
	sender := &lineSender{
		client:   client,
		baseURL:  lineAPI,
		token:    credentials.Token,
		to:       configString(channel.Config, "to"),
		panelURL: panelURL,
	}
	if sender.token == "" || sender.to == "" {
		return nil, fmt.Errorf("%w: this channel has no access token or recipient",
			ErrPermanent)
	}
	return sender, nil
}

func (l *lineSender) Send(ctx context.Context, message Message) error {
	text := plainText(message, l.panelURL)
	// LINE refuses a message over 5000 characters outright. Truncating here is
	// better than a delivery that fails for a reason the operator cannot see
	// from the panel.
	const maxLINEText = 4900
	if len(text) > maxLINEText {
		text = text[:maxLINEText] + "\n…"
	}

	payload := map[string]any{
		"to":       l.to,
		"messages": []map[string]any{{"type": "text", "text": text}},
	}
	headers := map[string]string{"Authorization": "Bearer " + l.token}
	return postJSON(ctx, l.client, l.baseURL+"/v2/bot/message/push", headers, payload, "LINE")
}

// ---------------------------------------------------------------- shared

// postJSON sends a JSON body and turns the answer into an error worth storing.
func postJSON(ctx context.Context, client *http.Client, url string,
	headers map[string]string, payload map[string]any, service string,
) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrPermanent, err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url,
		bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("%w: %s", ErrPermanent, err)
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}

	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %s could not be reached: %s",
			ErrSendFailed, service, cleanNetworkError(err))
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}

	body, _ := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	detail := collapseWhitespace(string(body))
	if len(detail) > 300 {
		detail = detail[:300]
	}

	// 4xx other than rate limiting is a configuration problem: a wrong token, a
	// chat the bot was removed from. Retrying four times reaches the same
	// answer four times and delays every notification behind it.
	if response.StatusCode >= 400 && response.StatusCode < 500 &&
		response.StatusCode != http.StatusTooManyRequests {
		return fmt.Errorf("%w: %s refused it (%s): %s",
			ErrPermanent, service, response.Status, detail)
	}
	return fmt.Errorf("%w: %s answered %s: %s",
		ErrSendFailed, service, response.Status, detail)
}

// plainText renders a message for a chat service.
func plainText(message Message, panelURL string) string {
	var builder strings.Builder
	builder.WriteString(strings.ToUpper(message.Severity))
	builder.WriteString(": ")
	builder.WriteString(message.Title)
	if message.Body != "" {
		builder.WriteString("\n\n")
		builder.WriteString(message.Body)
	}
	if message.Link != "" && panelURL != "" {
		builder.WriteString("\n\n")
		builder.WriteString(strings.TrimRight(panelURL, "/"))
		builder.WriteString(message.Link)
	}
	return builder.String()
}

// cleanNetworkError strips a URL's path and query from an error.
//
// A Telegram URL carries the bot token in its path, and a transport error
// containing it would put a full credential into the delivery record — which is
// shown in the panel and kept for weeks.
func cleanNetworkError(err error) string {
	text := err.Error()
	for _, marker := range []string{"/bot", "https://", "http://"} {
		if index := strings.Index(text, marker); index >= 0 {
			end := strings.IndexAny(text[index:], " \"")
			if end < 0 {
				text = text[:index] + "[address]"
			} else {
				text = text[:index] + "[address]" + text[index+end:]
			}
		}
	}
	return collapseWhitespace(text)
}

// collapseWhitespace makes a multi-line error one readable line.
func collapseWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// configString reads a string from a channel's configuration.
func configString(config map[string]any, key string) string {
	value, _ := config[key].(string)
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

func configStrings(config map[string]any, key string) []string {
	raw, ok := config[key].([]any)
	if !ok {
		return nil
	}
	values := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok && text != "" {
			values = append(values, text)
		}
	}
	return values
}
