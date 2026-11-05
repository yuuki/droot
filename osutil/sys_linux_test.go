package osutil

import (
	"os"
	"path/filepath"
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
