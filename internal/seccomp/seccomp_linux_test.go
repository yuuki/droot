//go:build linux && (amd64 || arm64 || arm || 386)

package seccomp

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// TestMain lets the test binary act as a process that installs the filter
// and reports what a few system calls return (see TestFilter).
func TestMain(m *testing.M) {
	if os.Getenv("DROOT_TEST_SECCOMP_CHILD") == "1" {
		os.Exit(seccompChild())
	}
	os.Exit(m.Run())
}

func seccompChild() int {
	runtime.LockOSThread() // the filter is the thread's
	if os.Geteuid() != 0 {
		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			fmt.Println("nnp:", err)
			return 1
		}
	}
	if err := Load(); err != nil {
		fmt.Println("load:", err)
		return 1
	}
	errno := func(err error) string {
		var e unix.Errno
		if err == nil {
			return "ok"
		}
		if errors.As(err, &e) {
			return unix.ErrnoName(e)
		}
		return err.Error()
	}
	_, _, e3 := unix.Syscall(unix.SYS_CLONE3, 0, 0, 0)
	_, _, ek := unix.Syscall(unix.SYS_KEYCTL, 0, 0, 0)
	fmt.Println("unshare", errno(unix.Unshare(unix.CLONE_NEWNS)))
	fmt.Println("mount", errno(unix.Mount("none", "/nonexistent", "tmpfs", 0, "")))
	fmt.Println("clone3", errno(errnoOrNil(e3)))
	fmt.Println("keyctl", errno(errnoOrNil(ek)))
	fmt.Println("getpid", errno(nil))
	// Threads and processes can still be created, and programs run.
	out, err := exec.Command("/bin/sh", "-c", "echo child-ok").Output()
	fmt.Println("exec", strings.TrimSpace(string(out)), errno(err))
	return 0
}

func errnoOrNil(e unix.Errno) error {
	if e == 0 {
		return nil
	}
	return e
}

func TestFilter(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "DROOT_TEST_SECCOMP_CHILD=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	want := "unshare EPERM\nmount EPERM\nclone3 ENOSYS\nkeyctl EPERM\ngetpid ok\nexec child-ok ok\n"
	if string(out) != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
}

func TestProgram(t *testing.T) {
	p := Program()
	if len(p) > 255 {
		t.Fatalf("the program is too long for its jumps: %d instructions", len(p))
	}
	// Every jump lands inside the program.
	for i, ins := range p {
		if ins.Code&0x07 == unix.BPF_JMP { // the instruction class
			for _, off := range []uint8{ins.Jt, ins.Jf} {
				if i+1+int(off) >= len(p) {
					t.Fatalf("instruction %d jumps out of the program", i)
				}
			}
		}
	}
	last := p[len(p)-1]
	if last.Code != unix.BPF_RET|unix.BPF_K || last.K != retEPERM {
		t.Fatalf("the program must end with the EPERM return, got %+v", last)
	}
}
