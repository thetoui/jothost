package command

import (
	"io/fs"
	"os"
)

// statFile is a thin indirection over os.Stat so tests can exercise the
// allowlist without depending on which binaries the host happens to have.
var statFile = func(path string) (fs.FileInfo, error) {
	return os.Stat(path)
}
