package osutil

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/yuuki/droot/log"
)

// maxSymlinks is the maximum number of symlinks followed while resolving a
// path, like Linux's MAXSYMLINKS.
const maxSymlinks = 40

// ErrIsRoot is returned when a path resolves to the root directory itself
// where a path below it is required.
var ErrIsRoot = errors.New("path resolves to the container root directory itself")

// Root gives race-free access to a directory tree that untrusted processes
// may modify concurrently, such as the root directory of a container that is
// still running from a previous `droot run`.
//
// Paths are resolved component by component with openat(2) and O_NOFOLLOW,
// starting from a file descriptor of the root, and symbolic links are followed
// by droot itself as if the root were "/". Every operation is then performed
// relative to a file descriptor of the resolved parent directory, so swapping
// a directory for a symlink at any time cannot redirect an operation outside
// the root. Unlike chroot(2) resolution, ".." can never climb above the root.
type Root struct {
	fd   int
	path string
}

// OpenRoot opens the directory at path as a Root.
func OpenRoot(path string) (*Root, error) {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	return &Root{fd: fd, path: path}, nil
}

// Close releases the root's file descriptor.
func (r *Root) Close() error { return unix.Close(r.fd) }

// Path returns the host path the root was opened with.
func (r *Root) Path() string { return r.path }

// Fd returns the O_PATH file descriptor of the root directory.
func (r *Root) Fd() int { return r.fd }

// FdPath returns a path that refers to the file open as fd, for system calls
// that only accept paths (mount, umount, chmod, statfs). The kernel resolves
// it to the very file the descriptor refers to, not to a path name.
func FdPath(fd int) string { return "/proc/self/fd/" + strconv.Itoa(fd) }

// DirCheck is called with every directory traversed while resolving a path
// (excluding the root and the last component) and with the directory in
// which a missing component is about to be created. Returning an error
// aborts the resolution.
type DirCheck func(fd int) error

type lookupOptions struct {
	mkdir      bool     // create missing intermediate directories (0755)
	followLast bool     // resolve a symlink in the last component
	noSymlinks bool     // fail on any symlink instead of following it
	check      DirCheck // optional
}

// resolved is the result of a lookup: the last component name inside the
// directory dir. If name is empty, the path resolved to dir itself.
type resolved struct {
	dir    int
	name   string
	isRoot bool // name == "" and dir is the root
}

func (r *Root) displayPath(p string) string {
	return filepath.Join(r.path, filepath.Join("/", p))
}

// lookup resolves unsafePath inside the root. The caller must close res.dir.
func (r *Root) lookup(unsafePath string, opt lookupOptions) (res resolved, err error) {
	var stack []int // directories below the root, deepest last
	cur := func() int {
		if len(stack) == 0 {
			return r.fd
		}
		return stack[len(stack)-1]
	}
	defer func() {
		for _, fd := range stack {
			if fd != res.dir || err != nil {
				_ = unix.Close(fd)
			}
		}
	}()
	keep := func(name string) (resolved, error) {
		dir := cur()
		if dir == r.fd {
			d, err := unix.FcntlInt(uintptr(r.fd), unix.F_DUPFD_CLOEXEC, 0)
			if err != nil {
				return resolved{}, err
			}
			return resolved{dir: d, name: name, isRoot: name == ""}, nil
		}
		return resolved{dir: dir, name: name}, nil
	}
	fail := func(e error) (resolved, error) {
		return resolved{dir: -1}, &fs.PathError{Op: "resolve", Path: r.displayPath(unsafePath), Err: e}
	}

	parts := strings.Split(filepath.ToSlash(unsafePath), "/")
	followed := 0
	for len(parts) > 0 {
		part := parts[0]
		parts = parts[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			if len(stack) > 0 {
				_ = unix.Close(stack[len(stack)-1])
				stack = stack[:len(stack)-1]
			}
			continue
		}
		last := len(parts) == 0 || allEmpty(parts)

		fd, err := unix.Openat(cur(), part, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) {
			if last {
				return keep(part)
			}
			if !opt.mkdir {
				return fail(err)
			}
			if opt.check != nil {
				if err := opt.check(cur()); err != nil {
					return fail(err)
				}
			}
			if err := unix.Mkdirat(cur(), part, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
				return fail(err)
			}
			fd, err = unix.Openat(cur(), part, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if err != nil {
			return fail(err)
		}

		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			_ = unix.Close(fd)
			return fail(err)
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFLNK:
			if last && !opt.followLast {
				_ = unix.Close(fd)
				return keep(part)
			}
			if opt.noSymlinks {
				_ = unix.Close(fd)
				return fail(fmt.Errorf("unexpected symbolic link %q", part))
			}
			followed++
			if followed > maxSymlinks {
				_ = unix.Close(fd)
				return fail(unix.ELOOP)
			}
			target, err := readlinkFd(fd)
			_ = unix.Close(fd)
			if err != nil {
				return fail(err)
			}
			if strings.HasPrefix(target, "/") {
				for _, d := range stack {
					_ = unix.Close(d)
				}
				stack = stack[:0]
			}
			parts = append(strings.Split(target, "/"), parts...)
		case unix.S_IFDIR:
			if last {
				_ = unix.Close(fd)
				return keep(part)
			}
			if opt.check != nil {
				if err := opt.check(fd); err != nil {
					_ = unix.Close(fd)
					return fail(err)
				}
			}
			stack = append(stack, fd)
		default:
			_ = unix.Close(fd)
			if last {
				return keep(part)
			}
			return fail(unix.ENOTDIR)
		}
	}
	// The path resolved to a directory reached through "", "." or "..".
	if len(stack) == 0 {
		return keep("")
	}
	dir := stack[len(stack)-1]
	stack = stack[:len(stack)-1]
	return resolved{dir: dir}, nil
}

func allEmpty(parts []string) bool {
	for _, p := range parts {
		if p != "" && p != "." {
			return false
		}
	}
	return true
}

func fileOwner(fi fs.FileInfo) (uid, gid int, ok bool) {
	st, ok := fi.Sys().(*unix.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}

func readlinkFd(fd int) (string, error) {
	for size := 256; size <= 1<<16; size *= 4 {
		buf := make([]byte, size)
		n, err := unix.Readlinkat(fd, "", buf)
		if err != nil {
			return "", err
		}
		if n < size {
			return string(buf[:n]), nil
		}
	}
	return "", unix.ENAMETOOLONG
}

// OpenDir returns an O_PATH descriptor of the directory at path, following
// symlinks inside the root. Missing directories are created (mode 0755) if
// create is set. See DirCheck for check.
func (r *Root) OpenDir(path string, create bool, check DirCheck) (*os.File, error) {
	res, err := r.lookup(path, lookupOptions{mkdir: create, followLast: true, check: check})
	if err != nil {
		return nil, err
	}
	if res.name == "" {
		return os.NewFile(uintptr(res.dir), r.displayPath(path)), nil
	}
	defer unix.Close(res.dir)
	fd, err := unix.Openat(res.dir, res.name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) && create {
		if check != nil {
			if err := check(res.dir); err != nil {
				return nil, &fs.PathError{Op: "mkdir", Path: r.displayPath(path), Err: err}
			}
		}
		if err := unix.Mkdirat(res.dir, res.name, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, &fs.PathError{Op: "mkdir", Path: r.displayPath(path), Err: err}
		}
		fd, err = unix.Openat(res.dir, res.name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: r.displayPath(path), Err: err}
	}
	return os.NewFile(uintptr(fd), r.displayPath(path)), nil
}

// MountTarget is a resolved mount target inside a Root.
type MountTarget struct {
	// File is an O_PATH descriptor of the target as resolved, to be used
	// through FdPath as the target of mount(2).
	File   *os.File
	parent int
	name   string
	dir    bool
}

// ParentFd returns an O_PATH descriptor of the directory containing the
// target. It stays valid until Close.
func (t *MountTarget) ParentFd() int { return t.parent }

// Close releases the descriptors of the target.
func (t *MountTarget) Close() error {
	_ = unix.Close(t.parent)
	return t.File.Close()
}

// Reopen opens the target again from its parent directory, which crosses
// into whatever is mounted on it now (e.g. the mount just made on File). The
// parent is not resolved again, so the result cannot be redirected.
func (t *MountTarget) Reopen() (*os.File, error) {
	fd, err := unix.Openat(t.parent, t.name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: t.File.Name(), Err: err}
	}
	if err := verifyTarget(fd, t.dir); err != nil {
		_ = unix.Close(fd)
		return nil, &fs.PathError{Op: "open", Path: t.File.Name(), Err: err}
	}
	return os.NewFile(uintptr(fd), t.File.Name()), nil
}

func verifyTarget(fd int, dir bool) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	switch mode := st.Mode & unix.S_IFMT; {
	case mode == unix.S_IFLNK:
		return unix.ELOOP // replaced by a symlink concurrently
	case dir && mode != unix.S_IFDIR:
		return unix.ENOTDIR
	case !dir && mode == unix.S_IFDIR:
		return unix.EISDIR
	}
	return nil
}

// OpenMountTarget resolves a mount target. Symlinks are followed inside the
// root. A missing target is created as a directory (dir) or as an empty
// regular file. A path resolving to the root itself is refused with
// ErrIsRoot, and so is a path ending in "..". See DirCheck for check.
func (r *Root) OpenMountTarget(path string, dir bool, check DirCheck) (*MountTarget, error) {
	res, err := r.lookup(path, lookupOptions{mkdir: true, followLast: true, check: check})
	if err != nil {
		return nil, err
	}
	if res.name == "" {
		_ = unix.Close(res.dir)
		if res.isRoot {
			return nil, &fs.PathError{Op: "mount", Path: r.displayPath(path), Err: ErrIsRoot}
		}
		return nil, &fs.PathError{Op: "mount", Path: r.displayPath(path), Err: errors.New(`a mount target must not end with ".."`)}
	}
	fail := func(op string, err error) (*MountTarget, error) {
		_ = unix.Close(res.dir)
		return nil, &fs.PathError{Op: op, Path: r.displayPath(path), Err: err}
	}

	fd, err := unix.Openat(res.dir, res.name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		if check != nil {
			if err := check(res.dir); err != nil {
				return fail("mount", err)
			}
		}
		if dir {
			err = unix.Mkdirat(res.dir, res.name, 0o755)
		} else {
			var f int
			f, err = unix.Openat(res.dir, res.name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644)
			if err == nil {
				_ = unix.Close(f)
			}
		}
		if err != nil && !errors.Is(err, unix.EEXIST) {
			return fail("create", err)
		}
		fd, err = unix.Openat(res.dir, res.name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return fail("open", err)
	}
	if err := verifyTarget(fd, dir); err != nil {
		_ = unix.Close(fd)
		return fail("mount", err)
	}
	return &MountTarget{
		File:   os.NewFile(uintptr(fd), r.displayPath(path)),
		parent: res.dir,
		name:   res.name,
		dir:    dir,
	}, nil
}

// OpenFile opens the regular file at path for reading, following symlinks
// inside the root. FIFOs, devices and other special files are refused
// without blocking.
func (r *Root) OpenFile(path string) (*os.File, error) {
	res, err := r.lookup(path, lookupOptions{followLast: true})
	if err != nil {
		return nil, err
	}
	defer unix.Close(res.dir)
	if res.name == "" {
		return nil, &fs.PathError{Op: "open", Path: r.displayPath(path), Err: unix.EISDIR}
	}
	fd, err := unix.Openat(res.dir, res.name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: r.displayPath(path), Err: err}
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		if err == nil {
			err = errors.New("not a regular file")
		}
		return nil, &fs.PathError{Op: "open", Path: r.displayPath(path), Err: err}
	}
	return os.NewFile(uintptr(fd), r.displayPath(path)), nil
}

// MknodChar makes sure path is a character device with the given numbers
// and permission bits. A file of another kind at path (including a symlink)
// is replaced. Missing parent directories are created. See DirCheck for
// check; it is also applied to the directory the node is created in.
func (r *Root) MknodChar(path string, major, minor uint32, perm os.FileMode, check DirCheck) error {
	res, err := r.lookup(path, lookupOptions{mkdir: true, check: check})
	if err != nil {
		return err
	}
	defer unix.Close(res.dir)
	if res.name == "" {
		return &fs.PathError{Op: "mknod", Path: r.displayPath(path), Err: unix.EISDIR}
	}
	if check != nil {
		if err := check(res.dir); err != nil {
			return &fs.PathError{Op: "mknod", Path: r.displayPath(path), Err: err}
		}
	}

	dev := unix.Mkdev(major, minor)
	var st unix.Stat_t
	err = unix.Fstatat(res.dir, res.name, &st, unix.AT_SYMLINK_NOFOLLOW)
	switch {
	case err == nil && st.Mode&unix.S_IFMT == unix.S_IFCHR && st.Rdev == dev:
		// Already there.
	case err == nil:
		log.Debugf("replacing %s with a character device", r.displayPath(path))
		if err := unix.Unlinkat(res.dir, res.name, 0); err != nil {
			return &fs.PathError{Op: "unlink", Path: r.displayPath(path), Err: err}
		}
		fallthrough
	case errors.Is(err, unix.ENOENT):
		log.Debugf("mknod %s c %d %d", r.displayPath(path), major, minor)
		if err := unix.Mknodat(res.dir, res.name, unix.S_IFCHR|uint32(perm.Perm()), int(dev)); err != nil {
			return &fs.PathError{Op: "mknod", Path: r.displayPath(path), Err: err}
		}
	default:
		return &fs.PathError{Op: "stat", Path: r.displayPath(path), Err: err}
	}

	// mknod(2) is subject to the umask, so set the mode explicitly. chmod
	// follows symlinks, so do it through a descriptor of the node itself.
	fd, err := unix.Openat(res.dir, res.name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return &fs.PathError{Op: "open", Path: r.displayPath(path), Err: err}
	}
	defer unix.Close(fd)
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFCHR || st.Rdev != dev {
		return &fs.PathError{Op: "mknod", Path: r.displayPath(path), Err: errors.New("replaced concurrently")}
	}
	if err := unix.Chmod(FdPath(fd), uint32(perm.Perm())); err != nil {
		return &fs.PathError{Op: "chmod", Path: r.displayPath(path), Err: err}
	}
	return nil
}

// SymlinkIfMissing creates the symbolic link path -> target unless something
// already exists at path. The parent directory must exist.
func (r *Root) SymlinkIfMissing(target, path string) error {
	res, err := r.lookup(path, lookupOptions{})
	if err != nil {
		return err
	}
	defer unix.Close(res.dir)
	if res.name == "" {
		return nil
	}
	if err := unix.Symlinkat(target, res.dir, res.name); err != nil && !errors.Is(err, unix.EEXIST) {
		return &fs.PathError{Op: "symlink", Path: r.displayPath(path), Err: err}
	}
	return nil
}

// CopyFileIn copies the host's regular file src (following symlinks on the
// host) to path inside the root, preserving its permission bits, ownership
// and modification time, like `cp -p`. Symlinks in path are followed inside
// the root, and the destination is replaced atomically with rename(2), so a
// symlink swapped in at the last moment is replaced rather than followed.
func (r *Root) CopyFileIn(src, path string) (err error) {
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

	res, err := r.lookup(path, lookupOptions{mkdir: true, followLast: true})
	if err != nil {
		return err
	}
	defer unix.Close(res.dir)
	if res.name == "" {
		return &fs.PathError{Op: "copy", Path: r.displayPath(path), Err: unix.EISDIR}
	}

	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	tmpName := ".droot-" + hex.EncodeToString(rnd[:]) + ".tmp"
	fd, err := unix.Openat(res.dir, tmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return &fs.PathError{Op: "create", Path: r.displayPath(path), Err: err}
	}
	tmp := os.NewFile(uintptr(fd), tmpName)
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = unix.Unlinkat(res.dir, tmpName, 0)
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
	mtime := unix.NsecToTimeval(fi.ModTime().UnixNano())
	if err := unix.Futimes(fd, []unix.Timeval{unix.NsecToTimeval(time.Now().UnixNano()), mtime}); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(res.dir, tmpName, res.dir, res.name); err != nil {
		return &fs.PathError{Op: "rename", Path: r.displayPath(path), Err: err}
	}
	return nil
}
