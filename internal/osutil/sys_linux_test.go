package osutil

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLastCap(t *testing.T) {
	last, err := lastCap()
	if err != nil {
		t.Fatal(err)
	}
	if last < unix.CAP_AUDIT_CONTROL { // present since Linux 2.6.11
		t.Fatalf("lastCap() = %d is implausibly small", last)
	}
	if b, err := os.ReadFile("/proc/sys/kernel/cap_last_cap"); err == nil {
		var want uintptr
		for _, c := range b {
			if c >= '0' && c <= '9' {
				want = want*10 + uintptr(c-'0')
			}
		}
		if last != want {
			t.Fatalf("lastCap() = %d, /proc says %d", last, want)
		}
	}
}

func TestIsExecutable(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "script")
	mustWrite(t, script, "#!/bin/sh\n")
	if err := IsExecutable(script); err == nil && os.Geteuid() != 0 {
		t.Error("a 0644 file should not be executable")
	}
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := IsExecutable(script); err != nil {
		t.Errorf("a 0755 file should be executable: %v", err)
	}
	if err := IsExecutable(dir); err == nil {
		t.Error("a directory should not be executable")
	}
}

func TestClearInheritableCapabilities(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	errc := make(chan error, 1)
	go func() {
		// Work on a dedicated thread, which exits with the goroutine
		// because it is never unlocked.
		runtime.LockOSThread()
		hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
		var data [2]unix.CapUserData
		if err := unix.Capget(&hdr, &data[0]); err != nil {
			errc <- err
			return
		}
		data[0].Inheritable |= 1 << unix.CAP_SYS_ADMIN
		if err := unix.Capset(&hdr, &data[0]); err != nil {
			errc <- err
			return
		}
		_ = unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_RAISE, unix.CAP_SYS_ADMIN, 0, 0)

		if err := ClearInheritableCapabilities(); err != nil {
			errc <- err
			return
		}
		if err := unix.Capget(&hdr, &data[0]); err != nil {
			errc <- err
			return
		}
		if data[0].Inheritable != 0 || data[1].Inheritable != 0 {
			errc <- fmt.Errorf("inheritable set not cleared: %#x %#x", data[0].Inheritable, data[1].Inheritable)
			return
		}
		if v, err := unix.PrctlRetInt(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_IS_SET, unix.CAP_SYS_ADMIN, 0, 0); err == nil && v != 0 {
			errc <- fmt.Errorf("ambient capability not cleared")
			return
		}
		errc <- nil
	}()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}
