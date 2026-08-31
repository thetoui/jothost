package agentclient

import (
	"context"

	"github.com/jothost/panel/shared/protocol"
)

// The FTP operations.
//
// A reconcile carries the *complete* set of accounts the panel has recorded, so
// the Agent can make the host match rather than merge. Anything on the host and
// not in the set is removed, which is what makes a deleted website's accounts
// actually go away.
//
// Passwords travel in one direction only, and only when one is being set: the
// panel writes a password to the host and does not keep it.

// FTPAccount is one FTP account as the host has it.
type FTPAccount struct {
	Name string `json:"name"`
	UID  int    `json:"uid"`
	GID  int    `json:"gid"`
	Home string `json:"home"`
	// Locked reports an account proftpd will not let in.
	Locked bool `json:"locked"`
	// QuotaMB is the limit in force, zero for none.
	QuotaMB int `json:"quota_mb"`
	// UsedMB is what proftpd counted this account uploading. It is not a
	// directory size: files removed through the file manager were never seen
	// by proftpd and are not subtracted.
	UsedMB float64 `json:"used_mb"`
}

// FTPSession is one connected client.
type FTPSession struct {
	PID      int    `json:"pid"`
	User     string `json:"user"`
	Elapsed  string `json:"elapsed"`
	Activity string `json:"activity"`
	Client   string `json:"client"`
	// Protocol is "ftp" or "ftps": the difference between a password that
	// crossed the network encrypted and one that did not.
	Protocol string `json:"protocol"`
	Location string `json:"location"`
}

// FTPConflict is a setting another configuration file also sets.
type FTPConflict struct {
	Directive string `json:"directive"`
	File      string `json:"file"`
	PanelWins bool   `json:"panel_wins"`
}

// FTPStatus is what the host reports about its FTP server.
type FTPStatus struct {
	Available  bool   `json:"available"`
	Running    bool   `json:"running"`
	CanInstall bool   `json:"can_install"`
	Version    string `json:"version"`
	Reason     string `json:"reason"`
	// SupportsTLS and SupportsQuota report what this build can do, so the page
	// can say why a feature is not on offer.
	SupportsTLS   bool          `json:"supports_tls"`
	SupportsQuota bool          `json:"supports_quota"`
	Accounts      []FTPAccount  `json:"accounts"`
	Sessions      []FTPSession  `json:"sessions"`
	ConfigPath    string        `json:"config_path"`
	Conflicts     []FTPConflict `json:"conflicts"`
	// FirewallOpen reports whether the ports FTP needs are actually admitted.
	// A blocked passive range is a server that accepts the login and then hangs
	// on the first listing.
	FirewallOpen   bool   `json:"firewall_open"`
	FirewallReason string `json:"firewall_reason"`
}

// FTPSettings are the server-wide options.
type FTPSettings struct {
	PassiveFrom       int    `json:"passive_from"`
	PassiveTo         int    `json:"passive_to"`
	TLSCertificate    string `json:"tls_certificate"`
	TLSKey            string `json:"tls_key"`
	RequireTLS        bool   `json:"require_tls"`
	MasqueradeAddress string `json:"masquerade_address"`
	MaxClients        int    `json:"max_clients"`
}

// FTPUser is one account in a reconcile request.
type FTPUser struct {
	Name string `json:"name"`
	// SystemUser is the website's own account. The Agent resolves it to a uid
	// and gid from the host's own passwd database.
	SystemUser string `json:"system_user"`
	Home       string `json:"home"`
	ReadOnly   bool   `json:"read_only"`
	QuotaMB    int    `json:"quota_mb"`
	Suspended  bool   `json:"suspended"`
}

// FTPDesired is the state the host should be put into.
type FTPDesired struct {
	Settings FTPSettings `json:"settings"`
	Users    []FTPUser   `json:"users"`
	// Passwords are the new ones, by account name. Only accounts being created
	// or reset appear here.
	Passwords map[string]string `json:"passwords,omitempty"`
}

// FTPReconcileResult reports what changed.
type FTPReconcileResult struct {
	Path      string        `json:"path"`
	Backup    string        `json:"backup"`
	Reloaded  bool          `json:"reloaded"`
	Conflicts []FTPConflict `json:"conflicts"`
	Created   []string      `json:"created"`
	Updated   []string      `json:"updated"`
	Removed   []string      `json:"removed"`
	// NeedPassword names accounts the panel has recorded that the host does not
	// have and cannot recreate, because nothing holds their password.
	NeedPassword []string `json:"need_password"`
}

// FTPSessionList is who is connected.
type FTPSessionList struct {
	Sessions []FTPSession `json:"sessions"`
	Count    int          `json:"count"`
}

// FTPStatusOf reports the server, its accounts and its sessions.
//
// The passive range goes with the request so the Agent's firewall check knows
// which ports to look for — it is the panel's setting, and the Agent has no
// record of it.
func (c *Client) FTPStatusOf(ctx context.Context, requestID string,
	passiveFrom, passiveTo int,
) (FTPStatus, error) {
	var result FTPStatus
	err := c.call(ctx, requestID, protocol.OperationFTPStatus, map[string]any{
		"passive_from": passiveFrom,
		"passive_to":   passiveTo,
	}, &result)
	return result, err
}

// FTPInstall puts an FTP server on the host.
func (c *Client) FTPInstall(ctx context.Context, requestID string) (FTPStatus, error) {
	var result FTPStatus
	err := c.call(ctx, requestID, protocol.OperationFTPInstall, map[string]any{}, &result)
	return result, err
}

// FTPReconcile makes the host match what the panel has recorded.
func (c *Client) FTPReconcile(ctx context.Context, requestID string,
	desired FTPDesired,
) (FTPReconcileResult, error) {
	payload := map[string]any{
		"settings": desired.Settings,
		"users":    desired.Users,
	}
	if len(desired.Passwords) > 0 {
		payload["passwords"] = desired.Passwords
	}

	var result FTPReconcileResult
	err := c.call(ctx, requestID, protocol.OperationFTPReconcile, payload, &result)
	return result, err
}

// FTPSessions reports who is connected right now.
func (c *Client) FTPSessions(ctx context.Context, requestID string) (FTPSessionList, error) {
	var result FTPSessionList
	err := c.call(ctx, requestID, protocol.OperationFTPSessions, map[string]any{}, &result)
	return result, err
}

// FTPDisconnect ends one session.
func (c *Client) FTPDisconnect(ctx context.Context, requestID string, pid int) error {
	var result struct {
		Disconnected bool `json:"disconnected"`
	}
	return c.call(ctx, requestID, protocol.OperationFTPDisconnect,
		map[string]any{"pid": pid}, &result)
}
