package dns

import (
	"fmt"
	"sort"
	"strings"
)

// Settings are the server-wide options the panel manages.
//
// There are few of them on purpose. Everything else in named.conf belongs to
// whoever wrote that file, and the provider reports those settings rather than
// replacing them.
type Settings struct {
	// ListenOn are the addresses the server answers on. Empty means every
	// address, which is what an authoritative server on a hosting box is for.
	ListenOn []string `json:"listen_on"`
	// AllowTransfer is the default for zones that name none of their own.
	AllowTransfer []string `json:"allow_transfer"`
	// DNSSECPolicy is the named policy applied to signed zones. "default" is
	// BIND's own, which is a single ECDSA key rolled on BIND's schedule; it is
	// a name here rather than a set of knobs because inventing a key policy is
	// how a zone ends up unresolvable for the length of its longest TTL.
	DNSSECPolicy string `json:"dnssec_policy"`
}

// DefaultDNSSECPolicy is BIND's own built-in policy.
const DefaultDNSSECPolicy = "default"

// withDefaults fills in what the caller left empty.
func (s Settings) withDefaults() Settings {
	if s.DNSSECPolicy == "" {
		s.DNSSECPolicy = DefaultDNSSECPolicy
	}
	return s
}

// RenderInclude writes the file of zone statements the panel owns.
//
// Only zone statements and nothing global: this file is included from a
// named.conf the panel may not have written, and a global directive in an
// include is a change to somebody else's server made from a file they did not
// know was authoritative.
func RenderInclude(p Paths, s Settings, zones []Zone) (string, error) {
	p = p.withDefaults()
	s = s.withDefaults()

	var out strings.Builder
	fmt.Fprintf(&out, "%s. Do not edit: it is rewritten whenever a zone changes.\n", confMarker)
	out.WriteString("//\n")
	out.WriteString("// Every zone the panel serves is here. A zone removed from the panel is\n")
	out.WriteString("// removed from this file, which is what makes a delete take effect.\n\n")

	ordered := append([]Zone(nil), zones...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })

	for _, zone := range ordered {
		block, err := renderZoneBlock(p, s, zone)
		if err != nil {
			return "", err
		}
		out.WriteString(block)
	}
	return out.String(), nil
}

// renderZoneBlock writes one zone statement.
func renderZoneBlock(p Paths, s Settings, zone Zone) (string, error) {
	var out strings.Builder
	fmt.Fprintf(&out, "zone %q IN {\n", zone.Name)

	switch zone.Kind {
	case "slave":
		if len(zone.Masters) == 0 {
			return "", fmt.Errorf(
				"%w: the secondary zone %s names no primary to transfer from",
				ErrInvalidConfig, zone.Name)
		}
		out.WriteString("\ttype slave;\n")
		fmt.Fprintf(&out, "\tfile %q;\n", p.ZoneFile(zone.Name))
		fmt.Fprintf(&out, "\tprimaries { %s };\n", addressList(zone.Masters))
	default:
		out.WriteString("\ttype master;\n")
		fmt.Fprintf(&out, "\tfile %q;\n", p.ZoneFile(zone.Name))
	}

	// Transfers are denied unless somebody was named. BIND's own default is to
	// allow them to anyone, which hands a complete list of a customer's
	// internal names to whoever asks.
	transfer := zone.AllowTransfer
	if len(transfer) == 0 {
		transfer = s.AllowTransfer
	}
	if len(transfer) == 0 {
		out.WriteString("\tallow-transfer { none; };\n")
	} else {
		fmt.Fprintf(&out, "\tallow-transfer { %s };\n", addressList(transfer))
	}

	if len(zone.AlsoNotify) > 0 {
		fmt.Fprintf(&out, "\talso-notify { %s };\n", addressList(zone.AlsoNotify))
	}

	// Signing is only ever asked of a zone this server is authoritative for as
	// a primary. A secondary receives a signed zone already signed; asking
	// named to sign one it does not own is a configuration it refuses to load.
	if zone.DNSSEC && zone.Kind != "slave" {
		fmt.Fprintf(&out, "\tdnssec-policy %q;\n", s.DNSSECPolicy)
		// Required in 9.16 and 9.18 for a zone backed by a file: without it
		// named signs nothing and says nothing about why.
		out.WriteString("\tinline-signing yes;\n")
		fmt.Fprintf(&out, "\tkey-directory %q;\n", p.KeyDir())
	}

	out.WriteString("};\n\n")
	return out.String(), nil
}

// addressList renders an address match list.
//
// Every element has been through validate.MatchAddress, which is what makes it
// safe to write into a file named parses: the semicolons here are syntax the
// panel produced, not text that arrived from anywhere.
func addressList(addresses []string) string {
	parts := make([]string, 0, len(addresses))
	for _, address := range addresses {
		trimmed := strings.TrimSpace(address)
		if trimmed == "" {
			continue
		}
		parts = append(parts, trimmed+";")
	}
	if len(parts) == 0 {
		return "none;"
	}
	return strings.Join(parts, " ")
}

// RenderMainConf writes a named.conf for a host that has none.
//
// It is an authoritative server's configuration: no recursion, no transfers by
// default, and answers for the zones the panel serves. A resolver and an
// authoritative server in one daemon is the arrangement BIND's own sample
// configuration spends three paragraphs advising against, and a recursive
// server reachable from the internet is an amplifier somebody else will use.
func RenderMainConf(p Paths, s Settings) string {
	p = p.withDefaults()
	s = s.withDefaults()

	listen := "any;"
	if len(s.ListenOn) > 0 {
		listen = addressList(s.ListenOn)
	}

	var out strings.Builder
	fmt.Fprintf(&out, "%s.\n", confMarker)
	out.WriteString("//\n")
	out.WriteString("// This host had no named.conf, so the panel wrote one. It is an\n")
	out.WriteString("// authoritative server: it answers for the zones it is given and\n")
	out.WriteString("// resolves nothing on anybody's behalf.\n\n")

	out.WriteString("options {\n")
	fmt.Fprintf(&out, "\tdirectory %q;\n", p.StateDir)
	fmt.Fprintf(&out, "\tlisten-on { %s };\n", listen)
	out.WriteString("\tlisten-on-v6 { any; };\n")
	out.WriteString("\tpid-file \"/var/run/named/named.pid\";\n")
	out.WriteString("\n")
	out.WriteString("\t// An authoritative server answers for its own zones and nothing\n")
	out.WriteString("\t// else. A recursive server open to the internet is an amplifier.\n")
	out.WriteString("\trecursion no;\n")
	out.WriteString("\tallow-recursion { none; };\n")
	out.WriteString("\n")
	out.WriteString("\t// BIND's default is to allow a transfer to anyone, which hands\n")
	out.WriteString("\t// every name in every zone to whoever asks. Zones override this\n")
	out.WriteString("\t// individually.\n")
	out.WriteString("\tallow-transfer { none; };\n")
	out.WriteString("};\n\n")

	out.WriteString("// rndc is how the panel tells a running server to re-read what it\n")
	out.WriteString("// wrote. The key is BIND's own, generated at install time.\n")
	fmt.Fprintf(&out, "include %q;\n", p.ConfigDir+"/rndc.key")
	out.WriteString("controls {\n")
	out.WriteString("\tinet 127.0.0.1 port 953 allow { 127.0.0.1; } keys { \"rndc-key\"; };\n")
	out.WriteString("};\n\n")

	fmt.Fprintf(&out, "include %q;\n", p.Include())
	return out.String()
}

// includeLine is the line a foreign named.conf needs for the panel's zones to
// be served.
func includeLine(p Paths) string {
	return fmt.Sprintf("include %q;", p.Include())
}

// HasInclude reports whether a named.conf already pulls in the panel's zones.
func HasInclude(content string, p Paths) bool {
	// Matched on the path rather than the whole line: an operator may have
	// written the include themselves, with their own spacing, and adding a
	// second one would be a duplicate-zone error on the next reload.
	return strings.Contains(content, p.Include())
}

// AddInclude returns a named.conf with the panel's include appended.
//
// Appending one line is the whole intervention. The alternative — parsing
// somebody's configuration and deciding where the line belongs — would be a
// panel that rewrites files it does not understand, and BIND's include is
// position-independent at the top level, so the end is as correct as anywhere.
func AddInclude(content string, p Paths) string {
	trimmed := strings.TrimRight(content, "\n")
	return trimmed + "\n\n" +
		"// Added by JotHost Panel: the zones it serves are defined in this file.\n" +
		"// Removing this line stops the panel's zones being served; it does not\n" +
		"// delete them.\n" +
		includeLine(p) + "\n"
}
