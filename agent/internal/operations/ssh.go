package operations

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/ssh"
	"github.com/jothost/panel/shared/protocol"
)

// The SSH operations' request boundary.
//
// Two of these can lock an operator out of the machine, so the payloads are
// narrow on purpose: a configure request carries the handful of settings the
// panel manages and nothing else, and a key request carries an account name
// that is matched against the host's own list rather than turned into a path.
// There is no operation here that takes a file name.

// sshConfigurePayload is a change to the server's configuration.
//
// Pointers, so "leave it alone" and "set it to false" are different requests. A
// bool that meant both would turn every partial update into a full one, and the
// field it would silently reset is the one that decides whether anybody can log
// in.
type sshConfigurePayload struct {
	Port                   *int    `json:"port"`
	RootLogin              *string `json:"root_login"`
	PasswordAuthentication *bool   `json:"password_authentication"`
	PubkeyAuthentication   *bool   `json:"pubkey_authentication"`
	PermitEmptyPasswords   *bool   `json:"permit_empty_passwords"`
	X11Forwarding          *bool   `json:"x11_forwarding"`
	MaxAuthTries           *int    `json:"max_auth_tries"`
}

// sshKeyPayload names an account and, where relevant, a key.
type sshKeyPayload struct {
	// Account is a name from the host's own list of login accounts.
	Account string `json:"account"`
	// Key is a whole authorized_keys line.
	Key string `json:"key"`
	// Fingerprint identifies a key to remove. A line number would move when
	// another key went.
	Fingerprint string `json:"fingerprint"`
}

// sshProvider returns the provider, or the error a caller should see when this
// host has no SSH server.
func (r *Registry) sshProvider() (*ssh.Provider, error) {
	if r.deps.SSH == nil || !r.deps.SSH.Available() {
		return nil, Fail(protocol.CodeUnsupported,
			"This host has no SSH server, so there is nothing to configure", nil)
	}
	return r.deps.SSH, nil
}

// handleSSHStatus reports the configuration, the accounts, and what is worth
// changing.
func (r *Registry) handleSSHStatus(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	var payload struct{}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	if r.deps.SSH == nil {
		return map[string]any{
			"config":   ssh.Config{Ports: []int{}, Reason: "the SSH server is not installed on this host"},
			"accounts": []ssh.Account{},
			"findings": []ssh.Finding{},
		}, nil
	}

	config, err := r.deps.SSH.Read(ctx)
	if err != nil {
		return nil, err
	}
	config.Running = r.sshRunning(ctx)

	accounts, err := r.deps.SSH.Accounts()
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"config":   config,
		"accounts": accounts,
		"findings": ssh.Audit(config, accounts),
	}, nil
}

// handleSSHConfigure changes the server's configuration.
func (r *Registry) handleSSHConfigure(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.sshProvider()
	if err != nil {
		return nil, err
	}
	var payload sshConfigurePayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	result, err := provider.Apply(ctx, ssh.Change{
		Port:                   payload.Port,
		RootLogin:              payload.RootLogin,
		PasswordAuthentication: payload.PasswordAuthentication,
		PubkeyAuthentication:   payload.PubkeyAuthentication,
		PermitEmptyPasswords:   payload.PermitEmptyPasswords,
		X11Forwarding:          payload.X11Forwarding,
		MaxAuthTries:           payload.MaxAuthTries,
	}, firewallGuard{registry: r}, sshReloader{registry: r})
	if err != nil {
		return nil, sshError(err)
	}

	result.Config.Running = r.sshRunning(ctx)
	return structToMap(result)
}

// handleSSHKeyList reports one account's authorised keys.
func (r *Registry) handleSSHKeyList(_ context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.sshProvider()
	if err != nil {
		return nil, err
	}
	var payload sshKeyPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	keys, err := provider.Keys(payload.Account)
	if err != nil {
		return nil, sshError(err)
	}
	return map[string]any{"account": payload.Account, "keys": keys, "count": len(keys)}, nil
}

// handleSSHKeyAdd authorises a key for an account.
func (r *Registry) handleSSHKeyAdd(_ context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.sshProvider()
	if err != nil {
		return nil, err
	}
	var payload sshKeyPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	key, err := provider.AddKey(payload.Account, payload.Key)
	if err != nil {
		return nil, sshError(err)
	}
	return structToMap(key)
}

// handleSSHKeyRemove withdraws a key.
func (r *Registry) handleSSHKeyRemove(_ context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.sshProvider()
	if err != nil {
		return nil, err
	}
	var payload sshKeyPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	key, err := provider.RemoveKey(payload.Account, payload.Fingerprint)
	if err != nil {
		return nil, sshError(err)
	}
	return structToMap(key)
}

// sshRunning reports whether the server is up.
//
// Read from the process table through the service detector rather than from the
// configuration: a configuration is only interesting if something is serving it,
// and "the file says port 2222" on a host where sshd is stopped is a fact worth
// qualifying.
func (r *Registry) sshRunning(ctx context.Context) bool {
	if r.deps.Services == nil || r.deps.Collector == nil {
		return false
	}
	for _, detected := range r.deps.Services.Detect(ctx, nil, r.deps.Collector) {
		if detected.Key == "sshd" {
			return detected.Running
		}
	}
	return false
}

// sshReloader restarts the SSH server through the service manager.
//
// A restart rather than a reload: existing sessions are separate processes and
// survive either, and the service manager already has restart as one of its five
// verbs — adding a sixth for one caller would be a wider change than this phase
// needs. The configuration has already been through `sshd -t` by the time this
// runs, so the restart is not where a bad file would be discovered.
type sshReloader struct{ registry *Registry }

func (s sshReloader) ReloadSSH(ctx context.Context) error {
	provider := s.registry.deps.Services
	if provider == nil || !provider.Available() {
		return fmt.Errorf("this host has no service manager to restart the SSH server with")
	}

	_, unit, err := provider.UnitFor(ctx, "sshd", nil)
	if err != nil {
		return fmt.Errorf("find the SSH service: %w", err)
	}
	return provider.Restart(ctx, unit)
}

// firewallGuard answers whether a port would be reachable.
//
// The panel will not open a port as a side effect of an SSH change — a firewall
// change is its own deliberate act with its own protocol (CLAUDE.md section 19)
// — so this only reports, and the refusal upstream says what to do first.
type firewallGuard struct{ registry *Registry }

func (f firewallGuard) PortReachable(ctx context.Context, port int) (bool, string) {
	provider := f.registry.deps.Firewall
	if provider == nil || !provider.Available() {
		// No firewall is not a closed firewall. A host with nothing filtering
		// admits every port, and refusing on that basis would be refusing a
		// change that is perfectly safe.
		return true, ""
	}

	status, err := provider.Status(ctx)
	if err != nil {
		// An unreadable firewall is not proof that the port is blocked, but it
		// is not proof that it is open either. The safe answer for a change
		// that can lock somebody out is the cautious one.
		return false, "the firewall's rules could not be read: " + err.Error()
	}
	if !status.Enabled {
		return true, ""
	}

	for _, rule := range status.Rules {
		if !strings.EqualFold(rule.Action, "allow") {
			continue
		}
		if rule.Direction != "" && !strings.EqualFold(rule.Direction, "in") {
			continue
		}
		if portCovered(rule.Port, port) {
			return true, ""
		}
	}

	return false, fmt.Sprintf(
		"the firewall is on and no rule allows connections to port %d", port)
}

// portCovered reports whether a ufw rule's port field covers a port.
func portCovered(field string, port int) bool {
	if field == "" {
		// A rule with no port covers every port.
		return true
	}

	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		low, high, isRange := strings.Cut(part, ":")
		if !isRange {
			if value, err := strconv.Atoi(part); err == nil && value == port {
				return true
			}
			continue
		}

		from, err := strconv.Atoi(strings.TrimSpace(low))
		if err != nil {
			continue
		}
		to, err := strconv.Atoi(strings.TrimSpace(high))
		if err != nil {
			continue
		}
		if port >= from && port <= to {
			return true
		}
	}
	return false
}

// sshError turns a package error into the code the caller should see.
func sshError(err error) error {
	switch {
	case errors.Is(err, ssh.ErrUnavailable):
		return Fail(protocol.CodeUnsupported,
			"This host has no SSH server, so there is nothing to configure", err)
	case errors.Is(err, ssh.ErrNoDropIn), errors.Is(err, ssh.ErrNotApplied):
		// Not a validation failure: the request was fine and the host is not in
		// a state where the panel will change it.
		return Fail(protocol.CodeInvalidRequest, cleanMessage(err), err)
	case errors.Is(err, ssh.ErrWouldLockOut):
		// The most important error in this phase, and the one whose text an
		// operator has to be able to act on, so it is passed through whole.
		return Fail(protocol.CodeInvalidRequest, cleanMessage(err), err)
	case errors.Is(err, ssh.ErrInvalidConfig), errors.Is(err, ssh.ErrInvalidKey):
		return Fail(protocol.CodeInvalidPayload, cleanMessage(err), err)
	case errors.Is(err, ssh.ErrUnknownAccount), errors.Is(err, ssh.ErrKeyNotFound):
		return Fail(protocol.CodeNotFound, cleanMessage(err), err)
	case errors.Is(err, ssh.ErrDuplicateKey):
		return Fail(protocol.CodeInvalidRequest, cleanMessage(err), err)
	default:
		return err
	}
}

// cleanMessage renders an error for a person.
//
// The wrapped sentinel is dropped where it only repeats what follows: "this
// change would lock everybody out of the host: no account has a key" reads
// better as the second half, which is the part that says what to do.
func cleanMessage(err error) string {
	message := err.Error()
	if _, rest, found := strings.Cut(message, ": "); found && rest != "" {
		return strings.ToUpper(rest[:1]) + rest[1:]
	}
	return message
}
