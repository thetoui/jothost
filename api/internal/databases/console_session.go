package databases

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ConsoleMount is where the panel's own nginx proxies phpMyAdmin. It has to
// match the location block in scripts/jothost-installer.sh, the one in
// docker/nginx/dev.conf, and PmaAbsoluteUri in agent/internal/pma/config.go.
const ConsoleMount = "/phpmyadmin/"

// Errors returned when a console session cannot be opened.
var (
	// ErrNoConsoleAccount means no database account the panel holds a password
	// for can reach this database.
	ErrNoConsoleAccount = errors.New("no account this panel can sign in as")
	// ErrConsoleNotServed means phpMyAdmin is not published on this host.
	ErrConsoleNotServed = errors.New("phpMyAdmin is not served on this host")
)

// ConsoleSession is everything a browser needs to open phpMyAdmin on one
// database, signed in.
//
// The password is in the response, and that is a deliberate decision rather
// than an oversight. phpMyAdmin authenticates with a database account; the
// panel already stores these passwords — it has to, because MySQL and
// PostgreSQL keep only a hash and somebody has to be able to put the password
// in a configuration file — and it already has an endpoint that reveals them to
// exactly this permission. So this discloses nothing to this caller that they
// could not already ask for directly.
//
// What it must not become is a credential in a URL. The browser posts these as
// a form body; a GET with the password in the query string would put it in
// phpMyAdmin's access log, in the Referer of everything it links to, and in
// browser history.
type ConsoleSession struct {
	// URL is where the browser signs in: the path the panel proxies
	// phpMyAdmin at, on the panel's own origin — not the hostname the Agent
	// published it under.
	//
	// That is forced, not preferred. phpMyAdmin's login is a POST carrying a
	// CSRF token bound to the session cookie set on the page the form came
	// from, so a caller has to read that page before it can sign anybody in,
	// and only same-origin JavaScript may read it. Posting blind to
	// phpMyAdmin's own hostname returns the login page every time; this was
	// measured against a running phpMyAdmin rather than assumed.
	URL string `json:"url"`
	// Database is the one to open once signed in.
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"`
	// Host is the account's host part, for an operator reading the panel who
	// wonders which of two same-named accounts this is.
	Host string `json:"host,omitempty"`
}

// ConsoleSessionFor picks an account for a database and returns its credentials.
//
// Which account is not arbitrary. A database may be reachable by several, and
// the one chosen is the one with the most access to it: opening a console as an
// account with read-only rights, when a full one exists, produces a session
// where half the buttons fail for a reason the operator has to work out.
//
// A shared administrative account is never used, however convenient. It would
// give everybody who can open a console in this panel full access to every
// database on the host, which is the opposite of what per-site accounts are
// for.
func (s *Service) ConsoleSessionFor(ctx context.Context, databaseID string, actor Actor,
	requestID string,
) (ConsoleSession, error) {
	var session ConsoleSession

	database, err := s.repo.Get(ctx, databaseID)
	if err != nil {
		return session, err
	}

	status, err := s.ConsoleStatus(ctx, requestID)
	if err != nil {
		return session, err
	}
	if !status.Served || status.URL == "" {
		return session, ErrConsoleNotServed
	}

	// Already filtered to the accounts with a grant on this database, each
	// carrying the privilege it was granted.
	users, err := s.repo.ListUsers(ctx, database.ID)
	if err != nil {
		return session, err
	}

	chosen, ok := pickConsoleAccount(users, database.ID)
	if !ok {
		return session, fmt.Errorf("%w: no account in the panel has access to %s",
			ErrNoConsoleAccount, database.Name)
	}

	password, err := s.repo.RevealPassword(ctx, chosen.ID)
	if err != nil {
		return session, err
	}

	// Recorded as its own action rather than as a password reveal. Both hand
	// the same secret to the same person, but "opened a console on the orders
	// database" and "read the password for web_shop" answer different
	// questions afterwards, and an audit trail that could not tell them apart
	// would be the poorer for it.
	s.record(ctx, actor, ActionConsoleSession, database.ID, map[string]any{
		"database": database.Name,
		"username": chosen.Username,
		"engine":   database.Engine,
	})

	return ConsoleSession{
		URL:      ConsoleMount,
		Database: database.Name,
		Username: chosen.Username,
		Password: password,
		Host:     chosen.Host,
	}, nil
}

// privilegeRank orders grants by how much they allow.
//
// Named rather than compared as strings so the ordering is visible: a later
// privilege added to the panel has to be given a rank here, which is a change
// somebody has to make deliberately rather than one that silently sorts last.
func privilegeRank(privilege string) int {
	switch strings.ToLower(privilege) {
	case "full", "all":
		return 3
	case "write", "readwrite", "read_write":
		return 2
	case "read", "readonly", "read_only":
		return 1
	default:
		return 0
	}
}

// pickConsoleAccount chooses the account with the most access to one database.
//
// Ties break on username so the same database opens as the same account every
// time. A console that signed in as a different account depending on map order
// would show different tables to the same person on two consecutive clicks.
func pickConsoleAccount(users []User, databaseID string) (User, bool) {
	type candidate struct {
		user User
		rank int
	}

	var candidates []candidate
	for _, user := range users {
		for _, grant := range user.Grants {
			if grant.DatabaseID != databaseID {
				continue
			}
			candidates = append(candidates, candidate{user: user, rank: privilegeRank(grant.Privilege)})
			break
		}
	}
	if len(candidates) == 0 {
		return User{}, false
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].rank != candidates[j].rank {
			return candidates[i].rank > candidates[j].rank
		}
		return candidates[i].user.Username < candidates[j].user.Username
	})
	return candidates[0].user, true
}
