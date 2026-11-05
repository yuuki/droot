package mounter

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/unix"
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
		if left := mountpointsUnder(mounts, dir); len(left) > 0 {
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

	m := NewMounter(root)
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

	m := NewMounter(dir)
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
