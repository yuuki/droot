package commands

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/yuuki/droot/internal/identity"
	"github.com/yuuki/droot/internal/imageconfig"
	"github.com/yuuki/droot/internal/mounter"
	"github.com/yuuki/droot/internal/osutil"
)

func TestSetupDev(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rootDir := filepath.Join(base, "root")
	hostDev := filepath.Join(base, "hostdev")
	for _, d := range []string{filepath.Join(rootDir, "dev"), hostDev} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Like a /dev/ptmx of the host: it must become a symlink.
	if err := os.WriteFile(filepath.Join(rootDir, "dev", "ptmx"), nil, 0o666); err != nil {
		t.Fatal(err)
	}
	root, err := osutil.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	mnt := mounter.NewMounter(root)
	defer func() {
		if _, err := mnt.UmountAll(); err != nil {
			t.Error(err)
		}
	}()

	if err := setupDev(root, mnt, true); err != nil {
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
	for _, l := range devLinks {
		if got, err := os.Readlink(filepath.Join(rootDir, l.path)); err != nil || got != l.target {
			t.Errorf("%s -> %q, %v; want -> %s", l.path, got, err, l.target)
		}
	}
	for path, magic := range map[string]uint32{"/dev/pts": unix.DEVPTS_SUPER_MAGIC, "/dev/shm": unix.TMPFS_MAGIC} {
		var st unix.Statfs_t
		if err := unix.Statfs(filepath.Join(rootDir, path), &st); err != nil || uint32(st.Type) != magic {
			t.Errorf("%s: file system %#x, %v; want %#x", path, st.Type, err, magic)
		}
	}
	if _, err := os.Stat(filepath.Join(rootDir, "dev", "pts", "ptmx")); err != nil {
		t.Errorf("the devpts instance has no ptmx: %v", err)
	}
	// Running again reuses everything.
	if err := setupDev(root, mnt, true); err != nil {
		t.Fatal(err)
	}
	if mounts, _ := mnt.Mounts(); len(mounts) != 2 {
		t.Errorf("want 2 mounts, got %q", mounts)
	}
	if _, err := mnt.UmountAll(); err != nil {
		t.Fatal(err)
	}

	// A host directory bind mounted on /dev must be left alone.
	devDir := filepath.Join(rootDir, "dev")
	if err := unix.Mount(hostDev, devDir, "", unix.MS_BIND, ""); err != nil {
		t.Skipf("mount not permitted here: %v", err)
	}
	if err := setupDev(root, mnt, true); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(hostDev); len(entries) != 0 {
		t.Fatalf("/dev entries were created in the host directory: %v", entries)
	}
}

func TestParseTmpfsOption(t *testing.T) {
	tests := []struct {
		spec    string
		want    tmpfsMount
		wantErr bool
	}{
		{spec: "/tmp", want: tmpfsMount{path: "/tmp", opts: tmpfsDefaults}},
		{spec: "/run/", want: tmpfsMount{path: "/run", opts: tmpfsDefaults}},
		{spec: "/tmp:exec,size=1g,mode=1777", want: tmpfsMount{path: "/tmp", opts: mounter.FSOptions{NoSuid: true, NoDev: true, Data: []string{"size=1g", "mode=1777"}}}},
		{spec: "/ro:ro,suid,dev,noatime", want: tmpfsMount{path: "/ro", opts: mounter.FSOptions{ReadOnly: true, NoExec: true, NoAtime: true}}},
		{spec: "tmp", wantErr: true},
		{spec: "/tmp:size=", wantErr: true},
		{spec: "/tmp:bogus", wantErr: true},
		{spec: "/tmp:size=1g,,", wantErr: true},
		{spec: "/tmp:uid=0,gid", wantErr: true},
	}
	for _, tt := range tests {
		got, err := parseTmpfsOption(tt.spec)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseTmpfsOption(%q) should fail, got %+v", tt.spec, got)
			}
			continue
		}
		if err != nil || got.path != tt.want.path || !reflect.DeepEqual(got.opts, tt.want.opts) {
			t.Errorf("parseTmpfsOption(%q) = %+v, %v; want %+v", tt.spec, got, err, tt.want)
		}
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

func TestContainerEnv(t *testing.T) {
	dir := t.TempDir()
	root, err := osutil.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	cfg := &imageconfig.Config{Env: []string{"A=cfg", "MULTI=x\ny", "B=cfg"}}

	got, err := containerEnv(root, cfg)
	if err != nil || !slices.Equal(got, cfg.Env) {
		t.Fatalf("without /.drootenv: %q, %v", got, err)
	}
	// /.drootenv (possibly edited) overrides the configuration.
	if err := os.WriteFile(filepath.Join(dir, ".drootenv"), []byte("A=file\nC=file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = containerEnv(root, cfg)
	if want := []string{"A=file", "MULTI=x\ny", "B=cfg", "C=file"}; err != nil || !slices.Equal(got, want) {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
	got, err = containerEnv(root, nil)
	if want := []string{"A=file", "C=file"}; err != nil || !slices.Equal(got, want) {
		t.Fatalf("without config: got %q, %v; want %q", got, err, want)
	}
}

func TestDefaultEnv(t *testing.T) {
	got := defaultEnv([]string{"PATH=/bin", "USER=keep"}, &identity.Credential{Username: "app", Home: "/home/app"})
	if want := []string{"PATH=/bin", "USER=keep", "HOME=/home/app", "LOGNAME=app"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	got = defaultEnv(nil, &identity.Credential{UID: 4242})
	if want := []string{"PATH=" + osutil.DefaultPath, "HOME=/"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
