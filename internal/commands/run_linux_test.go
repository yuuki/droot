package commands

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/yuuki/droot/internal/osutil"
)

func TestCreateDevices(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rootDir := filepath.Join(base, "root")
	hostDev := filepath.Join(base, "hostdev")
	for _, d := range []string{rootDir, hostDev} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root, err := osutil.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if err := createDevices(root); err != nil {
		if os.IsPermission(err) {
			t.Skipf("mknod not permitted here: %v", err)
		}
		t.Fatal(err)
	}
	for _, d := range devices {
		var st unix.Stat_t
		if err := unix.Lstat(filepath.Join(rootDir, d.path), &st); err != nil {
			t.Fatal(err)
		}
		if st.Uid != 0 || st.Gid != 0 || st.Mode&0o777 != 0o666 || st.Rdev != unix.Mkdev(d.major, d.minor) {
			t.Errorf("%s: uid=%d gid=%d mode=%o", d.path, st.Uid, st.Gid, st.Mode)
		}
	}

	// A host directory bind mounted on /dev must be left alone.
	devDir := filepath.Join(rootDir, "dev")
	if err := unix.Mount(hostDev, devDir, "", unix.MS_BIND, ""); err != nil {
		t.Skipf("mount not permitted here: %v", err)
	}
	defer unix.Unmount(devDir, unix.MNT_DETACH)
	if err := createDevices(root); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(hostDev); len(entries) != 0 {
		t.Fatalf("device nodes were created in the host directory: %v", entries)
	}
}

func TestRunAbortsWhenInterrupted(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	rootDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Ctrl-C before the setup
	app := NewApp("test")
	app.Stdout = io.Discard
	err := app.Run(ctx, []string{"droot", "run", "--root", rootDir, "--", "/bin/true"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if entries, _ := os.ReadDir(rootDir); len(entries) != 0 {
		t.Fatalf("droot went on with the setup after the interruption: %v", entries)
	}
}
