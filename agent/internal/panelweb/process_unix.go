//go:build !windows

package panelweb

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// processAlive reports whether the process named by a pid file is running.
//
// The file is not trusted on its own. A pid file left behind by a master that
// was killed is exactly the state that produces a 502 against a configuration
// that looks perfect, and it is the state this package exists partly to stop
// being invisible.
func processAlive(pidPath string) bool {
	pid, err := readPID(pidPath)
	if err != nil || pid <= 0 {
		return false
	}
	// Signal 0 performs the permission and existence checks without delivering
	// anything.
	if err := syscall.Kill(pid, 0); err != nil {
		return errors.Is(err, syscall.EPERM)
	}
	return true
}

// signalReload asks a running FPM master to re-read its pools.
func signalReload(pidPath string) error {
	pid, err := readPID(pidPath)
	if err != nil {
		return err
	}
	if err := syscall.Kill(pid, syscall.SIGUSR2); err != nil {
		return fmt.Errorf("reload the panel's php-fpm (pid %d): %w", pid, err)
	}
	return nil
}

// signalStop asks a running master to shut down, and removes the pid file it
// leaves behind.
func signalStop(pidPath string) error {
	pid, err := readPID(pidPath)
	if err != nil {
		return err
	}
	// QUIT rather than TERM: FPM finishes the requests it is serving first,
	// and one of them may be an import somebody is halfway through.
	if err := syscall.Kill(pid, syscall.SIGQUIT); err != nil {
		return fmt.Errorf("stop the panel's php-fpm (pid %d): %w", pid, err)
	}
	if err := os.Remove(pidPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", pidPath, err)
	}
	return nil
}
