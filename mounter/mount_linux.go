package mounter

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

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

func mount(source, target, fstype string, flags uintptr, data string) error {
	log.Debugf("mount source=%q target=%q type=%q flags=%#x", source, target, fstype, flags)
	if err := unix.Mount(source, target, fstype, flags, data); err != nil {
		return &fs.PathError{Op: "mount", Path: target, Err: err}
	}
	return nil
}

// target returns the host path of containerPath, resolved securely inside the
// root, and creates it as a directory or an empty file if missing.
func (m *Mounter) target(containerPath string, dir bool) (string, error) {
	p, err := osutil.SecureJoin(m.rootDir, containerPath)
	if err != nil {
		return "", err
	}
	if dir {
		return p, os.MkdirAll(p, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDONLY, 0o644)
	if err != nil {
		return "", err
	}
	return p, f.Close()
}

// MountSysProc mounts procfs on /proc and recursively bind mounts the host's
// /sys on /sys as a slave mount. Existing mounts are reused.
func (m *Mounter) MountSysProc() error {
	proc, err := m.target("/proc", true)
	if err != nil {
		return err
	}
	if mounted, err := isMountpoint(proc); err != nil {
		return err
	} else if !mounted {
		if err := mount("proc", proc, "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
			return fmt.Errorf("mount /proc: %w", err)
		}
	}

	sys, err := m.target("/sys", true)
	if err != nil {
		return err
	}
	if mounted, err := isMountpoint(sys); err != nil {
		return err
	} else if !mounted {
		if err := mount("/sys", sys, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			return fmt.Errorf("bind mount /sys: %w", err)
		}
	}
	// Do not propagate mount events from the container back to the host.
	if err := mount("", sys, "", unix.MS_SLAVE|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("make /sys an rslave mount: %w", err)
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

	target, err := m.target(containerPath, fi.IsDir())
	if err != nil {
		return err
	}
	mounted, err := isMountpoint(target)
	if err != nil {
		return err
	}
	if !mounted {
		if err := mount(hostPath, target, "", unix.MS_BIND, ""); err != nil {
			return err
		}
	}
	if readonly {
		return remountReadonly(target)
	}
	return nil
}

// remountReadonly makes a bind mount read-only while preserving its
// nosuid/nodev/noexec/atime flags, which a plain MS_REMOUNT would clear.
func remountReadonly(target string) error {
	var st unix.Statfs_t
	if err := unix.Statfs(target, &st); err != nil {
		return &fs.PathError{Op: "statfs", Path: target, Err: err}
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
