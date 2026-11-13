// Package osutil contains file system and process helpers used by droot.
package osutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// CanExecute reports whether the user uid, with the primary group gid and
// the supplementary groups, may execute the file at path, judging from the
// permission bits of the file and of the directories leading to it (ACLs,
// capabilities and mount options are not considered). It lets a privileged
// caller check what will happen once it has switched to that user.
func CanExecute(path string, uid, gid int, groups []int) error {
	allowed := func(fi fs.FileInfo, bit fs.FileMode) bool { return permits(fi, bit, uid, gid, groups) }
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for d := filepath.Dir(abs); ; d = filepath.Dir(d) {
		fi, err := os.Stat(d)
		if err != nil {
			return err
		}
		if !allowed(fi, 0o1) {
			return &fs.PathError{Op: "exec", Path: path, Err: fmt.Errorf("permission denied: uid %d cannot search %s", uid, d)}
		}
		if d == "/" || d == "." {
			break
		}
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return err
	}
	return CanExecuteFile(path, fi, uid, gid, groups)
}

// CanExecuteFile is CanExecute for the file at path whose information is
// fi, judging from its permission bits only.
func CanExecuteFile(path string, fi fs.FileInfo, uid, gid int, groups []int) error {
	if fi.IsDir() || !fi.Mode().IsRegular() {
		return &fs.PathError{Op: "exec", Path: path, Err: errors.New("not a regular file")}
	}
	if !permits(fi, 0o1, uid, gid, groups) {
		return &fs.PathError{Op: "exec", Path: path, Err: fmt.Errorf("permission denied for uid %d", uid)}
	}
	return nil
}

// CanSearch reports whether the user uid, with the primary group gid and
// the supplementary groups, may enter the directory whose information is
// fi, judging from its permission bits.
func CanSearch(fi fs.FileInfo, uid, gid int, groups []int) bool {
	return fi.IsDir() && permits(fi, 0o1, uid, gid, groups)
}

// permits reports whether the permission bit (0o1 execute or search, 0o2
// write, 0o4 read) of the file fi is granted to the user uid, with the
// primary group gid and the supplementary groups. Root needs an execute
// bit to execute a file, like execve(2).
func permits(fi fs.FileInfo, bit fs.FileMode, uid, gid int, groups []int) bool {
	mode := fi.Mode().Perm()
	if uid == 0 {
		return mode&0o111 != 0 || fi.IsDir()
	}
	owner, group, _, ok := FileOwner(fi)
	switch {
	case !ok:
		return true // unknown: let execve(2) decide
	case owner == uid:
		return mode&(bit<<6) != 0
	case group == gid || slices.Contains(groups, group):
		return mode&(bit<<3) != 0
	}
	return mode&bit != 0
}
