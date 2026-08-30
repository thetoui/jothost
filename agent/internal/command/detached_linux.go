//go:build linux

package command

import (
	"os/exec"
	"syscall"
)

// configureDetached puts a background program in its own session, and under
// its own account when one was given.
//
// Setsid rather than Setpgid: a new session detaches the child from the
// Agent's controlling terminal as well as its process group, so a signal sent
// to the Agent — including the one a container runtime sends on shutdown —
// does not reach an application the panel is running.
//
// The credential is applied by the kernel at exec, which is what makes it a
// real boundary rather than a convention: the process never runs as root even
// for an instant.
func configureDetached(cmd *exec.Cmd, opts DetachedOptions) {
	attr := &syscall.SysProcAttr{Setsid: true}

	if opts.SetCredential {
		attr.Credential = &syscall.Credential{
			Uid: uint32(opts.UID),
			Gid: uint32(opts.GID),
			// No supplementary groups. An application inherits nothing from
			// the Agent's own group membership, which on a root daemon is
			// every group on the host.
			NoSetGroups: false,
			Groups:      []uint32{},
		}
	}

	cmd.SysProcAttr = attr
}
