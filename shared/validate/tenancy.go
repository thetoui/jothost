package validate

import (
	"errors"
	"fmt"
	"strings"
)

// Errors returned by tenancy validation.
var (
	// ErrInvalidTier covers an account tier outside the closed set.
	ErrInvalidTier = errors.New("invalid account tier")
	// ErrInvalidPlanName covers a plan or subscription name the panel will
	// not store.
	ErrInvalidPlanName = errors.New("invalid name")
	// ErrInvalidLimit covers a quota limit that is not a limit.
	ErrInvalidLimit = errors.New("invalid limit")
	// ErrInvalidEnforcement covers an overuse policy outside the closed set.
	ErrInvalidEnforcement = errors.New("invalid enforcement policy")
	// ErrInvalidDimension covers a quota dimension the panel does not count.
	ErrInvalidDimension = errors.New("invalid quota dimension")
	// ErrInvalidSlice covers a systemd slice name that is not one.
	ErrInvalidSlice = errors.New("invalid slice name")
)

// Account tiers. A closed set of three, and the order below is the order of
// authority: each tier may only create accounts strictly below itself, which
// is what makes a cycle in the hierarchy impossible rather than merely
// unlikely.
const (
	TierAdmin    = "admin"
	TierReseller = "reseller"
	TierCustomer = "customer"
)

// Plan kinds.
const (
	PlanKindPlan  = "plan"
	PlanKindAddon = "addon"
)

// Overuse policies.
const (
	EnforcementHard = "hard"
	EnforcementSoft = "soft"
)

// Quota dimensions.
//
// The first six are *counted*: the panel knows the exact number because it
// wrote every row, so a limit on one can be enforced at the moment somebody
// asks for one more. The last two are *measured*: they are facts about a disk
// and a log file that the panel learns after the event, so a limit on one can
// only ever be reported.
//
// That difference is not cosmetic and it is why they are named apart here —
// see docs/PHASE22.md on what "hard limit" can and cannot mean for each.
const (
	DimensionWebsites   = "websites"
	DimensionDatabases  = "databases"
	DimensionMailboxes  = "mailboxes"
	DimensionFTPUsers   = "ftp_users"
	DimensionCronJobs   = "cron_jobs"
	DimensionSubdomains = "subdomains"
	DimensionDisk       = "disk"
	DimensionBandwidth  = "bandwidth"
)

// Bounds.
const (
	// MaxPlanNameLength bounds a plan name.
	MaxPlanNameLength = 120
	// MaxSubscriptionNameLength bounds a subscription name.
	MaxSubscriptionNameLength = 255
	// MaxPersonNameLength bounds a contact name or company.
	MaxPersonNameLength = 255
	// MaxReasonLength bounds a suspension reason or an impersonation reason.
	MaxReasonLength = 500
	// MaxAddonQuantity bounds how many of one add-on a subscription may hold.
	// A thousand of anything is a typo, and the schema agrees.
	MaxAddonQuantity = 1000
	// MaxLimitValue bounds any countable limit. Ten million websites is not a
	// plan, and a number this large in an integer column is somebody pasting.
	MaxLimitValue = 10_000_000
	// MinMemoryMB is the smallest memory cap worth applying: below roughly
	// this, a slice kills every process that starts in it, which looks to a
	// customer exactly like a broken server.
	MinMemoryMB = 16
	// MaxCPUPercent allows more than one core, because CPUQuota=200% is a
	// meaningful and ordinary setting on a multi-core host.
	MaxCPUPercent = 10000
	// MinIOWeight and MaxIOWeight are systemd's own range for IOWeight.
	MinIOWeight = 1
	MaxIOWeight = 10000
)

// AccountTier checks an account tier.
func AccountTier(tier string) error {
	switch tier {
	case TierAdmin, TierReseller, TierCustomer:
		return nil
	default:
		return fmt.Errorf("%w: %q is not admin, reseller, or customer", ErrInvalidTier, tier)
	}
}

// TierRank returns a tier's position in the hierarchy, lower being higher
// authority. An unknown tier ranks below everything so it can create nothing.
func TierRank(tier string) int {
	switch tier {
	case TierAdmin:
		return 0
	case TierReseller:
		return 1
	case TierCustomer:
		return 2
	default:
		return 99
	}
}

// TierMayOwn reports whether an account of tier parent may be the parent of an
// account of tier child.
//
// Strictly above, never equal. A reseller who could create another reseller
// could create one whose parent is themselves' own parent, and — more to the
// point — a scheme allowing equal tiers has to detect cycles at write time,
// while this one cannot form them: every edge goes from a lower rank number to
// a higher one, so following parents strictly increases and must terminate.
func TierMayOwn(parent, child string) bool {
	if AccountTier(parent) != nil || AccountTier(child) != nil {
		return false
	}
	return TierRank(parent) < TierRank(child)
}

// PlanKind checks whether a plan is a plan or an add-on.
func PlanKind(kind string) error {
	switch kind {
	case PlanKindPlan, PlanKindAddon:
		return nil
	default:
		return fmt.Errorf("%w: %q is not a plan or an add-on", ErrInvalidDimension, kind)
	}
}

// Enforcement checks an overuse policy.
func Enforcement(policy string) error {
	switch policy {
	case EnforcementHard, EnforcementSoft:
		return nil
	default:
		return fmt.Errorf("%w: %q is not hard or soft", ErrInvalidEnforcement, policy)
	}
}

// QuotaDimension checks a quota dimension name.
func QuotaDimension(dimension string) error {
	switch dimension {
	case DimensionWebsites, DimensionDatabases, DimensionMailboxes,
		DimensionFTPUsers, DimensionCronJobs, DimensionSubdomains,
		DimensionDisk, DimensionBandwidth:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidDimension, dimension)
	}
}

// PlanName checks a plan name.
func PlanName(name string) error {
	return boundedName(name, MaxPlanNameLength, "a plan needs a name")
}

// SubscriptionName checks a subscription name.
func SubscriptionName(name string) error {
	return boundedName(name, MaxSubscriptionNameLength, "a subscription needs a name")
}

// PersonName checks an optional contact name or company.
func PersonName(name string) error {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	return boundedName(name, MaxPersonNameLength, "")
}

// boundedName is the shared check: present, short enough, and free of control
// characters. Names reach a page, a log line and a systemd unit comment, and a
// newline in one of those is how a value stops being a value.
func boundedName(name string, max int, missing string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("%w: %s", ErrInvalidPlanName, missing)
	}
	if len(trimmed) > max {
		return fmt.Errorf("%w: at most %d characters", ErrInvalidPlanName, max)
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: control characters are not allowed", ErrInvalidPlanName)
		}
	}
	return nil
}

// Reason checks an optional free-text reason.
func Reason(reason string) error {
	if len(reason) > MaxReasonLength {
		return fmt.Errorf("%w: a reason may be at most %d characters",
			ErrInvalidPlanName, MaxReasonLength)
	}
	for _, r := range reason {
		if r < 0x20 && r != '\n' && r != '\t' {
			return fmt.Errorf("%w: control characters are not allowed", ErrInvalidPlanName)
		}
	}
	return nil
}

// QuotaLimit checks one limit.
//
// A nil pointer is "unlimited" and is always valid — that is how this panel
// spells the absence of a limit, and it is deliberately a different value from
// zero, which means none at all.
func QuotaLimit(dimension string, value *int) error {
	if err := QuotaDimension(dimension); err != nil {
		return err
	}
	if value == nil {
		return nil
	}
	if *value < 0 {
		return fmt.Errorf("%w: %s cannot be negative — leave it unset for no limit",
			ErrInvalidLimit, dimension)
	}
	if *value > MaxLimitValue {
		return fmt.Errorf("%w: %s is implausibly large", ErrInvalidLimit, dimension)
	}
	return nil
}

// AddonQuantity checks how many of an add-on a subscription holds.
func AddonQuantity(quantity int) error {
	if quantity < 1 || quantity > MaxAddonQuantity {
		return fmt.Errorf("%w: a quantity must be between 1 and %d",
			ErrInvalidLimit, MaxAddonQuantity)
	}
	return nil
}

// Isolation checks the three resource-isolation settings.
//
// Each is optional, and unset means "do not cap this", which is the default. A
// panel that quietly applied a CPU cap nobody chose would be a panel whose
// sites are slow for a reason nobody can find.
func Isolation(cpuPercent, memoryMB, ioWeight *int) error {
	if cpuPercent != nil && (*cpuPercent < 1 || *cpuPercent > MaxCPUPercent) {
		return fmt.Errorf("%w: a CPU cap is a percentage between 1 and %d — "+
			"above 100 means more than one core", ErrInvalidLimit, MaxCPUPercent)
	}
	if memoryMB != nil && *memoryMB < MinMemoryMB {
		return fmt.Errorf("%w: a memory cap below %d MB kills everything that starts in it",
			ErrInvalidLimit, MinMemoryMB)
	}
	if memoryMB != nil && *memoryMB > MaxLimitValue {
		return fmt.Errorf("%w: that memory cap is implausibly large", ErrInvalidLimit)
	}
	if ioWeight != nil && (*ioWeight < MinIOWeight || *ioWeight > MaxIOWeight) {
		return fmt.Errorf("%w: an IO weight is between %d and %d",
			ErrInvalidLimit, MinIOWeight, MaxIOWeight)
	}
	return nil
}

// SliceName checks a systemd slice name.
//
// The name becomes a filename under /etc/systemd/system and an argument to
// systemctl, so it is an allowlist: lowercase letters, digits, dashes, and the
// required .slice suffix. systemd itself reads a dash as a hierarchy separator
// — "a-b.slice" is a child of "a.slice" — so the shape is not cosmetic, and a
// name that could contain a slash or a leading hyphen would be a path or an
// option rather than a slice.
func SliceName(name string) error {
	const suffix = ".slice"
	if !strings.HasSuffix(name, suffix) {
		return fmt.Errorf("%w: a slice name ends in %s", ErrInvalidSlice, suffix)
	}
	stem := strings.TrimSuffix(name, suffix)
	if stem == "" || len(name) > 120 {
		return fmt.Errorf("%w: %q", ErrInvalidSlice, name)
	}
	if stem[0] == '-' || stem[len(stem)-1] == '-' {
		return fmt.Errorf("%w: a slice name cannot begin or end with a dash", ErrInvalidSlice)
	}
	for i := 0; i < len(stem); i++ {
		c := stem[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
		default:
			return fmt.Errorf("%w: %q may contain only lowercase letters, digits and dashes",
				ErrInvalidSlice, name)
		}
	}
	return nil
}
