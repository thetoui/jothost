//go:build linux

package collectors

import "syscall"

// fsUsage is the subset of statfs this package needs.
type fsUsage struct {
	totalBytes     uint64
	freeBytes      uint64
	availableBytes uint64
	inodesTotal    uint64
	inodesFree     uint64
}

// statfsUsage reads capacity for a mounted filesystem.
//
// This is a syscall rather than a parse of df output: df's format varies by
// implementation and locale, and running it would mean executing a program to
// answer a question the kernel answers directly.
func statfsUsage(mountPoint string) (fsUsage, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(mountPoint, &stat); err != nil {
		return fsUsage{}, err
	}

	blockSize := uint64(stat.Bsize)
	return fsUsage{
		totalBytes: stat.Blocks * blockSize,
		freeBytes:  stat.Bfree * blockSize,
		// Bavail excludes blocks reserved for root, which is what an
		// unprivileged process can actually use.
		availableBytes: stat.Bavail * blockSize,
		inodesTotal:    stat.Files,
		inodesFree:     stat.Ffree,
	}, nil
}
