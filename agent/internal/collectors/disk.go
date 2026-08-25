package collectors

import (
	"fmt"
	"sort"
	"strings"
)

// Filesystem is one mounted filesystem's usage.
type Filesystem struct {
	Device     string `json:"device"`
	MountPoint string `json:"mount_point"`
	Type       string `json:"type"`

	TotalBytes uint64 `json:"total_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
	// AvailableBytes excludes the root-reserved blocks, so it is what a
	// non-root process can actually write.
	AvailableBytes uint64  `json:"available_bytes"`
	UsedPercent    float64 `json:"used_percent"`

	InodesTotal uint64 `json:"inodes_total"`
	InodesUsed  uint64 `json:"inodes_used"`
	InodesFree  uint64 `json:"inodes_free"`
	// InodesUsedPercent matters independently of bytes: a filesystem full of
	// tiny files runs out of inodes while still reporting free space.
	InodesUsedPercent float64 `json:"inodes_used_percent"`

	ReadOnly bool `json:"read_only"`
}

// DiskStats is a disk usage sample.
type DiskStats struct {
	Filesystems []Filesystem `json:"filesystems"`
	TotalBytes  uint64       `json:"total_bytes"`
	UsedBytes   uint64       `json:"used_bytes"`
}

// virtualFilesystems are excluded from a "disk usage" report. They are kernel
// interfaces rather than storage, and including them makes the output noise.
var virtualFilesystems = map[string]struct{}{
	"autofs": {}, "bpf": {}, "cgroup": {}, "cgroup2": {}, "configfs": {},
	"debugfs": {}, "devpts": {}, "devtmpfs": {}, "efivarfs": {}, "fuse.gvfsd-fuse": {},
	"fusectl": {}, "hugetlbfs": {}, "mqueue": {}, "nsfs": {}, "overlay": {},
	"proc": {}, "pstore": {}, "ramfs": {}, "rpc_pipefs": {}, "securityfs": {},
	"selinuxfs": {}, "squashfs": {}, "sysfs": {}, "tracefs": {},
}

// mountEntry is one line of /proc/mounts.
type mountEntry struct {
	device     string
	mountPoint string
	fsType     string
	options    []string
}

// Disk reports usage for every real filesystem, or for one mount point when
// mountPoint is non-empty.
//
// The caller-supplied mount point is matched against the mount table rather
// than passed to statfs directly: an arbitrary path would let a caller probe
// the filesystem layout, and only mounted filesystems are meaningful here.
func (c *Collector) Disk(mountPoint string) (DiskStats, error) {
	mounts, err := c.mounts()
	if err != nil {
		return DiskStats{}, err
	}

	var stats DiskStats
	seen := make(map[string]struct{}, len(mounts))

	for _, mount := range mounts {
		if mountPoint != "" && mount.mountPoint != mountPoint {
			continue
		}
		if mountPoint == "" {
			if _, virtual := virtualFilesystems[mount.fsType]; virtual {
				continue
			}
			// A device mounted twice (bind mounts) would double the totals.
			if _, duplicate := seen[mount.device]; duplicate && mount.device != "" {
				continue
			}
		}

		usage, err := statfsUsage(mount.mountPoint)
		if err != nil {
			// A filesystem that cannot be stat'ed — a stale NFS handle, a
			// permission boundary — is skipped rather than failing the whole
			// report, which would make one bad mount hide every good one.
			continue
		}

		fs := Filesystem{
			Device:         mount.device,
			MountPoint:     mount.mountPoint,
			Type:           mount.fsType,
			TotalBytes:     usage.totalBytes,
			FreeBytes:      usage.freeBytes,
			AvailableBytes: usage.availableBytes,
			InodesTotal:    usage.inodesTotal,
			InodesFree:     usage.inodesFree,
			ReadOnly:       hasOption(mount.options, "ro"),
			UsedBytes:      usage.totalBytes - usage.freeBytes,
			InodesUsed:     usage.inodesTotal - usage.inodesFree,
		}
		fs.UsedPercent = percent(fs.UsedBytes, fs.TotalBytes)
		fs.InodesUsedPercent = percent(fs.InodesUsed, fs.InodesTotal)

		// A filesystem with no capacity is a placeholder mount; reporting it
		// as 0% used is misleading, so it is left out.
		if fs.TotalBytes == 0 {
			continue
		}

		seen[mount.device] = struct{}{}
		stats.Filesystems = append(stats.Filesystems, fs)
		stats.TotalBytes += fs.TotalBytes
		stats.UsedBytes += fs.UsedBytes
	}

	if mountPoint != "" && len(stats.Filesystems) == 0 {
		return DiskStats{}, fmt.Errorf("%w: %s is not a mounted filesystem", ErrNotMounted, mountPoint)
	}

	sort.Slice(stats.Filesystems, func(i, j int) bool {
		return stats.Filesystems[i].MountPoint < stats.Filesystems[j].MountPoint
	})
	return stats, nil
}

// mounts parses /proc/mounts.
func (c *Collector) mounts() ([]mountEntry, error) {
	path, err := c.procPath("mounts")
	if err != nil {
		return nil, err
	}

	lines, err := readLines(path)
	if err != nil {
		return nil, fmt.Errorf("%w: /proc/mounts is unreadable", ErrUnsupported)
	}

	entries := make([]mountEntry, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		entries = append(entries, mountEntry{
			// The kernel escapes spaces and other characters as octal codes.
			device:     unescapeMount(fields[0]),
			mountPoint: unescapeMount(fields[1]),
			fsType:     fields[2],
			options:    strings.Split(fields[3], ","),
		})
	}
	return entries, nil
}

func hasOption(options []string, want string) bool {
	for _, option := range options {
		if option == want {
			return true
		}
	}
	return false
}

// unescapeMount decodes the octal escapes the kernel writes in /proc/mounts,
// so a mount point containing a space is reported as its real path.
func unescapeMount(value string) string {
	if !strings.Contains(value, `\`) {
		return value
	}

	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' || i+3 >= len(value) {
			out.WriteByte(value[i])
			continue
		}
		decoded, err := parseOctalTriple(value[i+1 : i+4])
		if err != nil {
			out.WriteByte(value[i])
			continue
		}
		out.WriteByte(decoded)
		i += 3
	}
	return out.String()
}

func parseOctalTriple(triple string) (byte, error) {
	var value byte
	for i := 0; i < len(triple); i++ {
		digit := triple[i]
		if digit < '0' || digit > '7' {
			return 0, fmt.Errorf("not an octal digit")
		}
		value = value*8 + (digit - '0')
	}
	return value, nil
}
