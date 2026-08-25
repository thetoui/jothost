//go:build !linux

package collectors

// fsUsage is the subset of statfs this package needs.
type fsUsage struct {
	totalBytes     uint64
	freeBytes      uint64
	availableBytes uint64
	inodesTotal    uint64
	inodesFree     uint64
}

// statfsUsage is unavailable off Linux.
//
// The Agent only ever runs on Linux; this stub exists so the module still
// builds on a developer's macOS or Windows machine rather than failing at
// compile time (ARCHITECTURE.md section 16).
func statfsUsage(string) (fsUsage, error) {
	return fsUsage{}, ErrUnsupported
}
