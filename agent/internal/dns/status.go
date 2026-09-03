package dns

import (
	"context"
	"os"
	"strconv"
	"strings"
)

// Status is what the panel can say about this host's name server.
type Status struct {
	Available bool   `json:"available"`
	Running   bool   `json:"running"`
	Version   string `json:"version"`
	// CanInstall reports whether the panel could install a name server here.
	CanInstall bool `json:"can_install"`
	// SupportsDNSSEC reports whether this BIND can sign and roll keys itself.
	SupportsDNSSEC bool `json:"supports_dnssec"`

	ConfigPath  string `json:"config_path"`
	IncludePath string `json:"include_path"`
	ZoneDir     string `json:"zone_dir"`
	// ManagedConfig reports whether named.conf is the panel's own. When it is
	// not, the panel has added one include line to it and owns nothing else in
	// it — which is what the settings below are for.
	ManagedConfig bool `json:"managed_config"`
	// ConfigIncluded reports whether named.conf actually pulls in the panel's
	// zones. False means everything the panel has recorded is written to disk
	// and served by nobody.
	ConfigIncluded bool `json:"config_included"`

	// Recursion and ListenOn are read from whatever named.conf this host has
	// and reported rather than changed. See the notes on Warnings.
	Recursion bool     `json:"recursion"`
	ListenOn  []string `json:"listen_on"`

	// Zones are the zones this host is configured to serve.
	Zones []string `json:"zones"`

	// FirewallOpen reports whether port 53 is actually reachable, and
	// FirewallReason says what is closed when it is not.
	FirewallOpen   bool   `json:"firewall_open"`
	FirewallReason string `json:"firewall_reason,omitempty"`

	// Warnings are things about this host's configuration an operator should
	// know and the panel will not change on its own.
	Warnings []string `json:"warnings,omitempty"`

	// Reason explains an unavailable server.
	Reason string `json:"reason,omitempty"`
}

// Status reports on the host's name server.
func (p *Provider) Status(ctx context.Context, canInstall bool) Status {
	status := Status{
		CanInstall: canInstall,
		Zones:      []string{},
		ListenOn:   []string{},
	}
	if p == nil || !p.Available() {
		status.Reason = ErrUnavailable.Error()
		return status
	}

	status.Available = true
	status.Running = p.Running(ctx)
	status.Version = p.Version(ctx)
	status.SupportsDNSSEC = p.SupportsDNSSEC(ctx)
	status.ConfigPath = p.paths.MainConf()
	status.IncludePath = p.paths.Include()
	status.ZoneDir = p.paths.StateDir
	status.Zones = p.ServedZones()

	if data, err := os.ReadFile(p.paths.MainConf()); err == nil {
		status.ManagedConfig = strings.Contains(string(data), confMarker)
		status.ConfigIncluded = HasInclude(string(data), p.paths)
	}

	status.Recursion, status.ListenOn = p.globalOptions(ctx)
	status.Warnings = p.warnings(status)
	return status
}

// globalOptions reads the settings the panel reports but does not own.
//
// Through `named-checkconf -p`, which prints the configuration as named
// understands it — includes resolved, defaults applied. Reading the file
// directly would answer about the text rather than about the server, and a
// directive that is absent is still in force at its default.
func (p *Provider) globalOptions(ctx context.Context) (bool, []string) {
	listen := []string{}
	result, err := p.runner.Run(ctx, CommandNamedCheckconf, "-p", p.paths.MainConf())
	if err != nil || !result.Succeeded() {
		return false, listen
	}

	recursion := false
	inListen := false
	for _, raw := range strings.Split(result.Stdout, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "recursion "):
			recursion = strings.HasPrefix(strings.TrimPrefix(line, "recursion "), "yes")
		case strings.HasPrefix(line, "listen-on"):
			inListen = true
		case inListen && line == "};":
			inListen = false
		case inListen:
			address := strings.Trim(strings.TrimSuffix(line, ";"), "\"")
			if address != "" && address != "{" {
				listen = append(listen, address)
			}
		}
	}
	return recursion, listen
}

// warnings describes what is wrong with this host's DNS that the panel will not
// fix by itself.
//
// Each one is a thing that produces no error anywhere: a server that is running
// and serving nothing, a server nobody can reach, and a server that is an open
// resolver. All three look healthy from the inside.
func (p *Provider) warnings(status Status) []string {
	warnings := make([]string, 0, 3)

	if !status.ConfigIncluded && len(status.Zones) > 0 {
		warnings = append(warnings,
			"this host's named.conf does not include the panel's zone file, so none "+
				"of the zones below are being served")
	}
	if status.Recursion {
		warnings = append(warnings,
			"this name server is configured to answer recursive queries. An "+
				"authoritative server that resolves for anyone is used to amplify "+
				"denial-of-service attacks against other people")
	}
	for _, address := range status.ListenOn {
		if address == "127.0.0.1" || address == "::1" || address == "localhost" {
			warnings = append(warnings,
				"this name server only listens on the loopback address, so nothing "+
					"outside this machine can query it")
			break
		}
	}
	return warnings
}

// ZoneState is what the running server says about one zone.
//
// It carries two serials, and the distinction between them is load-bearing.
// With inline signing named maintains its own serial on the signed copy, and it
// diverges from the one in the file the panel wrote — a file at 2026090302 was
// served as 2026090304 within seconds of being loaded. A panel that compared
// the served serial with its own would report drift on every signed zone,
// forever.
type ZoneState struct {
	Zone string `json:"zone"`
	// Type is "primary" or "secondary", as named reports it.
	Type string `json:"type"`
	// Serial is the serial of the file the panel wrote.
	Serial int64 `json:"serial"`
	// SignedSerial is the serial named is serving after signing. Zero when the
	// zone is not signed.
	SignedSerial int64 `json:"signed_serial"`
	// Secure reports whether the zone is signed.
	Secure bool `json:"secure"`
	// LastLoaded and LastTransfer are what a secondary's replication is judged
	// by: a zone that has never transferred is a zone this server cannot
	// answer for.
	LastLoaded   string `json:"last_loaded,omitempty"`
	LastTransfer string `json:"last_transfer,omitempty"`
	// Loaded reports whether the server has the zone at all.
	Loaded bool `json:"loaded"`
	// Reason explains a zone the server does not have.
	Reason string `json:"reason,omitempty"`
}

// ZoneStatus asks the running server about one zone.
func (p *Provider) ZoneStatus(ctx context.Context, zone string) (ZoneState, error) {
	state := ZoneState{Zone: zone}
	if !p.Available() {
		return state, ErrUnavailable
	}
	if !p.runner.Available(CommandRndc) {
		state.Reason = "this host has no rndc, so the running server cannot be asked"
		return state, nil
	}

	result, err := p.runner.Run(ctx, CommandRndc, "zonestatus", zone)
	if err != nil {
		return state, err
	}
	if !result.Succeeded() {
		// "not found" is the ordinary answer for a zone the server has not
		// been reconfigured for yet, and it is an answer rather than a fault.
		state.Reason = firstLine(result.Stderr, result.Stdout)
		return state, nil
	}

	state.Loaded = true
	for _, raw := range strings.Split(result.Stdout, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(raw), ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "type":
			state.Type = value
		case "serial":
			state.Serial = parseSerial(value)
		case "signed serial":
			state.SignedSerial = parseSerial(value)
		case "secure":
			state.Secure = value == "yes"
		case "last loaded":
			state.LastLoaded = value
		case "xfer status", "transfer status":
			state.LastTransfer = value
		}
	}
	return state, nil
}

// parseSerial reads a serial, returning zero for anything unparseable rather
// than guessing.
func parseSerial(value string) int64 {
	serial, err := strconv.ParseInt(strings.Fields(value + " ")[0], 10, 64)
	if err != nil {
		return 0
	}
	return serial
}
