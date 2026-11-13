package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// openPTY opens a pseudo-terminal and returns its master and its slave.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skip("no pseudo-terminal:", err)
	}
	t.Cleanup(func() { master.Close() })
	rc, err := master.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var n uint32
	var ioctlErr error
	if err := rc.Control(func(fd uintptr) {
		if ioctlErr = unix.IoctlSetPointerInt(int(fd), unix.TIOCSPTLCK, 0); ioctlErr == nil {
			n, ioctlErr = unix.IoctlGetUint32(int(fd), unix.TIOCGPTN)
		}
	}); err != nil || ioctlErr != nil {
		t.Fatal(err, ioctlErr)
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skip("no pseudo-terminal:", err)
	}
	t.Cleanup(func() { slave.Close() })
	return master, slave
}

// TestWriteFileToTerminal writes to -o /dev/stdout when STDOUT is a
// terminal: refused for a tar archive (droot export), written for text
// (droot unit).
func TestWriteFileToTerminal(t *testing.T) {
	master, slave := openPTY(t)
	ctx := context.Background()
	stdout := fmt.Sprintf("/proc/self/fd/%d", slave.Fd())
	write := func(w io.Writer) error {
		_, err := io.WriteString(w, "[Service]\n")
		return err
	}

	err := writeFile(ctx, stdout, true, write)
	if err == nil || !strings.Contains(err.Error(), "refusing to write a tar archive to a terminal") {
		t.Fatalf("a tar archive must not be written to a terminal, got %v", err)
	}

	if err := writeFile(ctx, stdout, false, write); err != nil {
		t.Fatalf("text must be written to a terminal: %v", err)
	}
	if err := master.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := master.Read(buf)
	if err != nil || !strings.HasPrefix(string(buf[:n]), "[Service]") {
		t.Fatalf("the terminal shows %q, %v", buf[:n], err)
	}
}
