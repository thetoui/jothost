package fail2ban

import (
	"fmt"

	"github.com/jothost/panel/shared/validate"
)

// The jails the panel offers.
//
// A catalogue rather than a text box, for the reason every other phase has one:
// a jail is a filter, a log path and an action, and the panel accepting those
// from a request would be accepting a regular expression that decides who gets
// locked out of the machine. What a request names is a key from this list.
//
// What the panel writes for each is *policy only* — enabled, maxretry, findtime,
// bantime, ignoreip. It does not write `filter`, and it writes `logpath` only
// where the distribution has not. That boundary is deliberate: Alpine's sshd
// jail uses a filter called `alpine-sshd` because BusyBox's syslog writes a
// prefix the standard filter does not expect, and a panel that overwrote that
// with `filter = sshd` would produce a jail that matches nothing and bans
// nobody, while reporting itself enabled.

// Definition is one jail the panel knows how to configure.
type Definition struct {
	// Name is fail2ban's own name for the jail, and the section header written.
	Name string
	// Label and Summary are what an operator reads.
	Label   string
	Summary string
	// Protects names the thing being defended, for the page's grouping.
	Protects string
	// LogPaths are the candidates this jail watches, in order. The first that
	// exists is used; a jail whose log is absent is offered as unavailable
	// rather than enabled into a daemon that would refuse to start it.
	LogPaths []string
	// SuppliesLogPath is false where the distribution already configures one —
	// the panel then leaves it alone, because the distribution knows which log
	// its own filter was written against.
	SuppliesLogPath bool
	// Defaults are the policy a jail gets when it is first switched on.
	Defaults Policy
}

// Policy is what the panel writes for a jail.
type Policy struct {
	MaxRetry int `json:"max_retry"`
	FindTime int `json:"find_time"`
	BanTime  int `json:"ban_time"`
}

// Validate checks a policy before it becomes a configuration file.
func (p Policy) Validate() error {
	if err := validate.Retries(p.MaxRetry); err != nil {
		return err
	}
	if err := validate.FindSeconds(p.FindTime); err != nil {
		return err
	}
	if err := validate.BanSeconds(p.BanTime); err != nil {
		return err
	}
	// A window longer than the ban is a window that has already reopened by the
	// time the ban lifts, which is not wrong so much as pointless — but a ban
	// shorter than the window means an attacker's counter never resets, which
	// is a permanent ban nobody chose.
	if p.FindTime > p.BanTime {
		return fmt.Errorf(
			"%w: the ban is shorter than the window failures are counted in, so a "+
				"banned address would be banned again the moment it is released",
			validate.ErrInvalidDuration)
	}
	return nil
}

// Defaults are what a jail gets when nothing else is said.
//
// Five failures in ten minutes, banned for an hour. Those are fail2ban's own
// defaults for the first two and longer than its ten minutes for the third:
// ten minutes is enough to slow a person down and not enough to inconvenience a
// script, and an hour is the shortest ban that costs an attacker more than it
// costs a mistyped password.
var Defaults = Policy{MaxRetry: 5, FindTime: 600, BanTime: 3600}

// Catalogue is the set of jails the panel offers on any host.
func Catalogue() []Definition {
	return []Definition{
		{
			Name:     "sshd",
			Label:    "SSH",
			Summary:  "Bans hosts that fail to authenticate over SSH.",
			Protects: "SSH",
			// Every distribution logs authentication somewhere different, and
			// the one that exists is the one the filter was written against.
			LogPaths: []string{
				"/var/log/auth.log",
				"/var/log/secure",
				"/var/log/messages",
			},
			Defaults: Defaults,
		},
		{
			Name:     "nginx-http-auth",
			Label:    "nginx password prompts",
			Summary:  "Bans hosts that fail HTTP authentication on a protected site.",
			Protects: "Web",
			LogPaths: []string{"/var/log/nginx/error.log"},
			// nginx's jails are not configured by any distribution the panel
			// targets, so the log path is the panel's to supply.
			SuppliesLogPath: true,
			Defaults:        Defaults,
		},
		{
			Name:            "nginx-botsearch",
			Label:           "nginx probes",
			Summary:         "Bans hosts scanning for administration pages and known holes.",
			Protects:        "Web",
			LogPaths:        []string{"/var/log/nginx/error.log"},
			SuppliesLogPath: true,
			// A scanner makes many more requests than a person mistypes a URL,
			// so the threshold is higher and the ban longer: what this catches
			// is never a customer.
			Defaults: Policy{MaxRetry: 10, FindTime: 600, BanTime: 6 * 3600},
		},
	}
}

// Lookup finds one definition by name.
func Lookup(name string) (Definition, error) {
	if err := validate.JailName(name); err != nil {
		return Definition{}, err
	}
	for _, definition := range Catalogue() {
		if definition.Name == name {
			return definition, nil
		}
	}
	return Definition{}, fmt.Errorf("%w: %q", ErrUnknownJail, name)
}

// logPathFor returns the first candidate this host has.
func (p *Provider) logPathFor(definition Definition) string {
	for _, candidate := range definition.LogPaths {
		if p.logs.LogExists(candidate) {
			return candidate
		}
	}
	return ""
}
