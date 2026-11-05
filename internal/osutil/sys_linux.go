package osutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/yuuki/droot/internal/log"
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

// ClearInheritableCapabilities empties the inheritable and ambient
// capability sets of the calling thread. The bounding set only limits what
// file capabilities and setuid-root programs can grant: capabilities in the
// inheritable set (e.g. set by pam_cap or systemd's CapabilityInheritable=)
// survive execve(2) for root regardless of the bounding set, and an
// unprivileged user gains them by executing a file with +i capabilities.
//
// Like DropBoundingCapabilities, it must run on the thread that execs.
func ClearInheritableCapabilities() error {
	// PR_CAP_AMBIENT is not supported before Linux 4.3 (EINVAL).
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil && !errors.Is(err, unix.EINVAL) {
		return fmt.Errorf("prctl(PR_CAP_AMBIENT_CLEAR_ALL): %w", err)
	}
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		return fmt.Errorf("capget: %w", err)
	}
	if data[0].Inheritable == 0 && data[1].Inheritable == 0 {
		return nil
	}
	data[0].Inheritable, data[1].Inheritable = 0, 0
	if err := unix.Capset(&hdr, &data[0]); err != nil {
		return fmt.Errorf("capset: %w", err)
	}
	return nil
}

// SetNoNewPrivs sets the no_new_privs flag: setuid/setgid programs and file
// capabilities can no longer grant privileges to the programs executed next.
func SetNoNewPrivs() error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl(PR_SET_NO_NEW_PRIVS): %w", err)
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
// that order. If groups is nil the supplementary groups are left unchanged,
// and so they are in a user namespace that denies setgroups(2) (rootless
// mode without subordinate group IDs), where droot has no supplementary
// groups.
//
// Since Go 1.16 these syscall wrappers apply to all threads of the process.
func SetCredentials(uid, gid int, groups []int) error {
	if groups != nil && !SetgroupsAllowed() {
		log.Debugf("not setting the supplementary groups %v: setgroups is denied in this user namespace", groups)
		groups = nil
	}
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

// UnshareMountNamespace moves the calling thread into a new mount namespace,
// a copy of the current one in which every mount is made a slave of the
// host's: mounts made from now on never propagate back to the host, and
// they disappear when the last process of the namespace exits.
//
// The new namespace is the calling thread's only: the caller must have
// locked its goroutine to the thread (runtime.LockOSThread), must do every
// mount and the chroot from it, and must exec from it.
func UnshareMountNamespace() error {
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return fmt.Errorf("unshare the mount namespace: %w", err)
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_SLAVE, ""); err != nil {
		return fmt.Errorf("make the mounts of the new namespace slaves: %w", err)
	}
	return nil
}

// UnshareIPCNamespace moves the calling thread into a new IPC namespace
// (System V IPC objects and POSIX message queues). Like
// UnshareMountNamespace, it must be called from a locked thread that execs.
func UnshareIPCNamespace() error {
	if err := unix.Unshare(unix.CLONE_NEWIPC); err != nil {
		return fmt.Errorf("unshare the IPC namespace: %w", err)
	}
	return nil
}

// SetHostname moves the calling thread into a new UTS namespace and sets its
// host name, leaving the host's unchanged. Like UnshareMountNamespace, it
// must be called from a locked thread that execs.
func SetHostname(name string) error {
	if err := unix.Unshare(unix.CLONE_NEWUTS); err != nil {
		return fmt.Errorf("unshare the UTS namespace: %w", err)
	}
	if err := unix.Sethostname([]byte(name)); err != nil {
		return fmt.Errorf("set the host name: %w", err)
	}
	return nil
}
