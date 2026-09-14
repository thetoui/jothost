// Package fsperm creates files and directories with exactly the mode asked
// for.
//
// The Agent runs with umask 0077, and the umask filters the mode given to
// os.MkdirAll, os.Mkdir, os.WriteFile and os.OpenFile: ask for 0755 and the
// directory is 0700. That is harmless for anything only root reads, and it is
// a broken host for everything an unprivileged process has to reach. It has
// already produced a 403 on every new website (the placeholder page), cron jobs
// that fired and did nothing (their log directory), and it is the reason this
// package exists rather than one more explicit chmod beside one more call.
//
// The rule: where a mode matters to someone other than root, the mode on disk
// is stated, not requested.
package fsperm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// MkdirAll creates path and any missing parents, and leaves every directory
// it created at exactly mode.
//
// Directories that already existed are not touched. An operator or a
// deployment may have set them deliberately, and this is called on paths the
// panel does not own outright. A caller that must also repair an existing
// directory says so with an explicit os.Chmod.
func MkdirAll(path string, mode os.FileMode) error {
	created, err := missing(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	// Outermost first, so a parent is traversable before its child is set.
	for i := len(created) - 1; i >= 0; i-- {
		if err := os.Chmod(created[i], mode); err != nil {
			return fmt.Errorf("set the mode of %s: %w", created[i], err)
		}
	}
	return nil
}

// Mkdir creates one directory at exactly mode.
func Mkdir(path string, mode os.FileMode) error {
	if err := os.Mkdir(path, mode); err != nil {
		return err
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("set the mode of %s: %w", path, err)
	}
	return nil
}

// SetCreated gives a file this process just created exactly mode, through
// its open handle. Through the handle rather than the path, so nothing can
// swap what the path names between the open and the chmod.
func SetCreated(file *os.File, mode os.FileMode) error {
	if err := file.Chmod(mode); err != nil {
		return fmt.Errorf("set the mode of %s: %w", file.Name(), err)
	}
	return nil
}

// missing lists the directories MkdirAll would have to create for path,
// innermost first.
func missing(path string) ([]string, error) {
	var created []string
	current := filepath.Clean(path)
	for {
		_, err := os.Lstat(current)
		if err == nil {
			return created, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("inspect %s: %w", current, err)
		}
		created = append(created, current)
		parent := filepath.Dir(current)
		if parent == current {
			return created, nil
		}
		current = parent
	}
}
