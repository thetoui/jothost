package firewall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/validate"
)

// The change protocol, which is CLAUDE.md section 19 in code:
//
//  1. Back up the current rules.
//  2. Validate the new ones — and refuse a change that would lock the host out.
//  3. Apply them temporarily: the change is live, and a timer is armed to undo
//     it.
//  4. Verify: the resulting ruleset is re-read and checked, and the caller must
//     confirm over the network the change could have broken.
//  5. Commit, by confirming inside the window.
//  6. Roll back automatically if the window closes without one.
//
// Step 4 is the one worth being precise about, because it is easy to claim more
// than is true. The Agent cannot test whether the host is reachable *from the
// internet*: a connection it makes to the host's own address is routed over the
// loopback interface, hits the INPUT chain as loopback traffic, and is allowed
// by a rule ufw installs for exactly that reason. Such a test passes while the
// host is unreachable, which is worse than no test.
//
// So the Agent verifies what it genuinely can — that the rules ufw ended up with
// still allow the guarded ports — and the *caller* supplies the other half. The
// confirmation is an ordinary request that has to cross the network the change
// governs. If the change cut the panel off, the confirmation cannot arrive, and
// the timer undoes it.

// DefaultWindow is how long a change stays provisional.
//
// Long enough for a browser to notice the response, re-read the firewall, and
// confirm; short enough that an operator who has just locked themselves out is
// waiting rather than rebooting. Plesk uses sixty seconds for the same reason.
const DefaultWindow = 60 * time.Second

// MaxWindow bounds what a caller may ask for.
const MaxWindow = 10 * time.Minute

// Pending is a change that has been applied and not yet confirmed.
type Pending struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Rule   Rule   `json:"rule,omitempty"`
	Policy string `json:"policy,omitempty"`
	// Backup is the directory holding the rules as they were before.
	Backup string `json:"backup"`
	// Deadline is when the change is undone if nothing confirms it.
	Deadline time.Time `json:"deadline"`
	// StartedBy is the request that made the change, for the audit trail.
	StartedBy string `json:"started_by,omitempty"`
}

// Expired reports whether the window has closed.
func (p Pending) Expired(now time.Time) bool { return now.After(p.Deadline) }

// changeState is the Agent's memory of a provisional change.
type changeState struct {
	mu      sync.Mutex
	pending *Pending
	timer   *time.Timer
	log     *slog.Logger
}

// Apply makes a change provisionally and arms the rollback.
//
// It returns the pending change: the caller has a window in which to confirm
// it, and nothing else may be changed until it does. One change at a time is
// deliberate — two overlapping windows would each hold a backup of a state the
// other had already moved away from, and rolling back would restore neither.
func (p *Provider) Apply(ctx context.Context, change Change, window time.Duration,
	requestID string,
) (Pending, error) {
	if !p.Available() {
		return Pending{}, ErrUnavailable
	}

	p.state.mu.Lock()
	defer p.state.mu.Unlock()

	if p.state.pending != nil && !p.state.pending.Expired(time.Now()) {
		return Pending{}, fmt.Errorf("%w: a change is already waiting to be confirmed",
			ErrChangeInFlight)
	}

	if window <= 0 {
		window = DefaultWindow
	}
	if window > MaxWindow {
		window = MaxWindow
	}

	// 1 and 2: what is there now, and whether the change may be made at all.
	current, err := p.Status(ctx)
	if err != nil {
		return Pending{}, err
	}
	if !current.Available {
		return Pending{}, fmt.Errorf("%w: %s", ErrUnavailable, current.Reason)
	}
	if err := p.Guard(change, current); err != nil {
		return Pending{}, err
	}

	backup, err := p.backup(ctx, current)
	if err != nil {
		return Pending{}, err
	}

	// 3: apply it.
	if err := p.run3(ctx, change); err != nil {
		// Nothing was committed, so the backup is not needed — but it is kept
		// anyway. A failed apply can still have changed something before it
		// failed, and the backup is the only record of what was there.
		return Pending{}, err
	}

	// 4a: the half the Agent can check — did ufw end up somewhere that still
	// allows the guarded ports?
	if err := p.verify(ctx); err != nil {
		if restoreErr := p.restore(ctx, backup); restoreErr != nil {
			// Both halves failed. This is the worst case in the package and it
			// is reported as loudly as an error can be.
			return Pending{}, fmt.Errorf("%w (and the previous rules could not be "+
				"restored: %v)", err, restoreErr)
		}
		return Pending{}, err
	}

	pending := Pending{
		ID:        newChangeID(),
		Kind:      change.Kind,
		Rule:      change.Rule,
		Policy:    change.Policy,
		Backup:    backup,
		Deadline:  time.Now().Add(window),
		StartedBy: requestID,
	}

	// 6: arm the rollback before returning. If the response never reaches the
	// caller — which is exactly what happens when the change cut them off —
	// this is what puts the host back.
	if err := p.arm(pending); err != nil {
		// The change is live and nothing would undo it. Undo it now rather
		// than leave it standing on a promise that cannot be kept.
		if restoreErr := p.restore(ctx, backup); restoreErr != nil {
			return Pending{}, fmt.Errorf("%w (and the previous rules could not be "+
				"restored: %v)", err, restoreErr)
		}
		return Pending{}, err
	}

	return pending, nil
}

// ErrChangeInFlight means another change is waiting to be confirmed.
var ErrChangeInFlight = errors.New("a firewall change is already waiting to be confirmed")

// ErrNoPendingChange means there is nothing to confirm or roll back.
var ErrNoPendingChange = errors.New("no firewall change is waiting")

// Confirm commits a provisional change.
//
// The request that calls this has crossed the network the change governs,
// which is the only proof available that the change did not cut the host off.
func (p *Provider) Confirm(id string) (Pending, error) {
	p.state.mu.Lock()
	defer p.state.mu.Unlock()

	if p.state.pending == nil {
		return Pending{}, ErrNoPendingChange
	}
	if p.state.pending.ID != id {
		// A stale id: the change it names is gone, and confirming the current
		// one because the caller asked about a different one would be
		// confirming something they have not seen.
		return Pending{}, fmt.Errorf("%w: %s", ErrNoPendingChange, id)
	}
	if p.state.pending.Expired(time.Now()) {
		return Pending{}, fmt.Errorf("%w: the window closed and it has been undone",
			ErrNoPendingChange)
	}

	confirmed := *p.state.pending
	p.disarmLocked()
	return confirmed, nil
}

// Rollback undoes a provisional change now, without waiting for the window.
func (p *Provider) Rollback(ctx context.Context, id string) (Pending, error) {
	p.state.mu.Lock()
	defer p.state.mu.Unlock()

	if p.state.pending == nil {
		return Pending{}, ErrNoPendingChange
	}
	if id != "" && p.state.pending.ID != id {
		return Pending{}, fmt.Errorf("%w: %s", ErrNoPendingChange, id)
	}

	pending := *p.state.pending
	if err := p.restore(ctx, pending.Backup); err != nil {
		return Pending{}, err
	}
	p.disarmLocked()
	return pending, nil
}

// PendingChange returns the change waiting to be confirmed, if any.
func (p *Provider) PendingChange() *Pending {
	p.state.mu.Lock()
	defer p.state.mu.Unlock()

	if p.state.pending == nil {
		return nil
	}
	pending := *p.state.pending
	return &pending
}

// RecoverPending puts the host back after an Agent restart.
//
// The timer lives in memory, so a restart during the window would otherwise
// leave a provisional change standing forever — which is the one failure the
// whole protocol exists to prevent, arriving by the back door. The marker on
// disk is what survives, and this is what reads it.
func (p *Provider) RecoverPending(ctx context.Context) error {
	if !p.Available() {
		return nil
	}

	pending, err := p.readPending()
	if err != nil || pending == nil {
		return err
	}

	if pending.Expired(time.Now()) {
		p.state.log.Warn("undoing a firewall change that was never confirmed",
			"change_id", pending.ID, "kind", pending.Kind,
			"deadline", pending.Deadline.Format(time.RFC3339))

		p.state.mu.Lock()
		defer p.state.mu.Unlock()
		if err := p.restore(ctx, pending.Backup); err != nil {
			return err
		}
		return p.clearPending()
	}

	// Still inside its window: re-arm for what is left of it.
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	p.state.pending = pending
	p.state.timer = time.AfterFunc(time.Until(pending.Deadline), func() {
		p.expire(pending.ID)
	})
	p.state.log.Warn("a firewall change is still waiting to be confirmed",
		"change_id", pending.ID, "expires_in", time.Until(pending.Deadline).String())
	return nil
}

// arm records the pending change and starts its timer.
func (p *Provider) arm(pending Pending) error {
	if err := p.writePending(pending); err != nil {
		return err
	}

	p.state.pending = &pending
	p.state.timer = time.AfterFunc(time.Until(pending.Deadline), func() {
		p.expire(pending.ID)
	})
	return nil
}

// disarmLocked clears the pending change. The caller holds the lock.
func (p *Provider) disarmLocked() {
	if p.state.timer != nil {
		p.state.timer.Stop()
		p.state.timer = nil
	}
	p.state.pending = nil
	if err := p.clearPending(); err != nil {
		p.state.log.Error("failed to clear the pending firewall change",
			logger.KeyError, err.Error())
	}
}

// expire is what the timer runs: the change was never confirmed.
func (p *Provider) expire(id string) {
	p.state.mu.Lock()
	defer p.state.mu.Unlock()

	if p.state.pending == nil || p.state.pending.ID != id {
		// Confirmed or rolled back while the timer was firing.
		return
	}

	// A fresh context: the request that made the change is long gone, and its
	// cancellation must not stop the host being put back.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pending := *p.state.pending
	p.state.log.Warn("a firewall change was not confirmed; undoing it",
		"change_id", pending.ID, "kind", pending.Kind)

	if err := p.restore(ctx, pending.Backup); err != nil {
		p.state.log.Error("failed to undo an unconfirmed firewall change",
			"change_id", pending.ID, logger.KeyError, err.Error())
		// The marker is deliberately left in place: the next Agent start finds
		// it expired and tries again.
		return
	}
	p.disarmLocked()
}

// run3 performs step 3 for whichever kind of change this is.
func (p *Provider) run3(ctx context.Context, change Change) error {
	switch change.Kind {
	case ChangeAddRule:
		return p.addRule(ctx, change.Rule)
	case ChangeDeleteRule:
		return p.deleteRule(ctx, change.Rule)
	case ChangeEnable:
		return p.enable(ctx)
	case ChangeDisable:
		return p.disable(ctx)
	case ChangeDefault:
		return p.setDefault(ctx, change.Direction, change.Policy)
	default:
		return fmt.Errorf("%w: unknown change %q", validate.ErrInvalidFirewallRule, change.Kind)
	}
}

// verify is the half of step 4 the Agent can perform.
//
// It re-reads what ufw ended up with and checks the guarded ports are still
// allowed. That is not the same as proving the host is reachable — see the
// note at the top of this file — but it does catch the case that matters most
// here: ufw having done something other than what it was asked.
func (p *Provider) verify(ctx context.Context) error {
	status, err := p.Status(ctx)
	if err != nil {
		return err
	}
	if !status.Available {
		return fmt.Errorf("%w: the firewall stopped answering after the change: %s",
			ErrUnavailable, status.Reason)
	}
	if !status.Enabled {
		// A disabled firewall filters nothing, so nothing is closed.
		return nil
	}
	if status.DefaultIncoming == validate.FirewallAllow {
		return nil
	}

	if missing := p.unprotected(status.Rules); len(missing) > 0 {
		return fmt.Errorf("%w: after the change nothing allows %s",
			ErrWouldLockOut, portList(missing))
	}
	return nil
}

// newChangeID returns an identifier for one change.
func newChangeID() string {
	return "fwc_" + time.Now().UTC().Format("20060102T150405.000000000")
}

// pendingPath is where the marker lives.
func (p *Provider) pendingPath() string { return filepath.Join(p.stateDir, "pending.json") }

func (p *Provider) writePending(pending Pending) error {
	if err := p.ensureStateDir(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the pending change: %w", err)
	}

	// Written and synced before the caller is told the change was made: a
	// crash between applying and recording would leave a change nothing knows
	// to undo.
	file, err := os.OpenFile(p.pendingPath(), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("write the pending change: %w", err)
	}
	defer file.Close()

	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write the pending change: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync the pending change: %w", err)
	}
	return nil
}

func (p *Provider) readPending() (*Pending, error) {
	data, err := os.ReadFile(p.pendingPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read the pending change: %w", err)
	}

	var pending Pending
	if err := json.Unmarshal(data, &pending); err != nil {
		return nil, fmt.Errorf("decode the pending change: %w", err)
	}
	return &pending, nil
}

func (p *Provider) clearPending() error {
	if err := os.Remove(p.pendingPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear the pending change: %w", err)
	}
	return nil
}
