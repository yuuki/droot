package mounter

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/yuuki/droot/log"
	"github.com/yuuki/droot/osutil"
)

// keepFile is created in empty bind mount sources (behavior inherited from
// droot <= 0.7).
const keepFile = ".droot.keep"

func readMountInfo() ([]MountInfo, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseMountInfo(f)
}

func isMountpoint(path string) (bool, error) {
	mounts, err := readMountInfo()
	if err != nil {
		return false, err
	}
	for _, m := range mounts {
		if m.Mountpoint == path {
			return true, nil
		}
	}
	return false, nil
}

var errNoMountID = errors.New("mount ID not available")

// mountID returns the ID (as in /proc/self/mountinfo) of the mount the
// descriptor belongs to. It needs Linux 3.15 or later.
func mountID(fd int) (int, error) {
	b, err := os.ReadFile("/proc/self/fdinfo/" + strconv.Itoa(fd))
	if err != nil {
		return 0, err
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "mnt_id:"); ok {
			return strconv.Atoi(strings.TrimSpace(v))
		}
	}
	return 0, errNoMountID
}

// isMountRoot reports whether fd, opened from the directory parent, refers to
// the root of a mount, i.e. whether something is mounted there.
func isMountRoot(parent, fd int) (bool, error) {
	id, err1 := mountID(fd)
	pid, err2 := mountID(parent)
	if err1 == nil && err2 == nil {
		return id != pid, nil
	}
	// Kernels without mnt_id in fdinfo: compare devices. This misses bind
	// mounts within the same file system.
	var st, pst unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return false, err
	}
	if err := unix.Fstat(parent, &pst); err != nil {
		return false, err
	}
	return st.Dev != pst.Dev, nil
}

func mount(source, target, fstype string, flags uintptr, data string) error {
	log.Debugf("mount source=%q target=%q type=%q flags=%#x", source, target, fstype, flags)
	if err := unix.Mount(source, target, fstype, flags, data); err != nil {
		return &fs.PathError{Op: "mount", Path: target, Err: err}
	}
	return nil
}

// mountOn mounts on the target unless something is already mounted there
// and returns a descriptor of the (new or existing) mount.
func mountOn(t *osutil.MountTarget, source, fstype string, flags uintptr) (*os.File, error) {
	mounted, err := isMountRoot(t.ParentFd(), int(t.File.Fd()))
	if err != nil {
		return nil, err
	}
	if mounted {
		return t.Reopen()
	}
	if err := mount(source, osutil.FdPath(int(t.File.Fd())), fstype, flags, ""); err != nil {
		return nil, fmt.Errorf("mount %s on %s: %w", source, t.File.Name(), err)
	}
	f, err := t.Reopen()
	if err != nil {
		return nil, err
	}
	if ok, err := isMountRoot(t.ParentFd(), int(f.Fd())); err != nil || !ok {
		_ = f.Close()
		return nil, fmt.Errorf("mount on %s did not take effect (raced?)", t.File.Name())
	}
	return f, nil
}

// MountSysProc mounts procfs on /proc and recursively bind mounts the host's
// /sys on /sys as a slave mount. Existing mounts are reused.
func (m *Mounter) MountSysProc() error {
	proc, err := m.root.OpenMountTarget("/proc", true, nil)
	if err != nil {
		return err
	}
	defer proc.Close()
	p, err := mountOn(proc, "proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC)
	if err != nil {
		return err
	}
	_ = p.Close()

	sys, err := m.root.OpenMountTarget("/sys", true, nil)
	if err != nil {
		return err
	}
	defer sys.Close()
	s, err := mountOn(sys, "/sys", "", unix.MS_BIND|unix.MS_REC)
	if err != nil {
		return err
	}
	defer s.Close()
	// Do not propagate mount events from the container back to the host.
	if err := mount("", osutil.FdPath(int(s.Fd())), "", unix.MS_SLAVE|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("make %s an rslave mount: %w", s.Name(), err)
	}
	return nil
}

// BindMount bind mounts hostPath (a directory or a file) on containerPath
// inside the root, read-only if readonly is set. The mount point is created
// if needed; an existing mount is reused.
func (m *Mounter) BindMount(hostPath, containerPath string, readonly bool) error {
	fi, err := os.Stat(hostPath)
	if err != nil {
		return fmt.Errorf("bind mount source: %w", err)
	}

	if fi.IsDir() && osutil.IsDirEmpty(hostPath) {
		// Best effort: the source may be read-only.
		if f, err := os.OpenFile(filepath.Join(hostPath, keepFile), os.O_CREATE|os.O_RDONLY, 0o644); err != nil {
			log.Debugf("cannot create %s in %s: %v", keepFile, hostPath, err)
		} else {
			_ = f.Close()
		}
	}

	t, err := m.root.OpenMountTarget(containerPath, fi.IsDir(), nil)
	if err != nil {
		return err
	}
	defer t.Close()
	mnt, err := mountOn(t, hostPath, "", unix.MS_BIND)
	if err != nil {
		return err
	}
	defer mnt.Close()
	if readonly {
		return remountReadonly(mnt)
	}
	return nil
}

// remountReadonly makes a bind mount read-only while preserving its
// nosuid/nodev/noexec/atime flags, which a plain MS_REMOUNT would clear.
func remountReadonly(mnt *os.File) error {
	target := osutil.FdPath(int(mnt.Fd()))
	var st unix.Statfs_t
	if err := unix.Statfs(target, &st); err != nil {
		return &fs.PathError{Op: "statfs", Path: mnt.Name(), Err: err}
	}
	flags := uintptr(unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY) | lockedFlags(uint64(st.Flags))
	return mount("", target, "", flags, "")
}

// lockedFlags converts statfs(2) flags to the corresponding mount(2) flags.
func lockedFlags(stFlags uint64) uintptr {
	var flags uintptr
	for st, ms := range map[uint64]uintptr{
		unix.ST_NOSUID:     unix.MS_NOSUID,
		unix.ST_NODEV:      unix.MS_NODEV,
		unix.ST_NOEXEC:     unix.MS_NOEXEC,
		unix.ST_NOATIME:    unix.MS_NOATIME,
		unix.ST_NODIRATIME: unix.MS_NODIRATIME,
		unix.ST_RELATIME:   unix.MS_RELATIME,
	} {
		if stFlags&st != 0 {
			flags |= ms
		}
	}
	return flags
}

// Mounts returns the mount points at or below the root, deepest first.
func (m *Mounter) Mounts() ([]string, error) {
	mounts, err := readMountInfo()
	if err != nil {
		return nil, err
	}
	return mountpointsUnder(mounts, m.rootDir), nil
}

// UmountAll lazily unmounts every mount point at or below the root, deepest
// first, and returns the mount points it unmounted.
func (m *Mounter) UmountAll() ([]string, error) {
	mps, err := m.Mounts()
	if err != nil {
		return nil, err
	}
	var unmounted []string
	var errs []error
	for _, mp := range mps {
		err := unix.Unmount(mp, unix.MNT_DETACH)
		switch {
		case err == nil:
			log.Debugf("umount %s", mp)
			unmounted = append(unmounted, mp)
		case errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOENT):
			// Already detached together with its parent.
		default:
			errs = append(errs, &fs.PathError{Op: "umount", Path: mp, Err: err})
		}
	}
	return unmounted, errors.Join(errs...)
}
