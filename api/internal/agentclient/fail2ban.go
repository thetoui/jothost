package agentclient

import (
	"context"

	"github.com/jothost/panel/shared/protocol"
)

// The intrusion-prevention operations.
//
// A jail is named from the Agent's catalogue and an address is parsed before it
// becomes an argument. Nothing here carries a filter, a log path or an action:
// those decide who gets banned.

// Fail2BanJail is one of fail2ban's jails as the host has it.
type Fail2BanJail struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Summary string `json:"summary"`
	// Managed is false for a jail somebody configured by hand. It is listed
	// because it is banning people, and left alone.
	Managed bool `json:"managed"`
	Enabled bool `json:"enabled"`

	MaxRetry    int `json:"max_retry"`
	FindTime    int `json:"find_time"`
	BanTime     int `json:"ban_time"`
	Currently   int `json:"currently_banned"`
	Total       int `json:"total_banned"`
	Failed      int `json:"currently_failed"`
	TotalFailed int `json:"total_failed"`

	LogPaths []string `json:"log_paths"`
	Banned   []string `json:"banned"`
	// Available is false where this host has none of the logs the jail watches.
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

// Fail2BanStatus is everything the page shows.
type Fail2BanStatus struct {
	Available  bool           `json:"available"`
	Running    bool           `json:"running"`
	CanInstall bool           `json:"can_install"`
	Version    string         `json:"version"`
	Reason     string         `json:"reason"`
	Jails      []Fail2BanJail `json:"jails"`
	Ignored    []string       `json:"ignored"`
	DropInPath string         `json:"drop_in_path"`
	Banned     int            `json:"banned"`
}

// Fail2BanBanned is one currently banned address.
type Fail2BanBanned struct {
	Address string `json:"address"`
	Jail    string `json:"jail"`
}

// Fail2BanBannedList is what the host is blocking.
type Fail2BanBannedList struct {
	Banned []Fail2BanBanned `json:"banned"`
	Count  int              `json:"count"`
}

// Fail2BanChange is a change to one jail. Nil fields are left alone.
type Fail2BanChange struct {
	Jail     string
	Enabled  *bool
	MaxRetry *int
	FindTime *int
	BanTime  *int
}

// Fail2BanApplyResult is what a change did.
type Fail2BanApplyResult struct {
	Jail    string `json:"jail"`
	Enabled bool   `json:"enabled"`
	Policy  struct {
		MaxRetry int `json:"max_retry"`
		FindTime int `json:"find_time"`
		BanTime  int `json:"ban_time"`
	} `json:"policy"`
	Backup   string `json:"backup"`
	Reloaded bool   `json:"reloaded"`
}

// Fail2BanStatusOf reports the jails, the bans and the policy.
func (c *Client) Fail2BanStatusOf(ctx context.Context, requestID string) (Fail2BanStatus, error) {
	var result Fail2BanStatus
	err := c.call(ctx, requestID, protocol.OperationFail2banStatus, map[string]any{}, &result)
	return result, err
}

// Fail2BanInstall puts fail2ban on the host.
func (c *Client) Fail2BanInstall(ctx context.Context, requestID string) (Fail2BanStatus, error) {
	var result Fail2BanStatus
	err := c.call(ctx, requestID, protocol.OperationFail2banInstall, map[string]any{}, &result)
	return result, err
}

// Fail2BanConfigure changes one jail.
func (c *Client) Fail2BanConfigure(ctx context.Context, requestID string,
	change Fail2BanChange,
) (Fail2BanApplyResult, error) {
	payload := map[string]any{"jail": change.Jail}
	if change.Enabled != nil {
		payload["enabled"] = *change.Enabled
	}
	if change.MaxRetry != nil {
		payload["max_retry"] = *change.MaxRetry
	}
	if change.FindTime != nil {
		payload["find_time"] = *change.FindTime
	}
	if change.BanTime != nil {
		payload["ban_time"] = *change.BanTime
	}

	var result Fail2BanApplyResult
	err := c.call(ctx, requestID, protocol.OperationFail2banConfigure, payload, &result)
	return result, err
}

// Fail2BanSetIgnored changes the addresses no jail may ban.
func (c *Client) Fail2BanSetIgnored(ctx context.Context, requestID string,
	addresses []string,
) ([]string, error) {
	if addresses == nil {
		addresses = []string{}
	}
	var result struct {
		Ignored []string `json:"ignored"`
	}
	err := c.call(ctx, requestID, protocol.OperationFail2banIgnore,
		map[string]any{"ignored": addresses}, &result)
	return result.Ignored, err
}

// Fail2BanBannedAddresses lists what the host is blocking.
func (c *Client) Fail2BanBannedAddresses(ctx context.Context, requestID string) (Fail2BanBannedList, error) {
	var result Fail2BanBannedList
	err := c.call(ctx, requestID, protocol.OperationFail2banBanned, map[string]any{}, &result)
	return result, err
}

// Fail2BanUnban releases an address.
func (c *Client) Fail2BanUnban(ctx context.Context, requestID, jail, address string) error {
	var result struct {
		Banned bool `json:"banned"`
	}
	return c.call(ctx, requestID, protocol.OperationFail2banUnban,
		map[string]any{"jail": jail, "address": address}, &result)
}

// Fail2BanBan bans an address by hand.
func (c *Client) Fail2BanBan(ctx context.Context, requestID, jail, address string) error {
	var result struct {
		Banned bool `json:"banned"`
	}
	return c.call(ctx, requestID, protocol.OperationFail2banBan,
		map[string]any{"jail": jail, "address": address}, &result)
}
