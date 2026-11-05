// Package osutil contains file system and process helpers used by droot.
package osutil

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/yuuki/droot/log"
)

// ExistsFile reports whether path exists and is not a directory.
func ExistsFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// ExistsDir reports whether path exists and is a directory.
func ExistsDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// IsDirEmpty reports whether dir is a readable, empty directory.
func IsDirEmpty(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		log.Debugf("failed to open %s: %v", dir, err)
		return false
	}
	defer f.Close()

	_, err = f.Readdirnames(1)
	return errors.Is(err, io.EOF)
}

// DefaultPath is the search path used when the environment has no PATH.
const DefaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// LookPath searches for an executable named file in the directories of
// pathEnv (a PATH value), using isExecutable to check candidates. Unlike
// exec.LookPath it does not consult the PATH of the current process, which
// matters after chroot(2) where the caller's PATH is meaningless.
// Relative directories in pathEnv are ignored for safety.
func LookPath(file, pathEnv string, isExecutable func(string) error) (string, error) {
	if strings.Contains(file, "/") {
		if err := isExecutable(file); err != nil {
			return "", &fs.PathError{Op: "exec", Path: file, Err: err}
		}
		return file, nil
	}
	if pathEnv == "" {
		pathEnv = DefaultPath
	}
	var firstErr error
	for _, dir := range filepath.SplitList(pathEnv) {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, file)
		err := isExecutable(candidate)
		if err == nil {
			return candidate, nil
		}
		if firstErr == nil && !errors.Is(err, fs.ErrNotExist) {
			firstErr = &fs.PathError{Op: "exec", Path: candidate, Err: err}
		}
	}
	if firstErr != nil {
		return "", firstErr
	}
	return "", fmt.Errorf("%s: executable file not found in PATH (%s)", file, pathEnv)
}
