package databases

import "testing"

func withGrant(username, privilege string) User {
	return User{
		ID:       username + "-id",
		Username: username,
		Grants:   []Grant{{DatabaseID: "db-1", Privilege: privilege}},
	}
}

// TestTheConsoleOpensAsTheAccountWithTheMostAccess.
//
// A database reachable by a read-only account and a full one opens as the full
// one. The other way round produces a session where half the buttons fail, and
// the operator has to work out that the panel signed them in as the wrong
// account.
func TestTheConsoleOpensAsTheAccountWithTheMostAccess(t *testing.T) {
	users := []User{
		withGrant("reader", "read"),
		withGrant("owner", "full"),
		withGrant("writer", "write"),
	}

	chosen, ok := pickConsoleAccount(users, "db-1")
	if !ok {
		t.Fatal("no account was chosen from three that have grants")
	}
	if chosen.Username != "owner" {
		t.Fatalf("opened as %q, want the account with full access", chosen.Username)
	}
}

// TestTheSameDatabaseAlwaysOpensAsTheSameAccount.
//
// Ties break on username. Without that the choice would follow whatever order
// the rows came back in, and the same person clicking twice could be shown two
// different sets of tables.
func TestTheSameDatabaseAlwaysOpensAsTheSameAccount(t *testing.T) {
	first, _ := pickConsoleAccount([]User{
		withGrant("bravo", "full"), withGrant("alpha", "full"),
	}, "db-1")
	second, _ := pickConsoleAccount([]User{
		withGrant("alpha", "full"), withGrant("bravo", "full"),
	}, "db-1")

	if first.Username != second.Username {
		t.Fatalf("the same database opened as %q and then %q",
			first.Username, second.Username)
	}
}

// TestADatabaseWithNoAccountIsRefused rather than opened as something else.
//
// The alternative is a shared administrative account, which would give
// everybody who can open a console full access to every database on the host —
// the opposite of what per-site accounts are for.
func TestADatabaseWithNoAccountIsRefused(t *testing.T) {
	if _, ok := pickConsoleAccount(nil, "db-1"); ok {
		t.Fatal("an account was chosen for a database that has none")
	}

	// Grants on a different database do not count.
	elsewhere := []User{{Username: "other", Grants: []Grant{{DatabaseID: "db-2", Privilege: "full"}}}}
	if _, ok := pickConsoleAccount(elsewhere, "db-1"); ok {
		t.Fatal("an account with a grant on another database was chosen")
	}
}

// TestAnUnknownPrivilegeSortsLast rather than being treated as full access.
func TestAnUnknownPrivilegeSortsLast(t *testing.T) {
	chosen, ok := pickConsoleAccount([]User{
		withGrant("mystery", "something-new"),
		withGrant("reader", "read"),
	}, "db-1")
	if !ok {
		t.Fatal("no account was chosen")
	}
	if chosen.Username != "reader" {
		t.Fatalf("opened as %q; an unrecognised privilege must not outrank a known one",
			chosen.Username)
	}
}
