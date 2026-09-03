package dns

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DNSSEC, and what this package does and does not do about it.
//
// It does not sign anything. named signs, using dnssec-policy: it generates the
// keys, keeps the zone signed as records change, and rolls the keys over on its
// own schedule. The panel turns that on for a zone, reports what named has, and
// gives the operator the one thing named cannot do for them — the DS record the
// parent zone's registrar needs.
//
// That last part is the whole reason DNSSEC has a page in a control panel. A
// zone can be signed perfectly and mean nothing: until the DS record is in the
// parent, no resolver knows to check the signatures. And it is the one step
// that is not automatic, because the parent belongs to a registrar this panel
// has no account with.

// Key is one DNSSEC key named holds for a zone.
type Key struct {
	// ID is the key tag, which is what a DS record refers to.
	ID int `json:"id"`
	// Algorithm is the signing algorithm's name, as named reports it.
	Algorithm string `json:"algorithm"`
	// Role is CSK, KSK or ZSK.
	Role string `json:"role"`
	// Published, KeySigning and ZoneSigning are the three states named tracks
	// separately, because a key is introduced and withdrawn in stages.
	Published    bool   `json:"published"`
	KeySigning   bool   `json:"key_signing"`
	ZoneSigning  bool   `json:"zone_signing"`
	RolloverText string `json:"rollover,omitempty"`
}

// DS is a delegation signer record, for the parent zone.
type DS struct {
	// KeyTag identifies the key this covers.
	KeyTag int `json:"key_tag"`
	// Algorithm and DigestType are the numbers a registrar's form asks for.
	Algorithm  int `json:"algorithm"`
	DigestType int `json:"digest_type"`
	// Digest is the hash itself.
	Digest string `json:"digest"`
	// Record is the whole thing as a line, for a registrar that takes one.
	Record string `json:"record"`
}

// SigningStatus is what the panel knows about a signed zone.
type SigningStatus struct {
	Zone string `json:"zone"`
	// Policy is the dnssec-policy named is applying.
	Policy string `json:"policy"`
	Keys   []Key  `json:"keys"`
	DS     []DS   `json:"ds"`
	// Reason explains an empty status: signing is off, the server is not
	// running, or named has not generated a key yet.
	Reason string `json:"reason,omitempty"`
}

// Signing reports what named has done about signing a zone.
func (p *Provider) Signing(ctx context.Context, zone string) (SigningStatus, error) {
	status := SigningStatus{Zone: zone, Keys: []Key{}, DS: []DS{}}
	if !p.Available() {
		return status, ErrUnavailable
	}
	if !p.runner.Available(CommandRndc) {
		status.Reason = "this host has no rndc, so the name server cannot be asked about its keys"
		return status, nil
	}

	result, err := p.runner.Run(ctx, CommandRndc, "dnssec", "-status", zone)
	if err != nil {
		return status, fmt.Errorf("ask the name server about %s: %w", zone, err)
	}
	if !result.Succeeded() {
		// The usual cause is a zone that is not signed, which rndc reports as
		// a failure. It is not one — it is the answer.
		status.Reason = firstLine(result.Stderr, result.Stdout)
		return status, nil
	}

	status.Policy, status.Keys = parseSigningStatus(result.Stdout)
	status.DS = p.dsRecords(ctx, zone)
	return status, nil
}

// parseSigningStatus reads `rndc dnssec -status`.
//
// The output is meant for a person, which makes it the same kind of interface
// as ftpwho in Phase 7.1 and is parsed with the same caution: a line that is
// not understood is skipped rather than guessed at, and the sample this was
// written against is frozen in the tests.
func parseSigningStatus(output string) (string, []Key) {
	policy := ""
	keys := make([]Key, 0, 2)
	var current *Key

	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}

		if rest, ok := strings.CutPrefix(line, "dnssec-policy:"); ok {
			policy = strings.TrimSpace(rest)
			continue
		}

		// "key: 4079 (ECDSAP256SHA256), CSK"
		if rest, ok := strings.CutPrefix(line, "key:"); ok {
			key := parseKeyLine(rest)
			keys = append(keys, key)
			current = &keys[len(keys)-1]
			continue
		}
		if current == nil {
			continue
		}

		switch {
		case strings.HasPrefix(line, "published:"):
			current.Published = yesNo(line)
		case strings.HasPrefix(line, "key signing:"):
			current.KeySigning = yesNo(line)
		case strings.HasPrefix(line, "zone signing:"):
			current.ZoneSigning = yesNo(line)
		case strings.HasPrefix(line, "No rollover scheduled"),
			strings.HasPrefix(line, "Next rollover scheduled"):
			current.RolloverText = line
		}
	}
	return policy, keys
}

// parseKeyLine reads " 4079 (ECDSAP256SHA256), CSK".
func parseKeyLine(rest string) Key {
	key := Key{}
	fields := strings.Fields(strings.TrimSpace(rest))
	if len(fields) == 0 {
		return key
	}
	if id, err := strconv.Atoi(fields[0]); err == nil {
		key.ID = id
	}
	for _, field := range fields[1:] {
		trimmed := strings.Trim(field, "(),")
		switch trimmed {
		case "CSK", "KSK", "ZSK":
			key.Role = trimmed
		default:
			if key.Algorithm == "" && trimmed != "" {
				key.Algorithm = trimmed
			}
		}
	}
	return key
}

// yesNo reads the "yes"/"no" that follows a colon on a status line.
func yesNo(line string) bool {
	_, after, found := strings.Cut(line, ":")
	if !found {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(after), "yes")
}

// dsRecords builds the delegation signer records for a zone's keys.
//
// Every key file for the zone is offered to dnssec-dsfromkey and the ones it
// refuses are dropped: a DS is only meaningful for a key that signs the DNSKEY
// set, and dnssec-dsfromkey is the thing that knows which those are. Asking it
// is more reliable than reading the flags out of the file here and being wrong
// about a key type BIND adds later.
func (p *Provider) dsRecords(ctx context.Context, zone string) []DS {
	records := make([]DS, 0, 2)
	if !p.runner.Available(CommandDNSSECFromKey) {
		return records
	}

	entries, err := os.ReadDir(p.paths.KeyDir())
	if err != nil {
		return records
	}
	prefix := "K" + zone + "."
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".key") {
			continue
		}
		path := filepath.Join(p.paths.KeyDir(), name)
		// SHA-256, which is the digest every registrar accepts and the only
		// one still recommended.
		result, err := p.runner.Run(ctx, CommandDNSSECFromKey, "-2", path)
		if err != nil || !result.Succeeded() {
			continue
		}
		if record, ok := parseDS(firstLine(result.Stdout)); ok {
			records = append(records, record)
		}
	}
	return records
}

// parseDS reads "example.test. IN DS 4079 13 2 814CBB08...".
func parseDS(line string) (DS, bool) {
	fields := strings.Fields(line)
	if len(fields) < 7 || fields[2] != "DS" {
		return DS{}, false
	}
	tag, err := strconv.Atoi(fields[3])
	if err != nil {
		return DS{}, false
	}
	algorithm, err := strconv.Atoi(fields[4])
	if err != nil {
		return DS{}, false
	}
	digestType, err := strconv.Atoi(fields[5])
	if err != nil {
		return DS{}, false
	}
	return DS{
		KeyTag:     tag,
		Algorithm:  algorithm,
		DigestType: digestType,
		Digest:     strings.Join(fields[6:], ""),
		Record:     line,
	}, true
}
