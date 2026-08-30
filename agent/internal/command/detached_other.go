//go:build !linux

package command

import "os/exec"

// configureDetached is a no-op off Linux.
//
// The Agent only ever runs on Linux; this exists so the package still builds
// on a developer's machine, where the tests that matter are the ones about
// argument handling rather than process credentials.
func configureDetached(*exec.Cmd, DetachedOptions) {}
