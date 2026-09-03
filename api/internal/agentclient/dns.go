package agentclient

import (
	"context"

	"github.com/jothost/panel/shared/protocol"
)

// The DNS operations.
//
// A reconcile carries the *complete* set of zones the panel has recorded, so
// the Agent can make the host match rather than merge. A zone the panel has
// forgotten is a zone the host stops serving, which is what makes a delete take
// effect on a host the panel has not spoken to for a while.

// DNSRecord is one resource record on its way to a zone file.
type DNSRecord struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	TTL      int    `json:"ttl"`
	Value    string `json:"value"`
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
	Port     int    `json:"port"`
	Flags    int    `json:"flags"`
	Tag      string `json:"tag"`
}

// DNSZone is one zone to serve.
type DNSZone struct {
	Name        string      `json:"name"`
	Kind        string      `json:"kind"`
	PrimaryNS   string      `json:"primary_ns"`
	Hostmaster  string      `json:"hostmaster"`
	Serial      int64       `json:"serial"`
	Refresh     int         `json:"refresh"`
	Retry       int         `json:"retry"`
	Expire      int         `json:"expire"`
	Minimum     int         `json:"minimum"`
	TTL         int         `json:"ttl"`
	Nameservers []string    `json:"nameservers"`
	Records     []DNSRecord `json:"records"`

	DNSSEC        bool     `json:"dnssec"`
	AllowTransfer []string `json:"allow_transfer"`
	AlsoNotify    []string `json:"also_notify"`
	Masters       []string `json:"masters"`
}

// DNSSettings are the name server's own options.
type DNSSettings struct {
	ListenOn      []string `json:"listen_on"`
	AllowTransfer []string `json:"allow_transfer"`
	DNSSECPolicy  string   `json:"dnssec_policy"`
}

// DNSDesired is the complete state the host should be in.
type DNSDesired struct {
	Settings DNSSettings
	Zones    []DNSZone
}

// DNSStatus is what the host reports about its name server.
type DNSStatus struct {
	Available      bool   `json:"available"`
	Running        bool   `json:"running"`
	CanInstall     bool   `json:"can_install"`
	SupportsDNSSEC bool   `json:"supports_dnssec"`
	Version        string `json:"version"`
	Reason         string `json:"reason"`
	ConfigPath     string `json:"config_path"`
	IncludePath    string `json:"include_path"`
	ZoneDir        string `json:"zone_dir"`
	// ManagedConfig reports whether named.conf is the panel's own, and
	// ConfigIncluded whether it pulls in the panel's zones at all. The second
	// being false means everything the panel recorded is on disk and served by
	// nobody.
	ManagedConfig  bool     `json:"managed_config"`
	ConfigIncluded bool     `json:"config_included"`
	Recursion      bool     `json:"recursion"`
	ListenOn       []string `json:"listen_on"`
	Zones          []string `json:"zones"`
	FirewallOpen   bool     `json:"firewall_open"`
	FirewallReason string   `json:"firewall_reason"`
	Warnings       []string `json:"warnings"`
}

// DNSReconcileResult reports what a reconcile did.
type DNSReconcileResult struct {
	IncludePath   string   `json:"include_path"`
	ConfigPath    string   `json:"config_path"`
	CreatedConfig bool     `json:"created_config"`
	AddedInclude  bool     `json:"added_include"`
	Changed       []string `json:"changed"`
	Removed       []string `json:"removed"`
	Signed        []string `json:"signed"`
	Reloaded      bool     `json:"reloaded"`
	Warnings      []string `json:"warnings"`
}

// DNSZoneState is what the running server says about one zone.
type DNSZoneState struct {
	Zone string `json:"zone"`
	Type string `json:"type"`
	// Serial is the file's; SignedSerial is what is being served after signing,
	// and the two differ on every signed zone.
	Serial       int64  `json:"serial"`
	SignedSerial int64  `json:"signed_serial"`
	Secure       bool   `json:"secure"`
	LastLoaded   string `json:"last_loaded"`
	LastTransfer string `json:"last_transfer"`
	Loaded       bool   `json:"loaded"`
	Reason       string `json:"reason"`
}

// DNSKey is one DNSSEC key.
type DNSKey struct {
	ID          int    `json:"id"`
	Algorithm   string `json:"algorithm"`
	Role        string `json:"role"`
	Published   bool   `json:"published"`
	KeySigning  bool   `json:"key_signing"`
	ZoneSigning bool   `json:"zone_signing"`
	Rollover    string `json:"rollover"`
}

// DNSDelegationSigner is the record the parent zone's registrar needs.
type DNSDelegationSigner struct {
	KeyTag     int    `json:"key_tag"`
	Algorithm  int    `json:"algorithm"`
	DigestType int    `json:"digest_type"`
	Digest     string `json:"digest"`
	Record     string `json:"record"`
}

// DNSSigningStatus is a zone's signing state.
type DNSSigningStatus struct {
	Zone   string                `json:"zone"`
	Policy string                `json:"policy"`
	Keys   []DNSKey              `json:"keys"`
	DS     []DNSDelegationSigner `json:"ds"`
	Reason string                `json:"reason"`
}

// DNSStatusOf reports on the host's name server.
func (c *Client) DNSStatusOf(ctx context.Context, requestID string) (DNSStatus, error) {
	var result DNSStatus
	err := c.call(ctx, requestID, protocol.OperationDNSStatus, map[string]any{}, &result)
	return result, err
}

// DNSInstall puts a name server on the host.
func (c *Client) DNSInstall(ctx context.Context, requestID string) (DNSStatus, error) {
	var result DNSStatus
	err := c.call(ctx, requestID, protocol.OperationDNSInstall, map[string]any{}, &result)
	return result, err
}

// DNSReconcile makes the host serve what the panel has recorded.
func (c *Client) DNSReconcile(ctx context.Context, requestID string,
	desired DNSDesired,
) (DNSReconcileResult, error) {
	var result DNSReconcileResult
	err := c.call(ctx, requestID, protocol.OperationDNSReconcile, map[string]any{
		"settings": desired.Settings,
		"zones":    desired.Zones,
	}, &result)
	return result, err
}

// DNSZoneStatus asks the running server about one zone.
func (c *Client) DNSZoneStatus(ctx context.Context, requestID, zone string) (DNSZoneState, error) {
	var result DNSZoneState
	err := c.call(ctx, requestID, protocol.OperationDNSZoneStatus,
		map[string]any{"zone": zone}, &result)
	return result, err
}

// DNSSigning reports a zone's keys and the DS record its parent needs.
func (c *Client) DNSSigning(ctx context.Context, requestID, zone string) (DNSSigningStatus, error) {
	var result DNSSigningStatus
	err := c.call(ctx, requestID, protocol.OperationDNSSigning,
		map[string]any{"zone": zone}, &result)
	return result, err
}
