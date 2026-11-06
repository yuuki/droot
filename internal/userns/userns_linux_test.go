package userns

import (
	"context"
	"os"
	"reflect"
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

func TestInheritedFiles(t *testing.T) {
	// Descriptors without FD_CLOEXEC, as inherited across execve(2), are
	// passed on at their numbers; those with FD_CLOEXEC, like the
	// descriptors droot opens itself, are not.
	var cloexec []int
	// inherit returns a descriptor without FD_CLOEXEC on a new pipe, which
	// has its own blocking mode.
	inherit := func(nonBlocking bool) int {
		var p [2]int
		if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = unix.Close(p[0]); _ = unix.Close(p[1]) })
		cloexec = append(cloexec, p[0], p[1])
		if err := unix.SetNonblock(p[0], nonBlocking); err != nil {
			t.Fatal(err)
		}
		fd, err := unix.FcntlInt(uintptr(p[0]), unix.F_DUPFD, 0)
		if err != nil {
			t.Fatal(err)
		}
		return fd
	}
	blocking, nonBlocking := inherit(false), inherit(true)

	files, err := inheritedFiles()
	if err != nil {
		t.Fatal(err)
	}
	at := func(fd int) *os.File {
		if fd-3 < len(files) {
			return files[fd-3]
		}
		return nil
	}
	for _, fd := range []int{blocking, nonBlocking} {
		f := at(fd)
		if f == nil {
			t.Fatalf("descriptor %d is not passed on: %v", fd, files)
		}
		// exec.Cmd passes ExtraFiles on with Fd().
		if got := f.Fd(); got != uintptr(fd) {
			t.Errorf("entry of descriptor %d: %d", fd, got)
		}
	}
	for _, fd := range cloexec {
		if at(fd) != nil {
			t.Errorf("descriptor %d with FD_CLOEXEC is passed on", fd)
		}
	}
	// The child shares the mode of the descriptors: Fd() must not change it.
	for fd, want := range map[int]bool{blocking: false, nonBlocking: true} {
		fl, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		if got := fl&unix.O_NONBLOCK != 0; err != nil || got != want {
			t.Errorf("descriptor %d: non-blocking %v (%v), want %v", fd, got, err, want)
		}
	}
	// Closes blocking and nonBlocking, and the descriptors the test
	// binary itself inherited, which it does not use.
	closeFiles(files)
	if _, err := unix.FcntlInt(uintptr(blocking), unix.F_GETFD, 0); err == nil {
		t.Errorf("closeFiles left descriptor %d open", blocking)
	}
}

func TestAdoptListenPID(t *testing.T) {
	const parentPID = "4242"
	t.Setenv("LISTEN_PID", parentPID)
	adoptListenPID(parentPID)
	if got, want := os.Getenv("LISTEN_PID"), strconv.Itoa(os.Getpid()); got != want {
		t.Errorf("LISTEN_PID of the supervising droot: got %s, want %s", got, want)
	}
	// Descriptors meant for another process are left alone.
	t.Setenv("LISTEN_PID", "1")
	adoptListenPID(parentPID)
	if got := os.Getenv("LISTEN_PID"); got != "1" {
		t.Errorf("LISTEN_PID of another process: got %s", got)
	}
}
