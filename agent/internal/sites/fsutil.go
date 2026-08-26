package sites

import (
	"os"
	"strings"
)

// isRegularFile reports whether a path is a regular file.
func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// trimSpace is a thin alias so this package does not import strings in three
// files for one call each.
func trimSpace(value string) string { return strings.TrimSpace(value) }
