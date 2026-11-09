//go:build linux

package systemd

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// envHelperSocket makes TestRelayHelperProcess send a notification to the
// socket it names.
const envHelperSocket = "DROOT_TEST_RELAY_SOCKET"

// TestRelayHelperProcess is not a test: run by TestRelay, it sends a
// notification from another process than COMMAND.
func TestRelayHelperProcess(t *testing.T) {
	path := os.Getenv(envHelperSocket)
	if path == "" {
		t.Skip("only run by TestRelay")
	}
	notify(t, path, "STATUS=from another process", nil)
}

// listenManager returns a socket playing the notification socket of the
// service manager at path.
func listenManager(t *testing.T, path string) int {
	t.Helper()
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		t.Fatal(err)
	}
	if err := unix.Bind(fd, &unix.SockaddrUnix{Name: path}); err != nil {
		t.Fatal(err)
	}
	return fd
}

// notify sends msg and the descriptors fds to the socket at path, like
// sd_pid_notify_with_fds(3).
func notify(t *testing.T, path, msg string, fds []int) {
	t.Helper()
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	var oob []byte
	if len(fds) > 0 {
		oob = unix.UnixRights(fds...)
	}
	if err := unix.Sendmsg(fd, []byte(msg), oob, &unix.SockaddrUnix{Name: path}, 0); err != nil {
		t.Fatal(err)
	}
}

// receive returns the next notification the manager socket fd receives,
// the PID of its sender and the descriptors it carries.
func receive(t *testing.T, fd int) (msg string, pid int, fds []int) {
	t.Helper()
	pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	if n, err := unix.Poll(pfd, 10000); err != nil || n == 0 {
		t.Fatalf("no notification received (%v)", err)
	}
	buf := make([]byte, maxNotifyMessage+1)
	oob := make([]byte, 1024)
	n, oobn, _, _, err := unix.Recvmsg(fd, buf, oob, unix.MSG_CMSG_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	cred, fds := parseControl(oob[:oobn])
	if cred == nil {
		t.Fatal("a notification without credentials")
	}
	return string(buf[:n]), int(cred.Pid), fds
}

func TestRelay(t *testing.T) {
	dir := t.TempDir()
	manager := listenManager(t, filepath.Join(dir, "manager"))
	if _, err := NewRelay(filepath.Join(dir, "manager"), nil); err == nil {
		t.Error("NewRelay without directory must fail")
	}
	r, err := NewRelay(filepath.Join(dir, "manager"), []string{filepath.Join(dir, "missing"), dir})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if fi, err := os.Stat(r.Path()); err != nil || fi.Mode().Type() != fs.ModeSocket || fi.Mode().Perm() != 0o666 {
		t.Fatalf("the socket of the relay: %v, %v", fi, err)
	}
	if fi, err := os.Stat(filepath.Dir(r.Path())); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the directory of the relay: %v, %v", fi, err)
	}
	// This process plays COMMAND.
	r.Start(os.Getpid())

	// The notification of another process is dropped. Then those of
	// COMMAND are relayed, without MAINPID=, from this process.
	helper := exec.Command(os.Args[0], "-test.run=^TestRelayHelperProcess$")
	helper.Env = append(os.Environ(), envHelperSocket+"="+r.Path())
	if out, err := helper.CombinedOutput(); err != nil {
		t.Fatalf("helper process: %v: %s", err, out)
	}
	notify(t, r.Path(), "READY=1\nMAINPID=1\nSTATUS=ready", nil)
	if msg, pid, _ := receive(t, manager); msg != "READY=1\nSTATUS=ready" || pid != os.Getpid() {
		t.Errorf("got %q from PID %d, want READY=1 and STATUS=ready without MAINPID= from PID %d", msg, pid, os.Getpid())
	}

	// Descriptors are passed on, and the relay closes its copies: the
	// manager's is then the last write end of a barrier (sd_notify_barrier(3)).
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	notify(t, r.Path(), "BARRIER=1", []int{int(pw.Fd())})
	_ = pw.Close()
	msg, _, fds := receive(t, manager)
	if msg != "BARRIER=1" || len(fds) != 1 {
		t.Fatalf("got %q with %d descriptors, want BARRIER=1 with one", msg, len(fds))
	}
	_ = unix.Close(fds[0])
	if err := pr.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := pr.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("the barrier is not released: %v", err)
	}

	// A notification that only sets the main process is not sent at all.
	notify(t, r.Path(), "MAINPID=1\n", nil)
	notify(t, r.Path(), "STATUS=next", nil)
	if msg, _, _ := receive(t, manager); msg != "STATUS=next" {
		t.Errorf("got %q, want STATUS=next", msg)
	}

	// Close relays what was sent before, then removes the socket.
	notify(t, r.Path(), "STOPPING=1", nil)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if msg, _, _ := receive(t, manager); msg != "STOPPING=1" {
		t.Errorf("got %q, want STOPPING=1", msg)
	}
	if _, err := os.Stat(filepath.Dir(r.Path())); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the directory of the relay is left: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("a second Close: %v", err)
	}
}

func TestRelayNotStarted(t *testing.T) {
	dir := t.TempDir()
	listenManager(t, filepath.Join(dir, "manager"))
	r, err := NewRelay(filepath.Join(dir, "manager"), []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(r.Path())); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the directory of the relay is left: %v", err)
	}
}

func TestRelayPath(t *testing.T) {
	dir := t.TempDir()
	listenManager(t, filepath.Join(dir, "manager"))
	if _, err := NewRelay(filepath.Join(dir, "manager"), []string{"relative"}); err == nil {
		t.Error("a relative directory must be refused")
	}
	r, err := NewRelay(filepath.Join(dir, "manager"), []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for path, want := range map[string]bool{
		r.Path():                        true,
		"/tmp/droot-notify-1/notify":    true,
		"droot-notify-1/notify":         false,
		"/tmp/droot-notify-1/other":     false,
		"/tmp/other/notify":             false,
		"/tmp/droot-notify-1/../notify": false,
		"/etc/passwd":                   false,
	} {
		if got := IsRelayPath(path); got != want {
			t.Errorf("IsRelayPath(%q) = %v, want %v", path, got, want)
		}
	}
	if err := RemoveRelayPath("/etc/passwd"); err == nil {
		t.Error("RemoveRelayPath must refuse another path")
	}
	// The relay keeps its socket open: removing its path does not stop it.
	if err := RemoveRelayPath(r.Path()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(r.Path())); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the directory of the relay is left: %v", err)
	}
	if err := RemoveRelayPath(r.Path()); err != nil {
		t.Errorf("removing it again: %v", err)
	}
}

func TestWithoutMainPID(t *testing.T) {
	for _, tt := range []struct {
		msg, want string
		left      bool
	}{
		{"READY=1", "READY=1", false},
		{"READY=1\nSTATUS=x\r\n", "READY=1\nSTATUS=x\r\n", false},
		{"READY=1\nMAINPID=42", "READY=1", true},
		{"MAINPID=42\nREADY=1\n", "READY=1", true},
		{"MAINPIDFD=1\nMAINPIDFDID=7\nSTATUS=x", "STATUS=x", true},
		{"STATUS=MAINPID=42", "STATUS=MAINPID=42", false},
		// systemd also splits lines at "\r".
		{"READY=1\rMAINPID=42", "READY=1", true},
		{"\r\rMAINPIDFD=1\rSTATUS=x\r\n", "STATUS=x", true},
	} {
		got, left := withoutMainPID([]byte(tt.msg))
		if string(got) != tt.want || left != tt.left {
			t.Errorf("withoutMainPID(%q) = %q, %v; want %q, %v", tt.msg, got, left, tt.want, tt.left)
		}
	}
}
