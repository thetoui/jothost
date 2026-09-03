// Package dns runs the host's authoritative name server.
//
// # Why BIND
//
// BIND 9.18 is what this package drives. The alternative in the task list is
// PowerDNS, and the deciding difference is not features but where the zone
// lives: PowerDNS keeps zones in a SQL database of its own, so adopting it
// would mean a second database beside the panel's, with its own schema,
// migrations and backup story, and no file an operator can read to see what
// their name server is actually serving. BIND keeps zones in files, which is
// also what makes every claim this phase makes checkable — by the panel, by
// `named-checkzone`, and by whoever has to debug it at three in the morning.
//
// The other reason is DNSSEC. `dnssec-policy` (BIND 9.16 and later) makes the
// server itself responsible for generating keys, signing the zone, and rolling
// the keys over on schedule. A panel that did that from outside would be a
// cron job racing a daemon over a set of key files, and getting a key rollover
// wrong takes a domain off the internet for as long as the old signatures are
// cached.
//
// # What the panel owns
//
// Two things, and deliberately not a third:
//
//   - The zone files, under a directory of this package's own making, one file
//     per zone, entirely generated.
//   - One include file of zone statements, jothost.conf, which named.conf
//     includes.
//
// It does not own named.conf. If the host has none — Alpine ships samples and
// no live file — the panel writes one, because a name server with no
// configuration is not a name server. If the host has one already, the panel
// adds a single include line to it and changes nothing else. Global settings in
// somebody else's named.conf are read and *reported*, never rewritten: an
// operator who has set `listen-on` to one address has done so for a reason, and
// a panel that silently widened it would be a panel that opened a name server
// to the internet as a side effect of adding a record.
//
// # The serial nobody should compare
//
// With inline signing, named maintains its own serial on the signed copy of a
// zone, and it diverges from the one in the file the panel wrote — measured,
// not assumed: a file at serial 2026090302 was served as 2026090304 within
// seconds. Anything here that reads a serial back therefore reports it as the
// *served* serial, separately from the panel's own, and nothing treats a
// difference between them as drift.
package dns

import (
	"errors"
	"path/filepath"
)

// Allowlist names for the programs this package runs.
//
// named itself is here only to read a version. Starting and stopping the daemon
// goes through the service manager, so one thing on the host owns its
// lifecycle; rndc is how a running server is told to re-read what the panel
// wrote.
const (
	CommandNamed          = "named"
	CommandNamedCheckconf = "named-checkconf"
	CommandNamedCheckzone = "named-checkzone"
	CommandRndc           = "rndc"
	CommandDNSSECFromKey  = "dnssec-dsfromkey"
)

// Errors this package returns. Each is a different thing for an operator to do,
// which is why they are distinct rather than one "DNS error".
var (
	// ErrUnavailable means no name server is installed.
	ErrUnavailable = errors.New("no DNS server is installed on this host")
	// ErrNotRunning means the daemon is not running, so nothing can be
	// reloaded and nothing is being answered.
	ErrNotRunning = errors.New("the DNS server is not running")
	// ErrInvalidConfig means named refused what the panel produced. It is a
	// bug in the panel, and it is reported as one rather than being applied.
	ErrInvalidConfig = errors.New("the generated DNS configuration was refused by named")
	// ErrInvalidZone means named-checkzone refused a zone file.
	ErrInvalidZone = errors.New("the generated zone file was refused by named-checkzone")
	// ErrUnknownZone means the host is not serving that zone.
	ErrUnknownZone = errors.New("this host does not serve that zone")
	// ErrForeignConfig means named.conf exists, is not the panel's, and could
	// not be given an include line.
	ErrForeignConfig = errors.New("this host's named.conf could not be extended")
	// ErrNoDNSSEC means the running BIND cannot sign zones.
	ErrNoDNSSEC = errors.New("this BIND cannot sign zones")
)

// Default locations. Every one of them is a constant here or configuration on
// the Agent; none is ever taken from a request.
const (
	// DefaultConfigDir is where BIND's configuration lives.
	DefaultConfigDir = "/etc/bind"
	// DefaultStateDir holds the zone files this panel writes, the journals
	// named keeps beside them, and the keys it generates.
	DefaultStateDir = "/var/bind/jothost"

	// mainConfName is BIND's own entry point.
	mainConfName = "named.conf"
	// includeName is the file this panel owns completely.
	includeName = "jothost.conf"
	// keyDirName holds DNSSEC keys. It is under the state directory rather
	// than beside the configuration because named writes to it, and a
	// directory named can write to should not be the one holding the
	// configuration it reads.
	keyDirName = "keys"
	// zoneSuffix is appended to a zone name to make its file name.
	zoneSuffix = ".zone"

	// marker identifies a zone file this panel generated.
	//
	// A semicolon, because that is a zone file's comment character. A "#"
	// here — which is what this had first — is not a comment at all: named
	// reads the line as a resource record and refuses the zone with "unknown
	// RR type 'Managed'", which is a name server that will not start.
	marker = "; Managed by JotHost Panel"
	// confMarker is the same thing in named.conf's syntax, which is C's rather
	// than a zone file's. Its absence in named.conf is what tells the provider
	// it is looking at somebody else's file.
	confMarker = "// Managed by JotHost Panel"
)

// Paths locates everything this package writes.
type Paths struct {
	// ConfigDir is BIND's configuration directory.
	ConfigDir string
	// StateDir holds zone files and keys.
	StateDir string
}

// withDefaults fills in what a caller left empty.
func (p Paths) withDefaults() Paths {
	if p.ConfigDir == "" {
		p.ConfigDir = DefaultConfigDir
	}
	if p.StateDir == "" {
		p.StateDir = DefaultStateDir
	}
	return p
}

// MainConf is BIND's own configuration file.
func (p Paths) MainConf() string {
	return filepath.Join(p.withDefaults().ConfigDir, mainConfName)
}

// Include is the file of zone statements this panel owns.
func (p Paths) Include() string {
	return filepath.Join(p.withDefaults().ConfigDir, includeName)
}

// KeyDir is where named keeps the DNSSEC keys it generates.
func (p Paths) KeyDir() string {
	return filepath.Join(p.withDefaults().StateDir, keyDirName)
}

// ZoneFile returns the file a zone's records are written to.
//
// The name is used as a file name, so it is the caller's job to have validated
// it first — every path into this package goes through validate.Zone, which
// refuses everything that is not a domain name, and a domain name contains no
// separator and cannot be "..".
func (p Paths) ZoneFile(zone string) string {
	return filepath.Join(p.withDefaults().StateDir, zone+zoneSuffix)
}
