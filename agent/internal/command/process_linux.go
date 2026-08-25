//go:build linux

package command

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup puts the child in its own process group.
//
// Without this, cancelling only kills the program that was started. A program
// that spawns children — a shell script, a package manager — leaves them
// running, and they keep the output pipes open, so the Agent's worker stays
// blocked in Wait long after the deadline passed.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup signals the whole group.
//
// A negative PID targets the process group, so descendants die with the
// program that spawned them.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		// The group may already be gone, or the child may have escaped its
		// group; fall back to killing the process itself.
		return cmd.Process.Kill()
	}
	return nil
}
