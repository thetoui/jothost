package mail

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// nowUnix is the clock, replaceable in tests.
var nowUnix = func() int64 { return time.Now().Unix() }

// Status reports what this host's mail server is actually doing.
//
// "Actually" is the whole design of this function. Every value here is read
// from the host — from Postfix's own view of its configuration, from the
// sockets that are or are not listening, from the certificate file on disk —
// rather than echoed back from what the panel asked for. The panel's record is
// what it *wanted*; this is what it got, and the two disagreeing is the most
// useful thing a status page can show.
func (p *Provider) Status(ctx context.Context, canInstall bool) Status {
	status := Status{CanInstall: canInstall}

	if p == nil || p.runner == nil {
		status.Reason = ErrUnavailable.Error()
		return status
	}

	status.Postfix = p.daemonStatus(ctx, DaemonPostfix,
		p.runner.Available(CommandPostconf), p.postfixVersion(ctx))
	status.Dovecot = p.daemonStatus(ctx, DaemonDovecot,
		p.runner.Available(CommandDovecot), p.dovecotVersion(ctx))
	status.Rspamd = p.daemonStatus(ctx, DaemonRspamd,
		p.runner.Available(CommandRspamadm), p.rspamdVersion(ctx))

	if !p.Available() {
		status.Reason = missingReason(status)
		return status
	}
	status.Available = true

	current, err := p.currentMain(ctx)
	if err != nil {
		status.Warnings = append(status.Warnings,
			"the panel could not read Postfix's configuration: "+err.Error())
		current = map[string]string{}
	}
	status.Hostname = current["myhostname"]
	status.MapType = mapTypeOf(current["virtual_mailbox_domains"])

	status.TLS = p.tlsStatus(current)
	status.Ports = p.portStatus(ctx, current)
	status.QueueLength, status.QueueOldest = p.queueDepth(ctx)
	status.Antivirus = p.antivirusStatus(ctx, virusConfigured(p.paths))
	status.Signing = p.signingKeysOnDisk()
	status.OpenRelay = p.relayCheck(ctx)

	if status.Postfix.Running && status.Hostname != "" &&
		!strings.Contains(status.Hostname, ".") {
		status.Warnings = append(status.Warnings,
			"the mail server calls itself "+status.Hostname+", which is not a fully "+
				"qualified name: most receiving servers refuse mail from a host that "+
				"greets them with one")
	}
	if status.Postfix.Running && !status.Dovecot.Running {
		status.Warnings = append(status.Warnings,
			"mail is being accepted and Dovecot is not running, so nothing is being "+
				"delivered into a mailbox and nobody can read their mail")
	}
	if status.Rspamd.Installed && !status.Rspamd.Running && current["smtpd_milters"] != "" {
		status.Warnings = append(status.Warnings,
			"the filter is not running and Postfix is configured to require it, so mail "+
				"is being deferred rather than delivered")
	}

	return status
}

// daemonStatus reports one piece of the mail server.
func (p *Provider) daemonStatus(ctx context.Context, name string, installed bool, version string) Daemon {
	daemon := Daemon{Installed: installed, Version: version}
	if !installed {
		daemon.Detail = "not installed"
		return daemon
	}
	if p.services == nil {
		daemon.Detail = "this host has no service manager, so the panel cannot tell whether it is running"
		return daemon
	}
	daemon.Running = p.services.Running(ctx, name)
	if !daemon.Running {
		daemon.Detail = "installed and not running"
	}
	return daemon
}

// missingReason says which half of the mail server is absent.
//
// Which half, rather than "not installed", because the two are different jobs:
// a host with Postfix and no Dovecot accepts mail nobody can read, and a host
// with Dovecot and no Postfix serves mailboxes nothing delivers into.
func missingReason(status Status) string {
	switch {
	case !status.Postfix.Installed && !status.Dovecot.Installed:
		return "this host has no mail server installed"
	case !status.Postfix.Installed:
		return "this host has Dovecot but no Postfix, so there are mailboxes and " +
			"nothing to deliver into them"
	default:
		return "this host has Postfix but no Dovecot, so mail can be accepted and " +
			"nobody can read it"
	}
}

// mapTypeOf pulls the lookup table format out of a configured map path.
func mapTypeOf(value string) string {
	if kind, _, found := strings.Cut(value, ":"); found {
		return kind
	}
	return ""
}

// virusConfigured reads back whether virus scanning is switched on.
//
// From the file the panel wrote rather than from the panel's own record,
// because this function is reporting what the host does. A file that says
// "enabled = false" is virus scanning that is off, whatever the database says.
func virusConfigured(paths Paths) bool {
	data, err := os.ReadFile(paths.RspamdLocal(antivirusConf))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "enabled = true")
}

// signingKeysOnDisk lists the domains with a private key on this host.
//
// Read from the key directory rather than from a request, so it answers the
// question a status page needs — what can this host sign — independently of
// what the panel believes it configured.
func (p *Provider) signingKeysOnDisk() []string {
	entries, err := os.ReadDir(p.paths.DKIMDir())
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var domains []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".key") {
			continue
		}
		// "<domain>.<selector>.key" — the selector is the last field before
		// the extension, and everything before it is the domain, which may
		// itself contain dots.
		trimmed := strings.TrimSuffix(name, ".key")
		index := strings.LastIndex(trimmed, ".")
		if index <= 0 {
			continue
		}
		domain := trimmed[:index]
		if !seen[domain] {
			seen[domain] = true
			domains = append(domains, domain)
		}
	}
	return domains
}

// tlsStatus reads the certificate the mail server presents.
//
// The file is parsed rather than trusted, because the interesting failure is a
// path that is configured and points at something that is not a certificate, or
// at one that expired last week. A mail server's certificate expiring is
// quieter than a website's: a browser puts up a full-page warning, while a mail
// client mostly just stops syncing and says nothing an ordinary person can act
// on.
func (p *Provider) tlsStatus(current map[string]string) TLSStatus {
	path := current["smtpd_tls_cert_file"]
	if path == "" {
		return TLSStatus{Detail: "the mail server presents no certificate"}
	}

	status := TLSStatus{Configured: true, CertificatePath: path}
	data, err := os.ReadFile(path)
	if err != nil {
		status.Configured = false
		status.Detail = "the configured certificate cannot be read: " + err.Error()
		return status
	}
	block, _ := pem.Decode(data)
	if block == nil {
		status.Configured = false
		status.Detail = "the configured certificate file is not a certificate"
		return status
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		status.Configured = false
		status.Detail = "the configured certificate could not be parsed: " + err.Error()
		return status
	}

	status.NotAfter = certificate.NotAfter.UTC().Format(time.RFC3339)
	if time.Now().After(certificate.NotAfter) {
		status.Detail = "the mail server's certificate has expired, so mail clients " +
			"are refusing to connect"
	}
	return status
}

// The services a mail server offers, and what each one is for.
var mailPorts = []struct {
	name        string
	port        int
	requiresTLS bool
	// service is the master.cf name, empty for a port Postfix always runs or
	// that belongs to Dovecot.
	service string
}{
	{name: "SMTP", port: 25, service: ""},
	{name: "Submission (STARTTLS)", port: 587, requiresTLS: true, service: "submission/inet"},
	{name: "Submission (implicit TLS)", port: 465, requiresTLS: true, service: "submissions/inet"},
	{name: "IMAP (STARTTLS)", port: 143},
	{name: "IMAP (implicit TLS)", port: 993, requiresTLS: true},
	{name: "POP3 (STARTTLS)", port: 110},
	{name: "POP3 (implicit TLS)", port: 995, requiresTLS: true},
}

// portStatus reports which services are configured and which are listening.
//
// Both, separately, because they differ in exactly the case a page has to be
// able to show: a daemon that failed to start leaves every port configured and
// none of them listening, and a status that reported only the configuration
// would show a mail server that is entirely fine.
func (p *Provider) portStatus(ctx context.Context, current map[string]string) []PortStatus {
	configuredServices := p.masterServices(ctx)

	ports := make([]PortStatus, 0, len(mailPorts))
	for _, definition := range mailPorts {
		entry := PortStatus{
			Name:        definition.name,
			Port:        definition.port,
			RequiresTLS: definition.requiresTLS,
			Configured:  true,
		}
		if definition.service != "" {
			entry.Configured = configuredServices[definition.service]
		}
		entry.Listening = listening(definition.port)
		ports = append(ports, entry)
	}
	return ports
}

// masterServices reads which SMTP services Postfix is configured to run.
func (p *Provider) masterServices(ctx context.Context) map[string]bool {
	found := map[string]bool{}
	result, err := p.runner.Run(ctx, CommandPostconf, "-M")
	if err != nil || !result.Succeeded() {
		return found
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// "submission inet n - n - - smtpd" — the service is named by its
		// first two fields, which is the form postconf -M takes back.
		found[fields[0]+"/"+fields[1]] = true
	}
	return found
}

// listening reports whether anything is bound to a port on this host.
//
// A connection attempt rather than a look at /proc, because "bound" is not the
// question — the question is whether a client can reach it, and a daemon
// listening on the loopback only would answer yes to the first and no to the
// second for everybody who matters. This checks the loopback, which is the
// weaker of the two answers and the one that is true wherever the panel runs;
// what an outside client sees additionally depends on the firewall, which
// Phase 16 reports.
func listening(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// relayCheck asks this host, over the network, whether it will carry a
// stranger's mail.
//
// This is the single most important check in the phase, and it is done by
// *trying* rather than by reading the configuration. Reading the configuration
// tells you what Postfix was told; only a connection tells you what Postfix
// does — and the ways to accidentally build an open relay all look correct in a
// file, because they are correct in a file and wrong in combination.
//
// The probe is a conversation and never a message: EHLO, MAIL FROM, RCPT TO,
// QUIT. It stops before DATA, so nothing is ever sent, and the addresses are in
// reserved domains that cannot exist. What it looks at is the reply to RCPT TO
// for a domain this host has no business accepting.
//
// It connects from this host's own routable address rather than the loopback,
// and that detail is the whole test: mynetworks contains 127.0.0.0/8, so a
// probe from the loopback is permitted by design and would report every
// correctly configured server as an open relay.
func (p *Provider) relayCheck(ctx context.Context) RelayStatus {
	address, err := routableAddress()
	if err != nil {
		return RelayStatus{
			Detail: "the panel could not find a non-loopback address to test from, so " +
				"it cannot tell whether this server relays: " + err.Error(),
		}
	}

	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(address, "25"))
	if err != nil {
		return RelayStatus{
			Detail: "nothing answered on port 25, so there is nothing to relay through",
		}
	}
	defer func() { _ = conn.Close() }()

	deadline := time.Now().Add(15 * time.Second)
	_ = conn.SetDeadline(deadline)
	reader := bufio.NewReader(conn)

	// The greeting.
	if _, err := readSMTPReply(reader); err != nil {
		return RelayStatus{Detail: "the mail server did not greet: " + err.Error()}
	}

	// Reserved names throughout. ".invalid" is guaranteed by RFC 2606 never to
	// resolve, so this probe cannot accidentally name somebody's real domain
	// and cannot cause a delivery attempt anywhere if the answer is wrong.
	steps := []string{
		"EHLO relay-test.invalid",
		"MAIL FROM:<probe@relay-test.invalid>",
		"RCPT TO:<probe@relay-target.invalid>",
	}
	var reply string
	for _, step := range steps {
		if _, err := fmt.Fprintf(conn, "%s\r\n", step); err != nil {
			return RelayStatus{Detail: "the relay check could not complete: " + err.Error()}
		}
		reply, err = readSMTPReply(reader)
		if err != nil {
			return RelayStatus{Detail: "the relay check could not complete: " + err.Error()}
		}
		if strings.HasPrefix(step, "RCPT") {
			break
		}
		if !strings.HasPrefix(reply, "2") {
			// A server that refuses EHLO or MAIL FROM from this address is not
			// relaying, but it is also not answering the question that was
			// asked. Saying which is better than reporting a pass.
			_, _ = fmt.Fprint(conn, "QUIT\r\n")
			return RelayStatus{
				Checked: true,
				Detail: "the server refused the relay probe before it could be asked to " +
					"relay, which is safe: " + firstLine(reply),
			}
		}
	}
	_, _ = fmt.Fprint(conn, "QUIT\r\n")

	if strings.HasPrefix(reply, "2") {
		return RelayStatus{
			Checked: true,
			Open:    true,
			Detail: "this server accepted mail for a domain it does not host, from an " +
				"unauthenticated client. It is an open relay and will be on a blocklist " +
				"within hours: every customer on this host will stop being able to send mail.",
		}
	}
	return RelayStatus{
		Checked: true,
		Detail:  "refused as it should be: " + firstLine(reply),
	}
}

// readSMTPReply reads one reply, following the multi-line continuation form.
//
// A reply is one or more lines whose fourth character is "-" for every line but
// the last. Reading only the first line would leave the rest of an EHLO reply
// in the buffer, and the next command's reply would be read as a continuation
// of it — so the check would be answering about the wrong command.
func readSMTPReply(reader *bufio.Reader) (string, error) {
	var last string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return last, err
		}
		last = strings.TrimRight(line, "\r\n")
		if len(last) < 4 || last[3] != '-' {
			return last, nil
		}
	}
}

// routableAddress finds an address on this host that is not the loopback.
//
// Not for reaching the outside world — for reaching *this* host from an address
// the mail server does not already trust. See relayCheck.
func routableAddress() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("list this host's network interfaces: %w", err)
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addresses {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP.IsLoopback() || ipNet.IP.IsLinkLocalUnicast() {
				continue
			}
			if ipv4 := ipNet.IP.To4(); ipv4 != nil {
				return ipv4.String(), nil
			}
		}
	}
	return "", fmt.Errorf("this host has no non-loopback address")
}
