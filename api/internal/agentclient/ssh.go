package agentclient

import (
	"context"

	"github.com/jothost/panel/shared/protocol"
)

// The SSH operations.
//
// Nothing here names a file. A key request carries an account name that the
// Agent matches against the host's own list, and a configuration request carries
// the handful of settings the panel manages — because the file these become
// decides who may log in to the machine.

// SSHConfig is the server's effective configuration.
type SSHConfig struct {
	Available bool   `json:"available"`
	Managed   bool   `json:"managed"`
	Reason    string `json:"reason"`

	Ports                  []int  `json:"ports"`
	RootLogin              string `json:"root_login"`
	PasswordAuthentication bool   `json:"password_authentication"`
	PubkeyAuthentication   bool   `json:"pubkey_authentication"`
	PermitEmptyPasswords   bool   `json:"permit_empty_passwords"`
	X11Forwarding          bool   `json:"x11_forwarding"`
	MaxAuthTries           int    `json:"max_auth_tries"`
	LoginGraceTime         int    `json:"login_grace_time"`

	ConfigPath string `json:"config_path"`
	DropInPath string `json:"drop_in_path"`
	Running    bool   `json:"running"`
}

// SSHAccount is a host account that can hold keys.
type SSHAccount struct {
	Name  string `json:"name"`
	UID   int    `json:"uid"`
	Home  string `json:"home"`
	Shell string `json:"shell"`
	Keys  int    `json:"keys"`
}

// SSHFinding is one security recommendation.
type SSHFinding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Action   string `json:"action"`
}

// SSHStatus is everything the SSH page shows.
type SSHStatus struct {
	Config   SSHConfig    `json:"config"`
	Accounts []SSHAccount `json:"accounts"`
	Findings []SSHFinding `json:"findings"`
}

// SSHKey is one authorised public key.
type SSHKey struct {
	Fingerprint string `json:"fingerprint"`
	Type        string `json:"type"`
	Comment     string `json:"comment"`
	Bits        int    `json:"bits"`
	Account     string `json:"account"`
}

// SSHKeyList is one account's keys.
type SSHKeyList struct {
	Account string   `json:"account"`
	Keys    []SSHKey `json:"keys"`
	Count   int      `json:"count"`
}

// SSHChange is a change to the configuration.
//
// Pointers throughout: "leave it alone" and "set it to false" are different
// requests, and the field a bool would silently reset is the one that decides
// whether anybody can log in.
type SSHChange struct {
	Port                   *int    `json:"port,omitempty"`
	RootLogin              *string `json:"root_login,omitempty"`
	PasswordAuthentication *bool   `json:"password_authentication,omitempty"`
	PubkeyAuthentication   *bool   `json:"pubkey_authentication,omitempty"`
	PermitEmptyPasswords   *bool   `json:"permit_empty_passwords,omitempty"`
	X11Forwarding          *bool   `json:"x11_forwarding,omitempty"`
	MaxAuthTries           *int    `json:"max_auth_tries,omitempty"`
}

// SSHApplyResult is what a change did.
type SSHApplyResult struct {
	Config  SSHConfig `json:"config"`
	Changed []string  `json:"changed"`
	Backup  string    `json:"backup"`
	// Reloaded reports whether the running server picked the change up. False
	// means it is on disk and takes effect at the next restart, which the panel
	// says rather than implying it is live.
	Reloaded bool `json:"reloaded"`
}

// SSHStatusOf reads the configuration, the accounts and the recommendations.
func (c *Client) SSHStatusOf(ctx context.Context, requestID string) (SSHStatus, error) {
	var result SSHStatus
	err := c.call(ctx, requestID, protocol.OperationSSHStatus, map[string]any{}, &result)
	return result, err
}

// SSHConfigure changes the server's configuration.
func (c *Client) SSHConfigure(ctx context.Context, requestID string,
	change SSHChange,
) (SSHApplyResult, error) {
	payload := map[string]any{}
	if change.Port != nil {
		payload["port"] = *change.Port
	}
	if change.RootLogin != nil {
		payload["root_login"] = *change.RootLogin
	}
	if change.PasswordAuthentication != nil {
		payload["password_authentication"] = *change.PasswordAuthentication
	}
	if change.PubkeyAuthentication != nil {
		payload["pubkey_authentication"] = *change.PubkeyAuthentication
	}
	if change.PermitEmptyPasswords != nil {
		payload["permit_empty_passwords"] = *change.PermitEmptyPasswords
	}
	if change.X11Forwarding != nil {
		payload["x11_forwarding"] = *change.X11Forwarding
	}
	if change.MaxAuthTries != nil {
		payload["max_auth_tries"] = *change.MaxAuthTries
	}

	var result SSHApplyResult
	err := c.call(ctx, requestID, protocol.OperationSSHConfigure, payload, &result)
	return result, err
}

// SSHKeys lists one account's authorised keys.
func (c *Client) SSHKeys(ctx context.Context, requestID, account string) (SSHKeyList, error) {
	var result SSHKeyList
	err := c.call(ctx, requestID, protocol.OperationSSHKeyList,
		map[string]any{"account": account}, &result)
	return result, err
}

// SSHAddKey authorises a key for an account.
func (c *Client) SSHAddKey(ctx context.Context, requestID, account, key string) (SSHKey, error) {
	var result SSHKey
	err := c.call(ctx, requestID, protocol.OperationSSHKeyAdd,
		map[string]any{"account": account, "key": key}, &result)
	return result, err
}

// SSHRemoveKey withdraws a key by fingerprint.
func (c *Client) SSHRemoveKey(ctx context.Context, requestID, account,
	fingerprint string,
) (SSHKey, error) {
	var result SSHKey
	err := c.call(ctx, requestID, protocol.OperationSSHKeyRemove,
		map[string]any{"account": account, "fingerprint": fingerprint}, &result)
	return result, err
}
