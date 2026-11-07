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
	// environment: in ExtraFiles, after the sockets of socket activation, it
	// would take the place of a descriptor droot inherited (the listening
	// socket of server-starter is descriptor 3).
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	for _, sockets := range [][]*os.File{nil, {w}} {
		cmd, err := firstStageCommand(r, sockets, true)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(cmd.ExtraFiles, sockets) {
			t.Errorf("ExtraFiles: %v, want the sockets %v", cmd.ExtraFiles, sockets)
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
}

func TestListenFiles(t *testing.T) {
	// The sockets of another process: none.
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()+1))
	t.Setenv("LISTEN_FDS", "2")
	if files, err := listenFiles(); err != nil || len(files) != 0 {
		t.Fatalf("got %v, %v", files, err)
	}
	// More sockets than droot was passed: refused before any is taken
	// (closing them would close descriptors of the test, all close-on-exec,
	// as Go opens them).
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "100000")
	files, err := listenFiles()
	if err == nil || !strings.Contains(err.Error(), "LISTEN_FDS is 100000, but descriptor 3 ") || files != nil {
		t.Fatalf("got %v, %v", files, err)
	}
	// The descriptors are left as they were.
	if flags, err := unix.FcntlInt(3, unix.F_GETFD, 0); err == nil && flags&unix.FD_CLOEXEC == 0 {
		t.Errorf("descriptor 3 lost close-on-exec")
	}
}
