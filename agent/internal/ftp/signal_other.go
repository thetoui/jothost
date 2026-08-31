//go:build !linux

package ftp

import "fmt"

// terminate is unavailable off Linux.
//
// The Agent only runs on Linux; this stub keeps the module building on a
// developer's machine (ARCHITECTURE.md section 16).
func terminate(pid int) error {
	return fmt.Errorf("%w: %d", ErrNoSession, pid)
}
