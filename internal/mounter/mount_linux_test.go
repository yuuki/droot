package mounter

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/yuuki/droot/internal/osutil"
)

// privilegedDir creates a scratch directory for tests that really mount
// things. Unlike t.TempDir it is only removed if nothing is mounted below it
// any more, so that a failed unmount can never make the cleanup delete files
// through a bind mount.
func privilegedDir(t *testing.T) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	dir, err := os.MkdirTemp("", "droot-mount-test-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(dir, "probe")
	if err := os.Mkdir(probe, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(probe, probe, "", unix.MS_BIND, ""); err != nil {
		_ = os.RemoveAll(dir)
		t.Skipf("mount not permitted here: %v", err)
	}
	_ = unix.Unmount(probe, unix.MNT_DETACH)

	t.Cleanup(func() {
		mounts, err := readMountInfo()
		if err != nil {
			t.Errorf("cannot verify mounts before cleanup, leaving %s: %v", dir, err)
			return
		}
		var left []string
		for _, mi := range mounts {
			if isUnder(dir, mi.Mountpoint) {
				left = append(left, mi.Mountpoint)
			}
		}
		if len(left) > 0 {
			for _, mp := range left {
				_ = unix.Unmount(mp, unix.MNT_DETACH)
			}
			t.Errorf("mounts left behind (now detached), leaving %s: %q", dir, left)
			return
		}
		_ = os.RemoveAll(dir)
	})
	return dir
}

// newTestMounter returns a Mounter for root whose mounts are removed when the
// test ends.
func newTestMounter(t *testing.T, root string) *Mounter {
	t.Helper()
	r, err := osutil.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	m := NewMounter(r)
	t.Cleanup(func() {
		if _, err := m.UmountAll(); err != nil {
			t.Errorf("UmountAll: %v", err)
		}
	})
	return m
}

func TestMounterLifecycle(t *testing.T) {
	dir := privilegedDir(t)
	root := filepath.Join(dir, "app")
	sibling := filepath.Join(dir, "app2") // shares the prefix "app"
	hostData := filepath.Join(dir, "host", "data")
	hostEmpty := filepath.Join(dir, "host", "empty")
	hostFile := filepath.Join(dir, "host", "config.yml")
	for _, d := range []string{root, filepath.Join(sibling, "mnt"), hostData, hostEmpty} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(hostData, "hello"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostFile, []byte("key: value"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A malicious image: /evil points outside of the root.
	if err := os.Symlink("/../../../"+filepath.Join(dir, "host"), filepath.Join(root, "evil")); err != nil {
		t.Fatal(err)
	}

	// The sibling has its own mount, which must survive UmountAll of root.
	if err := unix.Mount(hostData, filepath.Join(sibling, "mnt"), "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Unmount(filepath.Join(sibling, "mnt"), unix.MNT_DETACH) })

	r, err := osutil.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	m := NewMounter(r)
	if err := m.MountSysProc(); err != nil {
		t.Fatal(err)
	}
	if err := m.MountSysProc(); err != nil { // idempotent
		t.Fatal(err)
	}
	if err := m.BindMount(hostData, "/data", BindOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := m.BindMount(hostData, "/ro/data", BindOptions{ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.BindMount(hostEmpty, "/empty", BindOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := m.BindMount(hostFile, "/etc/app/config.yml", BindOptions{ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.BindMount(hostData, "/evil/escaped", BindOptions{}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(root, "proc", "self")); err != nil {
		t.Errorf("/proc is not mounted: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "data", "hello")); err != nil || string(b) != "hi" {
		t.Errorf("/data bind mount: %q, %v", b, err)
	}
	if err := os.WriteFile(filepath.Join(root, "data", "rw"), nil, 0o644); err != nil {
		t.Errorf("/data should be writable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "ro", "data", "x"), nil, 0o644); !errors.Is(err, unix.EROFS) {
		t.Errorf("/ro/data should be read-only, got %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "etc", "app", "config.yml")); err != nil || string(b) != "key: value" {
		t.Errorf("file bind mount: %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(hostEmpty, keepFile)); err != nil {
		t.Errorf("keep file should be created in empty sources: %v", err)
	}
	// The symlink must have been resolved inside the root.
	if _, err := os.Stat(filepath.Join(dir, "host", "escaped")); err == nil {
		t.Errorf("bind mount escaped the container root")
	}
	if ok, _ := isMountpoint(filepath.Join(root, dir, "host", "escaped")); !ok {
		t.Errorf("bind mount through the symlink should land inside the root")
	}

	mps, err := m.Mounts()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(mps, filepath.Join(root, "proc")) || !slices.Contains(mps, filepath.Join(root, "sys")) {
		t.Fatalf("missing mounts in %q", mps)
	}

	unmounted, err := m.UmountAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(unmounted) == 0 {
		t.Fatal("nothing was unmounted")
	}
	if left, _ := m.Mounts(); len(left) != 0 {
		t.Fatalf("mounts left after UmountAll: %q", left)
	}
	if ok, _ := isMountpoint(filepath.Join(sibling, "mnt")); !ok {
		t.Fatal("UmountAll must not touch a sibling directory sharing the prefix")
	}
	if again, err := m.UmountAll(); err != nil || len(again) != 0 {
		t.Fatalf("second UmountAll = %q, %v", again, err)
	}
}

func TestBindMountRefusesDangerousTargets(t *testing.T) {
	dir := privilegedDir(t)
	root := filepath.Join(dir, "app")
	host := filepath.Join(dir, "host")
	for _, d := range []string{root, host} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A malicious image: links into the kernel's pseudo file systems and to
	// the root itself.
	for link, target := range map[string]string{
		"intoproc": "/proc/sys/droot-test",
		"intosys":  "/sys/fs/droot-test",
		"toroot":   "/",
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	m := newTestMounter(t, root)
	if err := m.MountSysProc(); err != nil {
		t.Fatal(err)
	}

	for target, want := range map[string]string{
		"/intoproc/x": "procfs",
		"/intosys/x":  "sysfs",
		"/toroot":     osutil.ErrIsRoot.Error(),
		"/":           osutil.ErrIsRoot.Error(),
	} {
		err := m.BindMount(host, target, BindOptions{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("BindMount(%s) = %v, want an error mentioning %q", target, err, want)
		}
	}
	if ok, _ := isMountpoint(root); ok {
		t.Fatal("the root itself must never be mounted over")
	}
	// Re-running on the existing /proc and /sys mounts still works.
	if err := m.MountSysProc(); err != nil {
		t.Fatal(err)
	}
}

func TestBindMountsDoNotPropagateToTheHost(t *testing.T) {
	dir := privilegedDir(t)
	shared := filepath.Join(dir, "shared")
	root := filepath.Join(dir, "app")
	secret := filepath.Join(dir, "secret")
	for _, d := range []string{shared, root, secret} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The source lives on a shared mount, like / on systemd hosts.
	if err := unix.Mount("tmpfs", shared, "tmpfs", 0, "size=1m"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Unmount(shared, unix.MNT_DETACH) })
	if err := unix.Mount("", shared, "", unix.MS_SHARED, ""); err != nil {
		t.Fatal(err)
	}
	hostApp := filepath.Join(shared, "app")
	if err := os.Mkdir(hostApp, 0o755); err != nil {
		t.Fatal(err)
	}

	m := newTestMounter(t, root)
	if err := m.BindMount(hostApp, "/app", BindOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := m.BindMount(secret, "/app/secrets", BindOptions{ReadOnly: true}); err != nil {
		t.Fatal(err)
	}

	if ok, _ := isMountpoint(filepath.Join(hostApp, "secrets")); ok {
		t.Fatal("the nested read-only mount propagated back to the host's source directory")
	}
	mounts, err := readMountInfo()
	if err != nil {
		t.Fatal(err)
	}
	for _, mi := range mounts {
		if mi.Mountpoint == filepath.Join(root, "app") && strings.Contains(mi.Optional, "shared:") {
			t.Fatalf("the bind mount joined the host's peer group: %q", mi.Optional)
		}
	}
}

func TestUmountDoesNotFollowSymlinks(t *testing.T) {
	dir := privilegedDir(t)
	root := filepath.Join(dir, "app")
	hostMount := filepath.Join(dir, "outside", "b")
	for _, d := range []string{root, hostMount} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A mount of the host, outside the root.
	if err := unix.Mount("tmpfs", hostMount, "tmpfs", 0, "size=1m"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Unmount(hostMount, unix.MNT_DETACH) })
	mounts, err := readMountInfo()
	if err != nil {
		t.Fatal(err)
	}
	var hostID int
	for _, mi := range mounts {
		if mi.Mountpoint == hostMount {
			hostID = mi.ID
		}
	}

	// The container replaced <root>/a, which held a mount b, with a symlink
	// to the host directory after droot read /proc/self/mountinfo.
	if err := os.Symlink(filepath.Dir(hostMount), filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	m := newTestMounter(t, root)
	stale := MountInfo{ID: hostID, Mountpoint: filepath.Join(root, "a", "b")}
	if ok, _ := m.umount(stale); ok {
		t.Fatal("umount reported success through a symlink")
	}
	if ok, _ := isMountpoint(hostMount); !ok {
		t.Fatal("a mount outside the root was detached through a symlink")
	}
}

func TestUmountAllReportsHiddenMounts(t *testing.T) {
	dir := privilegedDir(t)
	root := filepath.Join(dir, "app")
	cover := filepath.Join(dir, "cover")
	data := filepath.Join(dir, "data")
	for _, d := range []string{root, cover, data} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m := newTestMounter(t, root)
	if err := m.BindMount(data, "/data", BindOptions{}); err != nil {
		t.Fatal(err)
	}
	// Something is mounted over the root itself, hiding <root>/data.
	if err := unix.Mount(cover, root, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	coverMounted := true
	t.Cleanup(func() {
		if coverMounted {
			_ = unix.Unmount(root, unix.MNT_DETACH)
		}
	})

	r, err := osutil.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, err = NewMounter(r).UmountAll()
	if err == nil || !strings.Contains(err.Error(), filepath.Join(root, "data")) || !strings.Contains(err.Error(), "is itself a mount point") {
		t.Fatalf("hidden mounts must be reported, got %v", err)
	}
	if ok, _ := isMountpoint(root); !ok {
		t.Fatal("the root itself must not be unmounted")
	}

	if err := unix.Unmount(root, unix.MNT_DETACH); err != nil {
		t.Fatal(err)
	}
	coverMounted = false
	if _, err := m.UmountAll(); err != nil {
		t.Fatal(err)
	}
	if left, _ := m.Mounts(); len(left) != 0 {
		t.Fatalf("mounts left: %q", left)
	}
}

func TestRemountReadonlyKeepsFlags(t *testing.T) {
	dir := privilegedDir(t)
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	for _, d := range []string{src, dst} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A nosuid,nodev,noexec source, like /tmp on many systems.
	if err := unix.Mount("tmpfs", src, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "size=1m"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Unmount(src, unix.MNT_DETACH) })

	r, err := osutil.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	m := NewMounter(r)
	if err := m.BindMount(src, "/dst", BindOptions{ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Unmount(dst, unix.MNT_DETACH) })

	var st unix.Statfs_t
	if err := unix.Statfs(dst, &st); err != nil {
		t.Fatal(err)
	}
	for name, flag := range map[string]uint64{"ro": unix.ST_RDONLY, "nosuid": unix.ST_NOSUID, "nodev": unix.ST_NODEV, "noexec": unix.ST_NOEXEC} {
		if uint64(st.Flags)&flag == 0 {
			t.Errorf("read-only bind mount lost the %s flag", name)
		}
	}
}

func isReadOnly(t *testing.T, path string) bool {
	t.Helper()
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		t.Fatal(err)
	}
	return uint64(st.Flags)&unix.ST_RDONLY != 0
}

func TestBindMountRecursiveReadOnly(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "mount_setattr", true: "fallback"}[fallback], func(t *testing.T) {
			if fallback {
				orig := mountSetattr
				mountSetattr = func(int, string, uint, *unix.MountAttr) error { return unix.ENOSYS }
				t.Cleanup(func() { mountSetattr = orig })
			}
			dir := privilegedDir(t)
			src := filepath.Join(dir, "src")
			sub := filepath.Join(src, "sub")
			if err := os.MkdirAll(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mount("tmpfs", sub, "tmpfs", unix.MS_NOSUID, "size=1m"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = unix.Unmount(sub, unix.MNT_DETACH) })
			root := filepath.Join(dir, "root")
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			r, err := osutil.OpenRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			m := NewMounter(r)
			if err := m.BindMount(src, "/dst", BindOptions{ReadOnly: true, Recursive: true}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = unix.Unmount(filepath.Join(root, "dst"), unix.MNT_DETACH) })

			if !isReadOnly(t, filepath.Join(root, "dst")) || !isReadOnly(t, filepath.Join(root, "dst", "sub")) {
				t.Error("the bind mount and its submount must be read-only")
			}
			if isReadOnly(t, sub) || isReadOnly(t, src) {
				t.Error("the host's mounts must stay writable")
			}
			if fallback {
				var st unix.Statfs_t
				if err := unix.Statfs(filepath.Join(root, "dst", "sub"), &st); err != nil || uint64(st.Flags)&unix.ST_NOSUID == 0 {
					t.Errorf("the submount lost its nosuid flag: %v", err)
				}
			}
		})
	}
}

func TestBindMountReplace(t *testing.T) {
	dir := privilegedDir(t)
	host := filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(host, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := osutil.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	m := NewMounter(r)
	target := filepath.Join(root, "etc", "resolv.conf")
	t.Cleanup(func() {
		for range 3 {
			_ = unix.Unmount(target, unix.MNT_DETACH)
		}
	})
	read := func() string {
		b, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	opts := BindOptions{ReadOnly: true, Replace: true}
	if err := m.BindMount(host, "/etc/resolv.conf", opts); err != nil {
		t.Fatal(err)
	}
	// Replace the host file like resolvconf does: write and rename.
	tmp := host + ".new"
	if err := os.WriteFile(tmp, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, host); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "old\n" {
		t.Fatalf("a file bind mount keeps the old inode, got %q", got)
	}
	if err := m.BindMount(host, "/etc/resolv.conf", BindOptions{ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "old\n" {
		t.Fatalf("without Replace the existing mount is reused, got %q", got)
	}
	if err := m.BindMount(host, "/etc/resolv.conf", opts); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "new\n" {
		t.Fatalf("Replace must bind the current file, got %q", got)
	}
	mounts, err := m.Mounts()
	if err != nil || len(mounts) != 1 {
		t.Fatalf("want exactly one mount below the root, got %q, %v", mounts, err)
	}
}

func TestProtectKernelFS(t *testing.T) {
	dir := privilegedDir(t)
	r, err := osutil.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	m := NewMounter(r)
	defer func() {
		if _, err := m.UmountAll(); err != nil {
			t.Error(err)
		}
	}()
	if err := m.MountSysProc(); err != nil {
		t.Fatal(err)
	}
	for range 2 { // idempotent
		if err := m.ProtectKernelFS(); err != nil {
			t.Fatal(err)
		}
	}

	// Masked files: their content is /dev/null's.
	masked := 0
	for _, p := range maskedPaths {
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() || !strings.HasPrefix(p, "/proc/") {
			continue // not on this kernel
		}
		masked++
		inRoot := filepath.Join(dir, p)
		if b, err := os.ReadFile(inRoot); err != nil || len(b) != 0 {
			t.Errorf("%s must be masked: %d bytes, %v", p, len(b), err)
		}
		if !isReadOnly(t, inRoot) {
			t.Errorf("the mask of %s must be read-only (chmod would change the host's /dev/null)", p)
		}
		if err := os.Chmod(inRoot, 0o600); !errors.Is(err, unix.EROFS) {
			t.Errorf("chmod through the mask of %s: %v, want EROFS", p, err)
		}
	}
	if masked == 0 {
		t.Error("no masked file exists on this kernel: the test checks nothing")
	}
	if fi, err := os.Stat("/dev/null"); err != nil || fi.Mode().Perm() != 0o666 {
		t.Errorf("the host's /dev/null changed: %v, %v", fi.Mode(), err)
	}
	if err := os.WriteFile(filepath.Join(dir, "proc", "sys", "kernel", "hostname"), []byte("x"), 0o644); !errors.Is(err, unix.EROFS) {
		t.Errorf("writing /proc/sys must fail with EROFS, got %v", err)
	}
	if _, err := os.Stat("/proc/sysrq-trigger"); err == nil {
		// Writing "h" only prints the help to the kernel log, should
		// the protection fail.
		if err := os.WriteFile(filepath.Join(dir, "proc", "sysrq-trigger"), []byte("h"), 0o200); !errors.Is(err, unix.EROFS) {
			t.Errorf("writing /proc/sysrq-trigger must fail with EROFS, got %v", err)
		}
	}
	if !isReadOnly(t, filepath.Join(dir, "sys")) {
		t.Error("/sys must be read-only")
	}
	if _, err := os.Stat(filepath.Join(dir, "sys", "fs", "cgroup")); err == nil && !isReadOnly(t, filepath.Join(dir, "sys", "fs", "cgroup")) {
		t.Error("the mounts below /sys must be read-only too")
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "sys", "firmware")); err == nil && len(entries) != 0 {
		t.Errorf("/sys/firmware must be masked, got %d entries", len(entries))
	}
	if isReadOnly(t, "/sys") || isReadOnly(t, "/proc/sys") {
		t.Error("the host's /sys and /proc/sys must stay writable")
	}
}
