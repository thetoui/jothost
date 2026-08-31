package ftp

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jothost/panel/shared/validate"
)

// Who is connected, and cutting them off.
//
// proftpd records live sessions in a scoreboard file and ftpwho formats it.
// There is no other interface: the sessions are separate processes and the
// master has no query protocol.
//
// The scoreboard is the reason the panel writes ScoreboardFile at all. Without
// the directive proftpd keeps no scoreboard, and ftpwho then answers "no users
// connected" while transfers are in progress — a session monitor that is
// always empty and never says why.

// Session is one connected client.
type Session struct {
	// PID is the process serving this session, and what a disconnect signals.
	PID int `json:"pid"`
	// User is the virtual account that logged in.
	User string `json:"user"`
	// Elapsed is how long the session has been open, as proftpd formats it.
	Elapsed string `json:"elapsed"`
	// Activity is what the session is doing now — a transfer with its file
	// name, or idle.
	Activity string `json:"activity"`
	// Client is the address the session came from.
	Client string `json:"client"`
	// Protocol is "ftp" or "ftps". It is worth surfacing on its own: it is the
	// difference between a password that crossed the network encrypted and one
	// that did not, and it is the only place an operator can see which.
	Protocol string `json:"protocol"`
	// Location is the directory the session is in, inside its chroot.
	Location string `json:"location"`
}

// Encrypted reports whether this session negotiated TLS.
func (s Session) Encrypted() bool { return strings.EqualFold(s.Protocol, "ftps") }

// Sessions lists what is connected right now.
//
// A stopped daemon is an empty list, not an error: "nothing is connected" is
// the true answer, and the caller already knows from the status whether the
// server is running.
func (p *Provider) Sessions(ctx context.Context) ([]Session, error) {
	if !p.Available() {
		return nil, ErrUnavailable
	}
	result, err := p.runner.Run(ctx, CommandFtpwho, "-v")
	if err != nil {
		return nil, wrap("list the FTP sessions", err)
	}
	if !result.Succeeded() {
		// ftpwho fails when there is no scoreboard to read, which is what a
		// stopped daemon looks like.
		return []Session{}, nil
	}
	return parseSessions(result.Stdout), nil
}

// parseSessions reads ftpwho -v output.
//
//	standalone FTP daemon [23688], up for 0 min
//	23698 demo     [  0m3s] (100%) RETR index.html
//		KB/s: inf
//		client: localhost [127.0.0.1]
//		server: 127.0.0.1:21 (ProFTPD Default Installation)
//		protocol: ftp
//		location: /
//
//	Service class                      -   1 user
//
// The header and the trailing summary are prose and are skipped; a session
// starts at a line beginning with a pid, and its indented lines belong to it.
func parseSessions(output string) []Session {
	sessions := make([]Session, 0, 4)
	// An index rather than a pointer: appending to the slice can move the
	// array, and a pointer into the old one writes somewhere nothing reads.
	current := -1

	for _, raw := range strings.Split(output, "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}

		// A session line first, whatever it is indented by.
		//
		// This order is load-bearing. ftpwho right-aligns the pid in a
		// five-column field, so a session served by a process with a pid under
		// 10000 begins with a *space* — and treating leading whitespace as
		// "this is a detail of the line above" silently dropped every such
		// session. On a freshly booted host that is all of them.
		if session, ok := parseSessionLine(raw); ok {
			sessions = append(sessions, session)
			current = len(sessions) - 1
			continue
		}

		// Otherwise, an indented "key: value" belongs to the session above.
		if raw[0] == ' ' || raw[0] == '\t' {
			if current < 0 {
				continue
			}
			key, value, found := strings.Cut(strings.TrimSpace(raw), ":")
			if !found {
				continue
			}
			value = strings.TrimSpace(value)
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "client":
				sessions[current].Client = value
			case "protocol":
				sessions[current].Protocol = value
			case "location":
				sessions[current].Location = value
			}
			continue
		}
	}
	return sessions
}

// parseSessionLine reads "23698 demo     [  0m3s] (100%) RETR index.html".
func parseSessionLine(line string) (Session, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return Session{}, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 1 {
		// Not a session line: the header starts with a word, and the summary
		// starts with "Service".
		return Session{}, false
	}
	// The user name is one the panel wrote, so a field that is not a valid one
	// means this line was misread — and the name goes on to a page.
	if validate.FTPUsername(fields[1]) != nil {
		return Session{}, false
	}

	session := Session{PID: pid, User: fields[1]}

	// The elapsed time is bracketed, and proftpd pads inside the brackets.
	rest := strings.TrimSpace(strings.Join(fields[2:], " "))
	if open := strings.Index(rest, "["); open >= 0 {
		if close := strings.Index(rest[open:], "]"); close >= 0 {
			session.Elapsed = strings.TrimSpace(rest[open+1 : open+close])
			rest = strings.TrimSpace(rest[open+close+1:])
		}
	}
	// What is left is the activity: "(idle)", or a transfer percentage
	// followed by the command and its file.
	session.Activity = rest
	return session, true
}

// Disconnect ends one session.
//
// The pid is checked against /proc before anything is signalled, and the check
// is the whole point of this function rather than a precaution around it. Pids
// are reused: between the page being rendered and the operator clicking, the
// session can end and the number can belong to something else entirely — and
// this process runs as root, so a signal sent to the wrong pid would be sent
// successfully.
//
// So a disconnect only ever signals a process that is, right now, a proftpd
// session this Agent can see in the scoreboard.
func (p *Provider) Disconnect(ctx context.Context, pid int) error {
	if pid <= 1 {
		// 1 is init, and 0 is "every process in my group".
		return fmt.Errorf("%w: %d", ErrNoSession, pid)
	}

	sessions, err := p.Sessions(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, session := range sessions {
		if session.PID == pid {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: %d", ErrNoSession, pid)
	}
	if err := p.confirmProftpd(pid); err != nil {
		return err
	}

	// SIGTERM: proftpd closes the control connection and logs the disconnect.
	// SIGKILL would leave a scoreboard entry behind for a session that is
	// gone, and the next page load would offer to disconnect it again.
	if err := terminate(pid); err != nil {
		return err
	}
	p.log.Info("ended an FTP session", "pid", pid)
	return nil
}

// confirmProftpd checks that a pid really is the FTP daemon.
//
// The scoreboard can be stale — a session killed by something else leaves its
// entry until the master notices — so agreeing with it is not enough.
func (p *Provider) confirmProftpd(pid int) error {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return fmt.Errorf("%w: %d", ErrNoSession, pid)
	}
	if name := strings.TrimSpace(string(data)); name != "proftpd" {
		// Refused, and loudly: this is a root process being asked to signal
		// something that is not what the caller thinks it is.
		p.log.Warn("refused to end an FTP session: the process is not proftpd",
			"pid", pid, "process", name)
		return fmt.Errorf("%w: %d", ErrNoSession, pid)
	}
	return nil
}
