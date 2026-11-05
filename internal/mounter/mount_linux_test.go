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
	if err := m.BindMount(hostData, "/data", false); err != nil {
		t.Fatal(err)
	}
	if err := m.BindMount(hostData, "/ro/data", true); err != nil {
		t.Fatal(err)
	}
	if err := m.BindMount(hostEmpty, "/empty", false); err != nil {
		t.Fatal(err)
	}
	if err := m.BindMount(hostFile, "/etc/app/config.yml", true); err != nil {
		t.Fatal(err)
	}
	if err := m.BindMount(hostData, "/evil/escaped", false); err != nil {
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
		err := m.BindMount(host, target, false)
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
	if err := m.BindMount(hostApp, "/app", false); err != nil {
		t.Fatal(err)
	}
	if err := m.BindMount(secret, "/app/secrets", true); err != nil {
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
	if err := m.BindMount(data, "/data", false); err != nil {
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
	if err := m.BindMount(src, "/dst", true); err != nil {
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
