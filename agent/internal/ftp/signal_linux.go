//go:build linux

package ftp

import (
	"errors"
	"fmt"
	"syscall"
)

// terminate asks one process to end.
//
// SIGTERM rather than SIGKILL: proftpd closes the control connection cleanly
// and logs the disconnect, where a kill would leave the scoreboard entry behind
// and the next page load would offer to disconnect a session that is gone.
func terminate(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("%w: %d", ErrNoSession, pid)
		}
		return fmt.Errorf("end FTP session %d: %w", pid, err)
	}
	return nil
}
