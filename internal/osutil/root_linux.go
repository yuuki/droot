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

	"github.com/yuuki/droot/internal/log"
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
	// within, if not nil, is also called, after check, with the
	// directories in which a missing component is created, and only
	// with them.
	within DirCheck
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
	w := &walker{root: r, opt: opt, path: unsafePath, parts: strings.Split(filepath.ToSlash(unsafePath), "/")}
	defer func() {
		for _, fd := range w.stack {
			if fd != res.dir || err != nil {
				_ = unix.Close(fd)
			}
		}
	}()

	for len(w.parts) > 0 {
		part := w.parts[0]
		w.parts = w.parts[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			if len(w.stack) > 0 {
				_ = unix.Close(w.stack[len(w.stack)-1])
				w.stack = w.stack[:len(w.stack)-1]
			}
			continue
		}
		last := len(w.parts) == 0 || allEmpty(w.parts)

		end, err := w.step(part, last)
		if err != nil {
			return w.fail(err)
		}
		if end {
			return w.keep(part)
		}
	}
	// The path resolved to a directory reached through "", "." or "..".
	if len(w.stack) == 0 {
		return w.keep("")
	}
	dir := w.stack[len(w.stack)-1]
	w.stack = w.stack[:len(w.stack)-1]
	return resolved{dir: dir}, nil
}

// walker is a lookup in progress.
type walker struct {
	root  *Root
	opt   lookupOptions
	path  string   // the path looked up, for errors
	parts []string // the components left to resolve
	stack []int    // directories below the root, deepest last
	// followed is the number of symlinks followed so far.
	followed int
}

// cur returns the directory the next component is resolved in.
func (w *walker) cur() int {
	if len(w.stack) == 0 {
		return w.root.fd
	}
	return w.stack[len(w.stack)-1]
}

// keep ends the lookup with name in the current directory.
func (w *walker) keep(name string) (resolved, error) {
	dir := w.cur()
	if dir == w.root.fd {
		d, err := unix.FcntlInt(uintptr(w.root.fd), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return resolved{}, err
		}
		return resolved{dir: d, name: name, isRoot: name == ""}, nil
	}
	return resolved{dir: dir, name: name}, nil
}

// fail ends the lookup with the error e.
func (w *walker) fail(e error) (resolved, error) {
	return resolved{dir: -1}, &fs.PathError{Op: "resolve", Path: w.root.displayPath(w.path), Err: e}
}

// step resolves part, the next component of the path (the last one if
// last), in the current directory: it enters a directory, follows a symlink
// or creates a missing directory, as opt says. It returns true when the
// lookup ends with part.
func (w *walker) step(part string, last bool) (bool, error) {
	fd, err := unix.Openat(w.cur(), part, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	switch {
	case errors.Is(err, unix.ENOENT) && last:
		return true, nil
	case errors.Is(err, unix.ENOENT) && w.opt.mkdir:
		fd, err = w.mkdir(part)
	}
	if err != nil {
		return false, err
	}

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return false, err
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		if last && !w.opt.followLast {
			_ = unix.Close(fd)
			return true, nil
		}
		return false, w.follow(fd, part)
	case unix.S_IFDIR:
		if last {
			_ = unix.Close(fd)
			return true, nil
		}
		return false, w.enter(fd)
	default:
		_ = unix.Close(fd)
		if last {
			return true, nil
		}
		return false, unix.ENOTDIR
	}
}

// mkdir creates the missing directory part (0755) in the current directory,
// after opt.check and opt.within, and opens it. A directory created
// concurrently is opened as it is.
func (w *walker) mkdir(part string) (int, error) {
	if err := checkAll(w.cur(), w.opt.check, w.opt.within); err != nil {
		return -1, err
	}
	mkErr := unix.Mkdirat(w.cur(), part, 0o755)
	if mkErr != nil && !errors.Is(mkErr, unix.EEXIST) {
		return -1, mkErr
	}
	fd, err := unix.Openat(w.cur(), part, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == nil && mkErr == nil {
		if err := setCreatedMode(fd, true, 0o755); err != nil {
			_ = unix.Close(fd)
			return -1, err
		}
	}
	return fd, err
}

// follow follows the symlink part, open as fd, which it closes: the
// components of its target are resolved next, from the root if it is
// absolute.
func (w *walker) follow(fd int, part string) error {
	if w.opt.noSymlinks {
		_ = unix.Close(fd)
		return fmt.Errorf("unexpected symbolic link %q", part)
	}
	w.followed++
	if w.followed > maxSymlinks {
		_ = unix.Close(fd)
		return unix.ELOOP
	}
	target, err := readlinkFd(fd)
	_ = unix.Close(fd)
	if err != nil {
		return err
	}
	if strings.HasPrefix(target, "/") {
		for _, d := range w.stack {
			_ = unix.Close(d)
		}
		w.stack = w.stack[:0]
	}
	w.parts = append(strings.Split(target, "/"), w.parts...)
	return nil
}

// enter makes the directory open as fd, after opt.check, the directory the
// next component is resolved in. It closes fd on failure.
func (w *walker) enter(fd int) error {
	if w.opt.check != nil {
		if err := w.opt.check(fd); err != nil {
			_ = unix.Close(fd)
			return err
		}
	}
	w.stack = append(w.stack, fd)
	return nil
}

// checkAll calls each check that is not nil with fd, stopping at the first
// error.
func checkAll(fd int, checks ...DirCheck) error {
	for _, check := range checks {
		if check == nil {
			continue
		}
		if err := check(fd); err != nil {
			return err
		}
	}
	return nil
}

func allEmpty(parts []string) bool {
	for _, p := range parts {
		if p != "" && p != "." {
			return false
		}
	}
	return true
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

// setCreatedMode gives the directory (dir) or file droot has just created,
// open as fd (O_PATH), the mode perm: mkdir(2) and open(2) are subject to
// the umask, and a unit's UMask=0077 would otherwise make the mount points
// unreachable by the users of the container. chmod(2) follows symlinks, so
// it goes through the descriptor, and only if fd is still of the kind
// created (a concurrent replacement is left to the checks of the caller).
func setCreatedMode(fd int, dir bool, perm uint32) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if kind := st.Mode & unix.S_IFMT; (kind == unix.S_IFDIR) != dir || kind == unix.S_IFLNK || st.Mode&0o7777 == perm {
		return nil
	}
	return unix.Chmod(FdPath(fd), perm)
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
	return r.OpenMountTargetWithin(path, dir, check, nil)
}

// OpenMountTargetWithin is OpenMountTarget, except that the target and its
// missing parent directories are only created in directories that within,
// if not nil, accepts too: e.g. only on the root's own file system, not in
// a directory bound from the host. Unlike check, within is not called with
// the directories that are only traversed, so a target that exists is
// opened wherever it is.
func (r *Root) OpenMountTargetWithin(path string, dir bool, check, within DirCheck) (*MountTarget, error) {
	res, err := r.lookup(path, lookupOptions{mkdir: true, followLast: true, check: check, within: within})
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
		var op string
		if fd, op, err = createMountTarget(res.dir, res.name, dir, check, within); err != nil {
			return fail(op, err)
		}
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

// createMountTarget creates the missing mount target name in the directory
// parent, after checks: a directory (dir) or an empty regular file. A target
// created concurrently is kept as it is. It returns an O_PATH descriptor of
// the target, or else the operation that failed and its error.
func createMountTarget(parent int, name string, dir bool, checks ...DirCheck) (fd int, op string, err error) {
	if err := checkAll(parent, checks...); err != nil {
		return -1, "mount", err
	}
	if dir {
		err = unix.Mkdirat(parent, name, 0o755)
	} else {
		var f int
		f, err = unix.Openat(parent, name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644)
		if err == nil {
			err = unix.Fchmod(f, 0o644) // not subject to the umask
			_ = unix.Close(f)
			if err != nil {
				return -1, "chmod", err
			}
		}
	}
	created := err == nil
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return -1, "create", err
	}
	fd, err = unix.Openat(parent, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, "open", err
	}
	if created && dir {
		if err := setCreatedMode(fd, true, 0o755); err != nil {
			_ = unix.Close(fd)
			return -1, "chmod", err
		}
	}
	return fd, "", nil
}

// OpenNoFollow returns an O_PATH descriptor of path without following any
// symlink: a symlink anywhere in path is an error. Mount points are crossed,
// so the descriptor refers to the top-most mount at path, if any.
func (r *Root) OpenNoFollow(path string) (*os.File, error) {
	res, err := r.lookup(path, lookupOptions{noSymlinks: true})
	if err != nil {
		return nil, err
	}
	if res.name == "" {
		return os.NewFile(uintptr(res.dir), r.displayPath(path)), nil
	}
	defer unix.Close(res.dir)
	fd, err := unix.Openat(res.dir, res.name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: r.displayPath(path), Err: err}
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT == unix.S_IFLNK {
		_ = unix.Close(fd)
		if err == nil {
			err = fmt.Errorf("unexpected symbolic link %q", res.name)
		}
		return nil, &fs.PathError{Op: "open", Path: r.displayPath(path), Err: err}
	}
	return os.NewFile(uintptr(fd), r.displayPath(path)), nil
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

// EnsureSymlink makes sure path is the symbolic link path -> target: a
// symlink with another target or a file of another kind (but a directory)
// at path is replaced. Missing parent directories are created. See DirCheck
// for check; it is also applied to the directory the link is created in.
func (r *Root) EnsureSymlink(target, path string, check DirCheck) error {
	res, err := r.lookup(path, lookupOptions{mkdir: true, check: check})
	if err != nil {
		return err
	}
	defer unix.Close(res.dir)
	if res.name == "" {
		return &fs.PathError{Op: "symlink", Path: r.displayPath(path), Err: unix.EISDIR}
	}
	if check != nil {
		if err := check(res.dir); err != nil {
			return &fs.PathError{Op: "symlink", Path: r.displayPath(path), Err: err}
		}
	}
	var st unix.Stat_t
	err = unix.Fstatat(res.dir, res.name, &st, unix.AT_SYMLINK_NOFOLLOW)
	switch {
	case err == nil && st.Mode&unix.S_IFMT == unix.S_IFLNK:
		fd, err := unix.Openat(res.dir, res.name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return &fs.PathError{Op: "open", Path: r.displayPath(path), Err: err}
		}
		cur, err := readlinkFd(fd)
		_ = unix.Close(fd)
		if err == nil && cur == target {
			return nil
		}
		fallthrough
	case err == nil && st.Mode&unix.S_IFMT != unix.S_IFDIR:
		log.Debugf("replacing %s with a symlink to %s", r.displayPath(path), target)
		if err := unix.Unlinkat(res.dir, res.name, 0); err != nil {
			return &fs.PathError{Op: "unlink", Path: r.displayPath(path), Err: err}
		}
	case err == nil:
		return &fs.PathError{Op: "symlink", Path: r.displayPath(path), Err: unix.EISDIR}
	case !errors.Is(err, unix.ENOENT):
		return &fs.PathError{Op: "stat", Path: r.displayPath(path), Err: err}
	}
	if err := unix.Symlinkat(target, res.dir, res.name); err != nil {
		return &fs.PathError{Op: "symlink", Path: r.displayPath(path), Err: err}
	}
	return nil
}

// SymlinkIfMissing creates the symbolic link path -> target unless something
// already exists at path. The parent directory must exist. See DirCheck for
// check; it is also applied to the directory the link is created in.
func (r *Root) SymlinkIfMissing(target, path string, check DirCheck) error {
	res, err := r.lookup(path, lookupOptions{check: check})
	if err != nil {
		return err
	}
	defer unix.Close(res.dir)
	if res.name == "" {
		return nil
	}
	if check != nil {
		if err := check(res.dir); err != nil {
			return &fs.PathError{Op: "symlink", Path: r.displayPath(path), Err: err}
		}
	}
	if err := unix.Symlinkat(target, res.dir, res.name); err != nil && !errors.Is(err, unix.EEXIST) {
		return &fs.PathError{Op: "symlink", Path: r.displayPath(path), Err: err}
	}
	return nil
}

// CopyFileIn copies the host's regular file src (following symlinks on the
// host) to path inside the root, preserving its permission bits, ownership
// and modification time, like `cp -p`. See DirCheck for check; it is also
// applied to the directory the file is written in. Symlinks in path are followed inside
// the root, and the destination is replaced atomically with rename(2), so a
// symlink swapped in at the last moment is replaced rather than followed.
func (r *Root) CopyFileIn(src, path string, check DirCheck) (err error) {
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

	res, err := r.lookup(path, lookupOptions{mkdir: true, followLast: true, check: check})
	if err != nil {
		return err
	}
	defer unix.Close(res.dir)
	if res.name == "" {
		return &fs.PathError{Op: "copy", Path: r.displayPath(path), Err: unix.EISDIR}
	}
	if check != nil {
		if err := check(res.dir); err != nil {
			return &fs.PathError{Op: "copy", Path: r.displayPath(path), Err: err}
		}
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
	if uid, gid, _, ok := FileOwner(fi); ok {
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
