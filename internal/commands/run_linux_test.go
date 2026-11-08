package commands

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/yuuki/droot/internal/cli"
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

	if err := setupDev(root, mnt, true, false); err != nil {
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
	if err := setupDev(root, mnt, true, false); err != nil {
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
	if err := setupDev(root, mnt, true, false); err != nil {
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

func TestNotifySocketMount(t *testing.T) {
	tests := []struct {
		notify string // droot's NOTIFY_SOCKET, unset if empty
		env    []string
		want   string
	}{
		{"", nil, ""},
		{"/run/systemd/notify", []string{"NOTIFY_SOCKET=/run/systemd/notify"}, "/run/systemd/notify"},
		// An abstract socket needs no mount.
		{"@notify", []string{"NOTIFY_SOCKET=@notify"}, ""},
		// --env gave COMMAND another socket, or none.
		{"/run/systemd/notify", []string{"NOTIFY_SOCKET=/run/app/notify"}, ""},
		{"/run/systemd/notify", []string{"PATH=/bin"}, ""},
		// The image's own NOTIFY_SOCKET, without droot's.
		{"", []string{"NOTIFY_SOCKET=/run/systemd/notify"}, ""},
	}
	for _, tt := range tests {
		if tt.notify == "" {
			t.Setenv("NOTIFY_SOCKET", "")
			os.Unsetenv("NOTIFY_SOCKET")
		} else {
			t.Setenv("NOTIFY_SOCKET", tt.notify)
		}
		if got := notifySocketMount(tt.env); got != tt.want {
			t.Errorf("NOTIFY_SOCKET=%q, env %q: got %q, want %q", tt.notify, tt.env, got, tt.want)
		}
	}
}

func TestNotifySocketTarget(t *testing.T) {
	file := filepath.Join(t.TempDir(), "notify")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Refused before the container is looked at: no Root is needed.
	if err := notifySocketTarget(nil, notifyBind{sock: file, source: file}); err == nil || !strings.Contains(err.Error(), "is not a socket") {
		t.Errorf("a regular file: got %v", err)
	}
	if err := notifySocketTarget(nil, notifyBind{sock: file, source: file + ".missing"}); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing socket: got %v", err)
	}
	// Not fatal: droot may have inherited NOTIFY_SOCKET by mistake.
	if err := bindNotifySocket(nil, nil, notifyBind{sock: file + ".missing", source: file + ".missing"}); err != nil {
		t.Errorf("a missing socket must only be reported, got %v", err)
	}
}

// mountTestDir returns a scratch directory for a test that mounts things. It
// is only removed if nothing is mounted below it any more: os.RemoveAll
// would delete host files through a bind mount.
func mountTestDir(t *testing.T) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	dir, err := os.MkdirTemp("", "droot-run-test-")
	if err != nil {
		t.Fatal(err)
	}
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f, err := os.Open("/proc/self/mountinfo")
		if err != nil {
			t.Errorf("leaving %s: %v", dir, err)
			return
		}
		defer f.Close()
		mounts, err := mounter.ParseMountInfo(f)
		if err != nil {
			t.Errorf("leaving %s: %v", dir, err)
			return
		}
		for _, m := range mounts {
			if strings.HasPrefix(m.Mountpoint, dir+"/") {
				t.Errorf("leaving %s: %s is still mounted", dir, m.Mountpoint)
				return
			}
		}
		_ = os.RemoveAll(dir)
	})
	return dir
}

// TestBindNotifySocketWithin checks that the socket of NOTIFY_SOCKET is
// only bound where droot may create or mount things on its own (#56).
func TestBindNotifySocketWithin(t *testing.T) {
	dir := mountTestDir(t)
	sock := filepath.Join(dir, "n", "systemd", "notify")
	hostDir := filepath.Join(dir, "h")
	rootDir := filepath.Join(dir, "root")
	for _, d := range []string{filepath.Dir(sock), hostDir, rootDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	l, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
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
	b := notifyBind{sock: sock, source: sock, within: mounter.OnMountOf(root.Fd()), hostNS: true}

	// --bind H:<parent of the socket>: only reported, nothing is created
	// in H.
	if err := mnt.BindMount(hostDir, filepath.Join(dir, "n"), mounter.BindOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := bindNotifySocket(root, mnt, b); err != nil {
		t.Fatalf("a socket covered by a bind of a host directory must only be reported, got %v", err)
	}
	if entries, _ := os.ReadDir(hostDir); len(entries) != 0 {
		t.Errorf("droot created %v in the host directory", entries)
	}
	if _, err := mnt.UmountAll(); err != nil {
		t.Fatal(err)
	}

	// On the root's own file system, the socket is bound.
	if err := bindNotifySocket(root, mnt, b); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Stat(filepath.Join(rootDir, sock), &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFSOCK {
		t.Errorf("the socket is not bound in the container: %v, mode %o", err, st.Mode)
	}
}

// TestNoSystemd checks that --no-systemd leaves out the variables of
// systemd and the bind of NOTIFY_SOCKET (#57).
func TestNoSystemd(t *testing.T) {
	rootDir := t.TempDir()
	root, err := osutil.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	sock := filepath.Join(t.TempDir(), "notify")
	l, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	pid := strconv.Itoa(os.Getpid())
	for k, v := range map[string]string{
		"NOTIFY_SOCKET": sock, "LISTEN_PID": pid, "LISTEN_FDS": "1", "LISTEN_FDNAMES": "web",
		"WATCHDOG_USEC": "30000000", "WATCHDOG_PID": pid,
	} {
		t.Setenv(k, v)
	}

	run := func(argv ...string) []string {
		t.Helper()
		var env []string
		app := NewApp("test")
		app.Stdout = io.Discard
		app.Command("run").Action = func(_ context.Context, cmd *cli.Invocation) error {
			extraEnv, err := commandLineEnv(cmd)
			if err != nil {
				return err
			}
			r := &runner{cmd: cmd, root: root}
			if env, err = r.environment(extraEnv); err != nil {
				return err
			}
			// No Mounter: with --no-systemd, nothing is looked at.
			if cmd.Bool("no-systemd") {
				return r.bindNotifySocket(nil, env, nil, nil)
			}
			return nil
		}
		if err := app.Run(context.Background(), append([]string{"droot", "run", "-r", rootDir}, append(argv, "--", "true")...)); err != nil {
			t.Fatal(err)
		}
		return env
	}

	want := []string{"NOTIFY_SOCKET=" + sock, "LISTEN_PID=" + pid, "LISTEN_FDS=1", "LISTEN_FDNAMES=web", "WATCHDOG_USEC=30000000", "WATCHDOG_PID=" + pid}
	if got := run(); !slices.Equal(got, want) {
		t.Errorf("without --no-systemd: got %q, want %q", got, want)
	}
	if got := run("--no-systemd"); len(got) != 0 {
		t.Errorf("--no-systemd: got %q, want no variable", got)
	}
	// What the command line asks for explicitly is kept, but the socket is
	// not bound.
	if got, want := run("--no-systemd", "--env", "NOTIFY_SOCKET"), []string{"NOTIFY_SOCKET=" + sock}; !slices.Equal(got, want) {
		t.Errorf("--no-systemd --env NOTIFY_SOCKET: got %q, want %q", got, want)
	}
	if entries, _ := os.ReadDir(rootDir); len(entries) != 0 {
		t.Errorf("--no-systemd: droot created %v in the container", entries)
	}
}
