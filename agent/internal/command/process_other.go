//go:build !linux

package command

import "os/exec"

// configureProcessGroup is a no-op off Linux.
//
// The Agent only runs on Linux; these stubs keep the module building on a
// developer's machine (ARCHITECTURE.md section 16).
func configureProcessGroup(*exec.Cmd) {}

// killProcessGroup falls back to killing just the process.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
