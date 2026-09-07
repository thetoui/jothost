package sites

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
)

// isRegularFile reports whether a path is a regular file.
func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// trimSpace is a thin alias so this package does not import strings in three
// files for one call each.
func trimSpace(value string) string { return strings.TrimSpace(value) }

// ownerOf reports the uid that owns a stat result.
//
// The second return is false where the platform does not carry one, which is
// every non-Unix build. The Agent only runs on Linux, but the tests build
// everywhere and a type assertion that panics in CI is not worth the brevity.
func ownerOf(info os.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(stat.Uid), true
}

// ownerAndGroupOf reports both halves of a stat result's ownership.
//
// A directory created for a site has to carry the group as well: the web
// server reaches a site's files through the group, and a directory owned by
// the right user in the wrong group is one nginx cannot read.
func ownerAndGroupOf(info os.FileInfo) (int, int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(stat.Uid), int(stat.Gid), true
}

// isEmptyDir reports whether a directory holds no entries beyond those named
// in ignore.
//
// With nothing ignored it stops after one entry: the answer needs a single
// name, and a document root with fifty thousand files in it should not be
// enumerated to find that out. With names to ignore it has to read enough to
// get past them, so it reads in batches rather than all at once — a directory
// holding only "public" and "logs" costs one batch, and a directory holding a
// customer's whole site stops at the first name that is not one of them.
func isEmptyDir(path string, ignore ...string) (bool, error) {
	dir, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", path, err)
	}
	defer dir.Close()

	skip := make(map[string]struct{}, len(ignore))
	for _, name := range ignore {
		skip[name] = struct{}{}
	}

	for {
		names, err := dir.Readdirnames(readdirBatch)
		for _, name := range names {
			if _, ignored := skip[name]; !ignored {
				return false, nil
			}
		}
		if errors.Is(err, io.EOF) {
			return true, nil
		}
		if err != nil {
			return false, fmt.Errorf("read %s: %w", path, err)
		}
		if len(names) == 0 {
			return true, nil
		}
	}
}

// readdirBatch is how many names are read at a time when looking for the first
// entry that is not ignored.
const readdirBatch = 16
