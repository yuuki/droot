package osutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/yuuki/droot/log"
)

// Chroot changes the root directory of the process to rootDir and changes the
// working directory to the new root.
func Chroot(rootDir string) error {
	log.Debugf("chroot %s", rootDir)
	if err := unix.Chroot(rootDir); err != nil {
		return &fs.PathError{Op: "chroot", Path: rootDir, Err: err}
	}
	return unix.Chdir("/")
}

// MknodChar makes sure path is a character device with the given numbers and
// permission bits. An existing file of another kind at path is replaced.
func MknodChar(path string, major, minor uint32, perm os.FileMode) error {
	dev := unix.Mkdev(major, minor)
	var st unix.Stat_t
	err := unix.Lstat(path, &st)
	switch {
	case err == nil && st.Mode&unix.S_IFMT == unix.S_IFCHR && st.Rdev == dev:
		// Already there.
	case err == nil:
		log.Debugf("replacing %s with a character device", path)
		if err := os.Remove(path); err != nil {
			return err
		}
		fallthrough
	case errors.Is(err, unix.ENOENT):
		log.Debugf("mknod %s c %d %d", path, major, minor)
		if err := unix.Mknod(path, unix.S_IFCHR|uint32(perm.Perm()), int(dev)); err != nil {
			return &fs.PathError{Op: "mknod", Path: path, Err: err}
		}
	default:
		return &fs.PathError{Op: "lstat", Path: path, Err: err}
	}
	// mknod(2) is subject to the umask, so set the mode explicitly.
	return os.Chmod(path, perm)
}

// DropBoundingCapabilities removes every capability not listed in keep from
// the capability bounding set, so that the program executed next can never
// gain them, even as root or through setuid binaries.
//
// The bounding set is a per-thread attribute: the caller must have locked the
// goroutine to its OS thread (runtime.LockOSThread) and must exec from the
// same goroutine.
func DropBoundingCapabilities(keep map[uintptr]bool) error {
	last, err := lastCap()
	if err != nil {
		return err
	}
	for c := uintptr(0); c <= last; c++ {
		if keep[c] {
			continue
		}
		if err := unix.Prctl(unix.PR_CAPBSET_DROP, c, 0, 0, 0); err != nil {
			switch {
			case errors.Is(err, unix.EINVAL):
				continue // not supported by this kernel
			case errors.Is(err, unix.EPERM):
				return errors.New("dropping capabilities requires CAP_SETPCAP (run droot as root)")
			default:
				return fmt.Errorf("prctl(PR_CAPBSET_DROP, %d): %w", c, err)
			}
		}
		log.Debugf("dropped capability %d from the bounding set", c)
	}
	return nil
}

// lastCap returns the highest capability number supported by the kernel.
func lastCap() (uintptr, error) {
	// Probe with PR_CAPBSET_READ, which fails with EINVAL past the last
	// capability. This works even without /proc mounted.
	var c uintptr
	for ; c < 1024; c++ {
		if _, err := unix.PrctlRetInt(unix.PR_CAPBSET_READ, c, 0, 0, 0); err != nil {
			if errors.Is(err, unix.EINVAL) {
				break
			}
			return 0, fmt.Errorf("prctl(PR_CAPBSET_READ, %d): %w", c, err)
		}
	}
	if c == 0 {
		return 0, errors.New("cannot determine the capabilities supported by the kernel")
	}
	return c - 1, nil
}

// SetCredentials switches the process to the given groups, gid and uid, in
// that order. If groups is nil the supplementary groups are left unchanged.
//
// Since Go 1.16 these syscall wrappers apply to all threads of the process.
func SetCredentials(uid, gid int, groups []int) error {
	if groups != nil {
		if err := syscall.Setgroups(groups); err != nil {
			return fmt.Errorf("setgroups(%v): %w", groups, err)
		}
	}
	if err := syscall.Setgid(gid); err != nil {
		return fmt.Errorf("setgid(%d): %w", gid, err)
	}
	if err := syscall.Setuid(uid); err != nil {
		return fmt.Errorf("setuid(%d): %w", uid, err)
	}
	return nil
}

// IsExecutable reports whether path is a regular file that the current
// (real) user may execute.
func IsExecutable(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return syscall.EISDIR
	}
	return unix.Access(path, unix.X_OK)
}

// Exec replaces the current process with argv0 (already resolved with
// LookPath).
func Exec(argv0 string, argv, env []string) error {
	log.Debugf("exec %s %q", argv0, argv)
	err := syscall.Exec(argv0, argv, env)
	if errors.Is(err, syscall.ENOENT) {
		// LookPath found the file, so what is missing is its interpreter.
		return fmt.Errorf("exec %s: %w (the file exists: its ELF interpreter/dynamic loader or "+
			"its #! interpreter is probably missing in the container)", argv0, err)
	}
	if err != nil {
		return fmt.Errorf("exec %s: %w", argv0, err)
	}
	return nil
}
