package userns

import (
	"context"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSubIDs(t *testing.T) {
	const file = `# comment
ubuntu:100000:65536
other:200000:65536

1000:300000:1000
1000:100000:65536
ubuntu:150000:10
ubuntu:bad:10
ubuntu:400000:0
ubuntu:500000:10:extra
:600000:10
ubuntu:700000:5
`
	got, err := SubIDs(strings.NewReader(file), "ubuntu", 1000)
	if err != nil {
		t.Fatal(err)
	}
	want := []Range{{Outside: 100000, Size: 65536}, {Outside: 300000, Size: 1000}, {Outside: 700000, Size: 5}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	// Without a name (a user missing from /etc/passwd), by ID only.
	got, _ = SubIDs(strings.NewReader(file), "", 1000)
	if !reflect.DeepEqual(got, []Range{{Outside: 300000, Size: 1000}, {Outside: 100000, Size: 65536}}) {
		t.Errorf("by ID: got %+v", got)
	}
	got, _ = SubIDs(strings.NewReader(file), "nobody", 65534)
	if got != nil {
		t.Errorf("unknown user: got %+v", got)
	}
}

func TestMapping(t *testing.T) {
	m := Mapping{
		UIDs: buildMapping(1000, []Range{{Outside: 100000, Size: 65536}, {Outside: 300000, Size: 10}}),
		GIDs: buildMapping(1001, nil),
	}
	wantUIDs := []Range{{0, 1000, 1}, {1, 100000, 65536}, {65537, 300000, 10}}
	if !reflect.DeepEqual(m.UIDs, wantUIDs) {
		t.Errorf("got %+v, want %+v", m.UIDs, wantUIDs)
	}
	if got := strings.Join(helperArgs(42, m.UIDs), " "); got != "42 0 1000 1 1 100000 65536 65537 300000 10" {
		t.Errorf("newuidmap arguments: %s", got)
	}
	if got := mapString(m.GIDs); got != "0 1001 1\n" {
		t.Errorf("gid_map: %q", got)
	}
	if got := m.String(); got != "uid 0→1000 1-65536→100000-165535 65537-65546→300000-300009, gid 0→1001" {
		t.Errorf("String: %s", got)
	}
}

func TestRunNeedsInit(t *testing.T) {
	// A test binary must not run itself again in a user namespace.
	if err := Run(context.Background(), Mapping{}); err == nil || !strings.Contains(err.Error(), "userns.Init") {
		t.Fatalf("got %v", err)
	}
}

func TestFirstStageCommand(t *testing.T) {
	// The pipe of the first stage keeps its own number, named by the
	// environment: in ExtraFiles, it would become descriptor 3, in place of
	// the one droot may have inherited (the listening socket of
	// server-starter), and the inherited descriptors would be moved around.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	cmd, err := firstStageCommand(r, true)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.ExtraFiles != nil {
		t.Errorf("ExtraFiles: %v", cmd.ExtraFiles)
	}
	if want := envSync + "=" + strconv.Itoa(int(r.Fd())); !slices.Contains(cmd.Env, want) {
		t.Errorf("no %s in the environment of the child", want)
	}
	flags, err := unix.FcntlInt(r.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC != 0 {
		t.Errorf("the pipe would not be inherited: flags %#x (%v)", flags, err)
	}
	if a := cmd.SysProcAttr; a == nil || a.Cloneflags&unix.CLONE_NEWUSER == 0 || !a.Setpgid {
		t.Errorf("SysProcAttr: %+v", a)
	}
}

func TestAdoptListenPID(t *testing.T) {
	const parentPID = "4242"
	t.Setenv("LISTEN_PID", parentPID)
	t.Setenv("LISTEN_PIDFDID", "1234")
	adoptListenPID(parentPID)
	if got, want := os.Getenv("LISTEN_PID"), strconv.Itoa(os.Getpid()); got != want {
		t.Errorf("LISTEN_PID of the supervising droot: got %s, want %s", got, want)
	}
	if v, ok := os.LookupEnv("LISTEN_PIDFDID"); ok {
		t.Errorf("LISTEN_PIDFDID of the supervising droot is kept: %s", v)
	}
	// Descriptors meant for another process are left alone.
	t.Setenv("LISTEN_PID", "1")
	t.Setenv("LISTEN_PIDFDID", "1234")
	adoptListenPID(parentPID)
	if got := os.Getenv("LISTEN_PID") + " " + os.Getenv("LISTEN_PIDFDID"); got != "1 1234" {
		t.Errorf("LISTEN_PID and LISTEN_PIDFDID of another process: got %s", got)
	}
}
