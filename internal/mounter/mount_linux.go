package mounter

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/yuuki/droot/internal/log"
	"github.com/yuuki/droot/internal/osutil"
)

// keepFile is created in empty bind mount sources (behavior inherited from
// droot <= 0.7).
const keepFile = ".droot.keep"

// readMountInfo returns the mounts of the calling thread's mount namespace,
// which differs from the process's after UnshareMountNamespace.
func readMountInfo() ([]MountInfo, error) {
	f, err := os.Open("/proc/thread-self/mountinfo")
	if errors.Is(err, fs.ErrNotExist) {
		f, err = os.Open("/proc/self/mountinfo") // Linux < 3.17
	}
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

// ErrOtherMount is returned by the check of OnMountOf.
var ErrOtherMount = errors.New("is on another mount than the container root")

// OnMountOf returns an osutil.DirCheck accepting only directories on the
// same mount as the descriptor fd, e.g. to make sure a directory of the
// container is not a bind mount of a host directory.
func OnMountOf(fd int) osutil.DirCheck {
	return func(dir int) error {
		id, err1 := mountID(fd)
		did, err2 := mountID(dir)
		if err1 == nil && err2 == nil {
			if id != did {
				return ErrOtherMount
			}
			return nil
		}
		var st, dst unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return err
		}
		if err := unix.Fstat(dir, &dst); err != nil {
			return err
		}
		if st.Dev != dst.Dev {
			return ErrOtherMount
		}
		return nil
	}
}

func mount(source, target, fstype string, flags uintptr, data string) error {
	log.Debugf("mount source=%q target=%q type=%q flags=%#x", source, target, fstype, flags)
	if err := unix.Mount(source, target, fstype, flags, data); err != nil {
		return &fs.PathError{Op: "mount", Path: target, Err: err}
	}
	return nil
}

// mountOn mounts on the target unless something is already mounted there
// and returns a descriptor of the (new or existing) mount. With replace, an
// existing mount is unmounted first, so that the mount is made afresh.
//
// The mount is made a slave mount: a bind mount of a directory living on a
// shared mount (the default for / on systemd hosts) would otherwise join the
// host's peer group, and the mounts made under it in the container (e.g. a
// read-only bind inside a read-write one) would propagate back to the host's
// directory — without the read-only flag, which does not propagate.
func (m *Mounter) mountOn(t *osutil.MountTarget, source, fstype string, flags uintptr, data string, replace bool) (*os.File, error) {
	target := t.File
	mounted, err := isMountRoot(t.ParentFd(), int(target.Fd()))
	if err != nil {
		return nil, err
	}
	for i := 0; mounted && replace; i++ {
		if i == maxUmountRounds {
			return nil, fmt.Errorf("mounts keep appearing on %s", t.File.Name())
		}
		// The descriptor refers to the top-most mount on the target.
		if err := unix.Unmount(osutil.FdPath(int(target.Fd())), unix.MNT_DETACH); err != nil {
			return nil, &fs.PathError{Op: "umount", Path: t.File.Name(), Err: err}
		}
		log.Debugf("umount %s (to mount it afresh)", t.File.Name())
		f, err := t.Reopen()
		if err != nil {
			return nil, err
		}
		if target != t.File {
			_ = target.Close()
		}
		target = f
		if mounted, err = isMountRoot(t.ParentFd(), int(target.Fd())); err != nil {
			return nil, err
		}
	}
	if target != t.File {
		defer target.Close()
	}
	if !mounted {
		if err := mount(source, osutil.FdPath(int(target.Fd())), fstype, flags, data); err != nil {
			return nil, fmt.Errorf("mount %s on %s: %w", source, t.File.Name(), err)
		}
	}
	f, err := t.Reopen()
	if err != nil {
		return nil, err
	}
	if ok, err := isMountRoot(t.ParentFd(), int(f.Fd())); err != nil || !ok {
		_ = f.Close()
		return nil, fmt.Errorf("mount on %s did not take effect (raced?)", t.File.Name())
	}
	if !mounted {
		m.track(f)
	}
	propagation := uintptr(unix.MS_SLAVE)
	if flags&unix.MS_REC != 0 {
		propagation |= unix.MS_REC
	}
	if err := mount("", osutil.FdPath(int(f.Fd())), "", propagation, ""); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("make %s a slave mount: %w", t.File.Name(), err)
	}
	return f, nil
}

// kernelFS are the pseudo file systems found under /proc and /sys. Mount
// targets are never resolved through them: creating a directory there
// changes the state of the host's kernel (a mkdir in cgroupfs creates a
// cgroup, in tracefs a trace instance, ...).
var kernelFS = map[uint32]string{
	unix.PROC_SUPER_MAGIC:    "procfs",
	unix.SYSFS_MAGIC:         "sysfs",
	unix.CGROUP_SUPER_MAGIC:  "cgroupfs",
	unix.CGROUP2_SUPER_MAGIC: "cgroup2fs",
	unix.TRACEFS_MAGIC:       "tracefs",
	unix.DEBUGFS_MAGIC:       "debugfs",
	unix.SECURITYFS_MAGIC:    "securityfs",
	unix.BPF_FS_MAGIC:        "bpffs",
	unix.PSTOREFS_MAGIC:      "pstore",
	unix.EFIVARFS_MAGIC:      "efivarfs",
	unix.SELINUX_MAGIC:       "selinuxfs",
	unix.SMACK_MAGIC:         "smackfs",
	unix.BINFMTFS_MAGIC:      "binfmt_misc",
	0x62656570:               "configfs",
	0x65735543:               "fusectl",
}

// notKernelFS is an osutil.DirCheck refusing directories of kernelFS.
func notKernelFS(fd int) error {
	var st unix.Statfs_t
	if err := unix.Statfs(osutil.FdPath(fd), &st); err != nil {
		return err
	}
	if name, ok := kernelFS[uint32(st.Type)]; ok {
		return fmt.Errorf("refusing to resolve a mount target through %s (a symlink in the container points into /proc or /sys?)", name)
	}
	return nil
}

// MountSysProc mounts procfs on /proc and recursively bind mounts the host's
// /sys on /sys as a slave mount. Existing mounts are reused.
func (m *Mounter) MountSysProc() error {
	proc, err := m.root.OpenMountTarget("/proc", true, notKernelFS)
	if err != nil {
		return err
	}
	defer proc.Close()
	p, err := m.mountOn(proc, "proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "", false)
	if err != nil {
		return err
	}
	_ = p.Close()

	sys, err := m.root.OpenMountTarget("/sys", true, notKernelFS)
	if err != nil {
		return err
	}
	defer sys.Close()
	s, err := m.mountOn(sys, "/sys", "", unix.MS_BIND|unix.MS_REC, "", false) // made an rslave mount
	if err != nil {
		return err
	}
	return s.Close()
}

// maskedPaths are hidden from the container, like Docker does: files are
// covered with a read-only bind mount of /dev/null, directories with an
// empty read-only tmpfs. Paths missing on this kernel are skipped.
var maskedPaths = []string{
	"/proc/acpi", "/proc/asound", "/proc/interrupts", "/proc/kcore", "/proc/keys",
	"/proc/latency_stats", "/proc/sched_debug", "/proc/scsi", "/proc/timer_list",
	"/proc/timer_stats", "/sys/devices/virtual/powercap", "/sys/firmware",
}

// readonlyPaths of /proc are made read-only, like Docker does. /sys is
// read-only as a whole.
var readonlyPaths = []string{"/proc/bus", "/proc/fs", "/proc/irq", "/proc/sys", "/proc/sysrq-trigger"}

// ProtectKernelFS masks the sensitive paths of /proc and /sys, makes the
// writable parts of /proc that change the kernel's configuration read-only,
// and makes /sys read-only, recursively. It must be called after
// MountSysProc. It is idempotent.
//
// The masks only hold as long as COMMAND cannot unmount them, i.e. without
// CAP_SYS_ADMIN, which droot drops by default.
func (m *Mounter) ProtectKernelFS() error {
	proc, err := m.kernelFSRoot("/proc", unix.PROC_SUPER_MAGIC)
	if err != nil {
		return err
	}
	defer proc.Close()
	sys, err := m.kernelFSRoot("/sys", unix.SYSFS_MAGIC)
	if err != nil {
		return err
	}
	defer sys.Close()

	base := func(p string) (*os.File, string) {
		if rel, ok := strings.CutPrefix(p, "/proc/"); ok {
			return proc, rel
		}
		return sys, strings.TrimPrefix(p, "/sys/")
	}
	for _, p := range maskedPaths {
		b, rel := base(p)
		if err := m.maskPath(b, rel, p); err != nil {
			return fmt.Errorf("mask %s: %w", p, err)
		}
	}
	for _, p := range readonlyPaths {
		b, rel := base(p)
		if err := m.readonlyPath(b, rel, p); err != nil {
			return fmt.Errorf("make %s read-only: %w", p, err)
		}
	}
	return m.remountReadonlyRecursive(sys)
}

// kernelFSRoot opens the mount on path in the container, which must be of
// the file system with the given magic number.
func (m *Mounter) kernelFSRoot(path string, magic uint32) (*os.File, error) {
	f, err := m.root.OpenNoFollow(path)
	if err != nil {
		return nil, err
	}
	var st unix.Statfs_t
	if err := unix.Fstatfs(int(f.Fd()), &st); err != nil {
		_ = f.Close()
		return nil, &fs.PathError{Op: "statfs", Path: f.Name(), Err: err}
	}
	if uint32(st.Type) != magic { // Type is int32 or int64 depending on the architecture
		_ = f.Close()
		return nil, fmt.Errorf("%s is not the expected file system (type %#x)", f.Name(), st.Type)
	}
	return f, nil
}

// openBelow opens rel below the directory base without following any
// symlink, and returns descriptors of it and of its parent directory. A
// missing path gives an error satisfying errors.Is(err, fs.ErrNotExist).
func openBelow(base *os.File, rel string) (parent, fd int, err error) {
	parent, err = unix.FcntlInt(base.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return -1, -1, err
	}
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		fd, err = unix.Openat(parent, part, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			_ = unix.Close(parent)
			return -1, -1, err
		}
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT == unix.S_IFLNK {
			_ = unix.Close(fd)
			_ = unix.Close(parent)
			if err == nil {
				err = fmt.Errorf("unexpected symbolic link %q", part)
			}
			return -1, -1, err
		}
		if i == len(parts)-1 {
			return parent, fd, nil
		}
		_ = unix.Close(parent)
		parent = fd
	}
	return -1, -1, unix.EINVAL
}

func (m *Mounter) maskPath(base *os.File, rel, display string) error {
	parent, fd, err := openBelow(base, rel)
	if errors.Is(err, unix.ENOENT) {
		log.Debugf("not masking %s: not present", display)
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	defer unix.Close(fd)
	if mounted, err := isMountRoot(parent, fd); err != nil || mounted {
		return err // masked by a previous run
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	ro := uintptr(unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC)
	if st.Mode&unix.S_IFMT == unix.S_IFDIR {
		if err := mount("tmpfs", osutil.FdPath(fd), "tmpfs", ro, "size=4k,mode=0555"); err != nil {
			return err
		}
		f, err := reopenMount(parent, rel, display)
		if err != nil {
			return err
		}
		m.track(f)
		return f.Close()
	}
	// Read-only, so that chmod/chown through the mask cannot change the
	// host's /dev/null.
	if err := mount("/dev/null", osutil.FdPath(fd), "", unix.MS_BIND, ""); err != nil {
		return err
	}
	return m.remountReopened(parent, rel, display, true)
}

func (m *Mounter) readonlyPath(base *os.File, rel, display string) error {
	parent, fd, err := openBelow(base, rel)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	defer unix.Close(fd)
	mounted, err := isMountRoot(parent, fd)
	if err != nil {
		return err
	}
	if !mounted {
		if err := mount(osutil.FdPath(fd), osutil.FdPath(fd), "", unix.MS_BIND, ""); err != nil {
			return err
		}
	}
	return m.remountReopened(parent, rel, display, !mounted)
}

// reopenMount opens the mount just made on the last component of rel, in the
// directory parent.
func reopenMount(parent int, rel, display string) (*os.File, error) {
	fd, err := unix.Openat(parent, path.Base(rel), unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), display)
	if ok, err := isMountRoot(parent, fd); err != nil || !ok {
		_ = f.Close()
		return nil, fmt.Errorf("mount on %s did not take effect (raced?)", display)
	}
	return f, nil
}

// remountReopened makes the mount on the last component of rel, in the
// directory parent, read-only, and tracks it if it was made now.
func (m *Mounter) remountReopened(parent int, rel, display string, made bool) error {
	f, err := reopenMount(parent, rel, display)
	if err != nil {
		return err
	}
	defer f.Close()
	if made {
		m.track(f)
	}
	return remountReadonly(f)
}

// track records a mount made by this Mounter, for Rollback.
func (m *Mounter) track(f *os.File) {
	fd, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		log.Debugf("cannot track the mount on %s: %v", f.Name(), err)
		return
	}
	m.made = append(m.made, os.NewFile(uintptr(fd), f.Name()))
}

// Rollback detaches the mounts made by this Mounter, newest first, so that
// a droot run that fails before executing COMMAND leaves the mounts as it
// found them. Reused mounts are left alone. Mounts are unmounted through
// descriptors kept since they were made, never by path.
func (m *Mounter) Rollback() error {
	var errs []error
	for _, f := range slices.Backward(m.made) {
		err := unix.Unmount(osutil.FdPath(int(f.Fd())), unix.MNT_DETACH)
		switch {
		case err == nil:
			log.Debugf("rolled back the mount on %s", f.Name())
		case errors.Is(err, unix.EINVAL):
			// Already detached together with its parent.
		default:
			errs = append(errs, &fs.PathError{Op: "umount", Path: f.Name(), Err: err})
		}
		_ = f.Close()
	}
	m.made = nil
	return errors.Join(errs...)
}

// Release forgets the mounts made so far: they are kept.
func (m *Mounter) Release() {
	for _, f := range m.made {
		_ = f.Close()
	}
	m.made = nil
}

// BindMount bind mounts hostPath (a directory or a file) on containerPath
// inside the root. The mount point is created if needed; an existing mount
// is reused unless opts.Replace is set.
func (m *Mounter) BindMount(hostPath, containerPath string, opts BindOptions) error {
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

	t, err := m.root.OpenMountTarget(containerPath, fi.IsDir(), notKernelFS)
	if err != nil {
		return err
	}
	defer t.Close()
	flags := uintptr(unix.MS_BIND)
	if opts.Recursive {
		flags |= unix.MS_REC
	}
	mnt, err := m.mountOn(t, hostPath, "", flags, "", opts.Replace)
	if err != nil {
		return err
	}
	defer mnt.Close()
	switch {
	case opts.ReadOnly && opts.Recursive:
		return m.remountReadonlyRecursive(mnt)
	case opts.ReadOnly:
		return remountReadonly(mnt)
	}
	return nil
}

// MountFS mounts a new file system of type fstype (e.g. tmpfs or devpts) on
// the directory containerPath, created if needed. An existing mount is
// reused. check, if not nil, is applied to the directories traversed, in
// addition to the refusal of kernel file systems.
func (m *Mounter) MountFS(fstype, containerPath string, opts FSOptions, check osutil.DirCheck) error {
	both := func(fd int) error {
		if check != nil {
			if err := check(fd); err != nil {
				return err
			}
		}
		return notKernelFS(fd)
	}
	t, err := m.root.OpenMountTarget(containerPath, true, both)
	if err != nil {
		return err
	}
	defer t.Close()
	var flags uintptr
	if opts.ReadOnly {
		flags |= unix.MS_RDONLY
	}
	if opts.NoSuid {
		flags |= unix.MS_NOSUID
	}
	if opts.NoDev {
		flags |= unix.MS_NODEV
	}
	if opts.NoExec {
		flags |= unix.MS_NOEXEC
	}
	if opts.NoAtime {
		flags |= unix.MS_NOATIME
	}
	mnt, err := m.mountOn(t, fstype, fstype, flags, strings.Join(opts.Data, ","), false)
	if err != nil {
		return err
	}
	return mnt.Close()
}

// remountReadonlyRecursive makes the mount open as mnt and every mount below
// it read-only, preserving their other flags.
func (m *Mounter) remountReadonlyRecursive(mnt *os.File) error {
	// Linux 5.12 and later do it atomically.
	attr := unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY}
	err := mountSetattr(int(mnt.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_RECURSIVE, &attr)
	if err == nil {
		log.Debugf("mount_setattr %s: recursively read-only", mnt.Name())
		return nil
	}
	if !errors.Is(err, unix.ENOSYS) {
		return &fs.PathError{Op: "mount_setattr", Path: mnt.Name(), Err: err}
	}

	// Older kernels: remount the mount, then each mount below it, found in
	// mountinfo and opened from the root without following symlinks.
	if err := remountReadonly(mnt); err != nil {
		return err
	}
	top, err := mountID(int(mnt.Fd()))
	if err != nil {
		return fmt.Errorf("make the mounts below %s read-only: %w", mnt.Name(), err)
	}
	mounts, err := readMountInfo()
	if err != nil {
		return err
	}
	for _, mi := range descendants(mounts, top) {
		f, err := m.root.OpenNoFollow(strings.TrimPrefix(mi.Mountpoint, m.rootDir))
		if err != nil {
			return fmt.Errorf("make %s read-only: %w", mi.Mountpoint, err)
		}
		id, err := mountID(int(f.Fd()))
		if err == nil && id != mi.ID {
			err = fmt.Errorf("mount %d is not reachable there (found mount %d)", mi.ID, id)
		}
		if err == nil {
			err = remountReadonly(f)
		}
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("make %s read-only: %w", mi.Mountpoint, err)
		}
	}
	return nil
}

// mountSetattr is replaced by tests to exercise the fallback for kernels
// older than 5.12.
var mountSetattr = unix.MountSetattr

// descendants returns the mounts below the mount with ID top, parents first.
func descendants(mounts []MountInfo, top int) []MountInfo {
	var out []MountInfo
	parents := map[int]bool{top: true}
	for changed := true; changed; {
		changed = false
		for _, mi := range mounts {
			if parents[mi.Parent] && !parents[mi.ID] {
				parents[mi.ID] = true
				out = append(out, mi)
				changed = true
			}
		}
	}
	return out
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

// BindRootOnItself bind mounts the directory dir on itself, recursively, so
// that it can be made read-only with RemountRootReadonly. droot umount never
// unmounts a root directory itself, so this is only meant for a private
// mount namespace (UnshareMountNamespace). A Root opened on dir before must
// be opened again to see the new mount.
func BindRootOnItself(dir string) error {
	return mount(dir, dir, "", unix.MS_BIND|unix.MS_REC, "")
}

// RemountRootReadonly makes the mount of the root directory read-only,
// keeping its other flags; the mounts below it are not changed.
func (m *Mounter) RemountRootReadonly() error {
	fd, err := unix.FcntlInt(uintptr(m.root.Fd()), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), m.rootDir)
	defer f.Close()
	return remountReadonly(f)
}

// MountInfos returns the mounts below the root, parents first.
func (m *Mounter) MountInfos() ([]MountInfo, error) {
	mounts, err := readMountInfo()
	if err != nil {
		return nil, err
	}
	below := mountsBelow(mounts, m.rootDir)
	slices.Reverse(below)
	return below, nil
}

// Mounts returns the mount points below the root, deepest first.
func (m *Mounter) Mounts() ([]string, error) {
	mounts, err := readMountInfo()
	if err != nil {
		return nil, err
	}
	return mountpointsBelow(mounts, m.rootDir), nil
}

// maxUmountRounds bounds the rounds of UmountAll in case mounts keep
// appearing concurrently.
const maxUmountRounds = 64

// UmountAll lazily unmounts every mount point below the root (not the root
// itself, which may be a file system of its own), deepest first, and returns
// the mount points it unmounted. Mount points that cannot be unmounted are
// reported as an error.
//
// Mount points are taken from /proc/self/mountinfo, but unmounting by path
// would follow symlinks: a process in the container could replace a directory
// with a symlink to a host directory and make droot detach a host mount. So
// each mount point is opened from the root without following any symlink,
// and only unmounted (through its descriptor) if the descriptor belongs to
// the very mount listed in mountinfo. Mounts that are hidden by another
// mount become reachable once that one is gone, hence the rounds.
func (m *Mounter) UmountAll() ([]string, error) {
	var unmounted []string
	for range maxUmountRounds {
		mounts, err := readMountInfo()
		if err != nil {
			return unmounted, err
		}
		todo := mountsBelow(mounts, m.rootDir)
		if len(todo) == 0 {
			return unmounted, nil
		}

		progress := false
		var errs []error
		for _, mi := range todo {
			ok, err := m.umount(mi)
			if err != nil {
				errs = append(errs, err)
			}
			if ok {
				progress = true
				unmounted = append(unmounted, mi.Mountpoint)
			}
		}
		if !progress {
			return unmounted, errors.Join(append(errs, leftoverError(mounts, todo, m.rootDir))...)
		}
	}
	return unmounted, fmt.Errorf("mounts keep appearing below %s", m.rootDir)
}

func leftoverError(mounts, left []MountInfo, root string) error {
	var mps []string
	for _, mi := range left {
		if !slices.Contains(mps, mi.Mountpoint) {
			mps = append(mps, mi.Mountpoint)
		}
	}
	err := fmt.Errorf("%d mount point(s) below %s could not be unmounted: %s", len(mps), root, strings.Join(mps, ", "))
	for _, mi := range mounts {
		if mi.Mountpoint == root {
			return fmt.Errorf("%w (%s is itself a mount point and may hide them: unmount it first, e.g. `umount %s`)", err, root, root)
		}
	}
	return err
}

// umount unmounts the mount mi if it is still reachable at its mount point
// without following symlinks.
func (m *Mounter) umount(mi MountInfo) (bool, error) {
	f, err := m.root.OpenNoFollow(strings.TrimPrefix(mi.Mountpoint, m.rootDir))
	if errors.Is(err, unix.ENOENT) {
		return false, nil // detached together with its parent
	}
	if err != nil {
		return false, fmt.Errorf("umount %s: %w", mi.Mountpoint, err)
	}
	defer f.Close()

	if id, err := mountID(int(f.Fd())); err == nil {
		if id != mi.ID {
			log.Debugf("skipping %s: mount %d is not reachable there (found mount %d)", mi.Mountpoint, mi.ID, id)
			return false, nil
		}
	} else {
		var st unix.Stat_t
		if err := unix.Fstat(int(f.Fd()), &st); err != nil {
			return false, err
		}
		if fmt.Sprintf("%d:%d", unix.Major(st.Dev), unix.Minor(st.Dev)) != mi.Dev {
			return false, nil
		}
	}

	err = unix.Unmount(osutil.FdPath(int(f.Fd())), unix.MNT_DETACH)
	switch {
	case err == nil:
		log.Debugf("umount %s", mi.Mountpoint)
		return true, nil
	case errors.Is(err, unix.EINVAL):
		return false, nil // not a mount point any more
	default:
		return false, &fs.PathError{Op: "umount", Path: mi.Mountpoint, Err: err}
	}
}
