// Package tenancy measures what a subscription uses and applies the resource
// limits it was sold.
//
// Two jobs, both privileged, and neither of them takes anything resembling a
// command from the panel.
//
// **Measuring.** A request carries document roots — paths the panel wrote when
// it created each website, resolved here through the same sites.Provisioner
// that created them, so a path outside the allowed root is refused by the code
// that already knows what the allowed root is. Disk is read with `du`, which
// counts blocks rather than apparent size and handles a hard link once; a
// walk in Go would count a backup's hard links twice and report a customer
// using double what they have.
//
// **Limiting.** The panel sends numbers — a percentage, a megabyte count, a
// weight — and this package renders them into a systemd slice. Nothing from a
// request is ever a directive: the unit is a template with typed values
// substituted, the slice name has been through validate.SliceName, and a value
// that could carry a newline could otherwise close one directive and open
// another in a file systemd reads as root.
//
// What this package deliberately does not claim: creating a slice does not put
// anything in it. See docs/PHASE22.md — the state reported back distinguishes
// a host that is enforcing the limits from one that has merely recorded them,
// because a panel that could not tell those apart would show a green tick for
// a cap that caps nothing.
package tenancy

import (
	"errors"
	"log/slog"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/agent/internal/services"
	"github.com/jothost/panel/agent/internal/sites"
)

// Allowlisted command names.
//
// Prefixed, like the deployment tools, because a name is owned by whichever
// phase claimed it first and a second spec of one name stops the Agent
// starting. `du` in particular is exactly the sort of name another feature
// would reach for.
const CommandDu = "tenant-du"

// Errors returned by this package.
var (
	// ErrNoSystemd means this host cannot place processes in a slice, so a
	// limit written here is recorded and not enforced.
	ErrNoSystemd = errors.New("this host does not run systemd, so a resource limit cannot be applied")
	// ErrNoDiskTool means disk usage cannot be measured on this host.
	ErrNoDiskTool = errors.New("du is not available, so disk usage cannot be measured")
	// ErrInvalidPath means a document root was not one this Agent manages.
	ErrInvalidPath = errors.New("that path is not a website this host manages")
)

// Isolation states.
//
// Four rather than a boolean, and 'declared' is the one that earns the type:
// the limits are written and this host is not applying them. A scheme with
// only applied and failed would report success for a file nothing reads.
const (
	StateNone     = "none"
	StateApplied  = "applied"
	StateDeclared = "declared"
	StateFailed   = "failed"
)

// Provider measures and limits subscriptions.
type Provider struct {
	runner   *command.Runner
	sites    *sites.Provisioner
	services *services.Provider
	log      *slog.Logger
	unitDir  string
}

// Options configure a Provider.
type Options struct {
	Runner   *command.Runner
	Sites    *sites.Provisioner
	Services *services.Provider
	Log      *slog.Logger
	// UnitDir is where slice units are written. Empty uses the systemd
	// default; tests point it at a temporary directory.
	UnitDir string
}

// DefaultUnitDir is where systemd reads units an administrator installed.
const DefaultUnitDir = "/etc/systemd/system"

// NewProvider builds a Provider.
func NewProvider(opts Options) *Provider {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	unitDir := opts.UnitDir
	if unitDir == "" {
		unitDir = DefaultUnitDir
	}
	return &Provider{
		runner:   opts.Runner,
		sites:    opts.Sites,
		services: opts.Services,
		log:      log,
		unitDir:  unitDir,
	}
}

// SiteUsage is what one website was measured to be using.
//
// Every field that could not be measured is a pointer, and nil means *not
// measured* rather than zero. A site whose disk could not be read is not a
// site using no disk, and reporting 0 would tell a customer they are within a
// quota nobody checked.
type SiteUsage struct {
	DocumentRoot   string `json:"document_root"`
	DiskBytes      *int64 `json:"disk_bytes"`
	BandwidthBytes *int64 `json:"bandwidth_bytes"`
	// LogTruncated says the access log was longer than this Agent will read in
	// one pass, so the bandwidth figure is a floor rather than a total.
	LogTruncated bool   `json:"log_truncated"`
	Error        string `json:"error,omitempty"`
}

// UsageResult is what a measurement found across a subscription's websites.
type UsageResult struct {
	Sites []SiteUsage `json:"sites"`
	// DiskBytes and BandwidthBytes are the totals, and are nil when nothing
	// could be measured at all — which is a different answer from zero and is
	// stored as a different value.
	DiskBytes      *int64 `json:"disk_bytes"`
	BandwidthBytes *int64 `json:"bandwidth_bytes"`
	// Partial says at least one website could not be measured, so the totals
	// are a floor. A page that showed them without this would be showing a
	// customer a smaller number than the truth and calling it their usage.
	Partial bool `json:"partial"`
	// MeasuredAt is left to the panel: the Agent's clock and the panel's are
	// not guaranteed to agree, and the record belongs to whichever one writes
	// the row.
	Warnings []string `json:"warnings,omitempty"`
}

// Limits are the caps a subscription was sold.
//
// Every one is optional, and nil means "do not cap this dimension" — which is
// the default and is not the same as capping it at zero.
type Limits struct {
	CPUPercent *int `json:"cpu_percent"`
	MemoryMB   *int `json:"memory_mb"`
	IOWeight   *int `json:"io_weight"`
}

// Empty reports whether these limits cap nothing.
func (l Limits) Empty() bool {
	return l.CPUPercent == nil && l.MemoryMB == nil && l.IOWeight == nil
}

// IsolationResult is what happened when limits were applied.
type IsolationResult struct {
	Slice string `json:"slice"`
	// State is one of the four above. 'applied' is the only one that means the
	// kernel is enforcing anything.
	State string `json:"state"`
	// Detail says why, in words an operator can act on.
	Detail   string `json:"detail"`
	UnitPath string `json:"unit_path"`
	// Placed says whether anything actually runs inside this slice. It is
	// false today for every host, and it is reported rather than hidden: a
	// slice with no processes in it is a limit on nothing.
	Placed bool `json:"placed"`
}
