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
	"syscall"
	"time"

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

// maxSymlinks is the maximum number of symlinks SecureJoin follows, like
// Linux's MAXSYMLINKS.
const maxSymlinks = 40

// SecureJoin joins unsafePath to root and resolves symlinks as if root were
// the file system root ("/"), so that the result is always inside root even if
// the tree under root contains malicious symlinks such as /etc -> /host/etc
// or ../../... Components that do not exist are treated as plain directories.
//
// This mirrors github.com/cyphar/filepath-securejoin. Like that package it is
// subject to races if the tree under root is modified concurrently.
func SecureJoin(root, unsafePath string) (string, error) {
	root = filepath.Clean(root)
	resolved := "/" // the resolved path, relative to root
	remaining := filepath.ToSlash(unsafePath)
	followed := 0

	for remaining != "" {
		var part string
		part, remaining, _ = strings.Cut(remaining, "/")
		switch part {
		case "", ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}

		next := filepath.Join(resolved, part)
		fi, err := os.Lstat(filepath.Join(root, next))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				resolved = next
				continue
			}
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			resolved = next
			continue
		}

		followed++
		if followed > maxSymlinks {
			return "", &fs.PathError{Op: "securejoin", Path: unsafePath, Err: syscall.ELOOP}
		}
		dest, err := os.Readlink(filepath.Join(root, next))
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(dest) {
			resolved = "/"
		}
		// A relative target is resolved against the symlink's directory,
		// which is the current value of resolved.
		remaining = filepath.ToSlash(dest) + "/" + remaining
	}
	return filepath.Join(root, resolved), nil
}

// CopyFile copies the regular file src (following symlinks) to dst,
// preserving its permission bits, ownership (when possible) and modification
// time, like `cp -p`. dst is replaced atomically: it is written to a temporary
// file in the same directory and then renamed, so a symlink at dst is replaced
// rather than followed.
func CopyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	fi, err := in.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}

	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".droot-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()

	if _, err := io.Copy(tmp, in); err != nil {
		return err
	}
	if err := tmp.Chmod(fi.Mode().Perm()); err != nil {
		return err
	}
	if uid, gid, ok := fileOwner(fi); ok {
		if err := tmp.Chown(uid, gid); err != nil && !errors.Is(err, fs.ErrPermission) {
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chtimes(tmp.Name(), time.Time{}, fi.ModTime()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
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
