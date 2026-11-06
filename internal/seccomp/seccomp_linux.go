//go:build linux && (amd64 || arm64 || arm || 386)

package seccomp

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// namespaceFlags are the clone(2) flags that create namespaces.
const namespaceFlags = unix.CLONE_NEWNS | unix.CLONE_NEWCGROUP | unix.CLONE_NEWUTS | unix.CLONE_NEWIPC |
	unix.CLONE_NEWUSER | unix.CLONE_NEWPID | unix.CLONE_NEWNET

// blockedCommon are the denied system calls that exist on every supported
// architecture; blockedArch (per architecture) adds the others.
var blockedCommon = []uintptr{
	// Namespaces and mounts.
	unix.SYS_UNSHARE, unix.SYS_SETNS, unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PIVOT_ROOT,
	unix.SYS_OPEN_TREE, unix.SYS_MOVE_MOUNT, unix.SYS_FSOPEN, unix.SYS_FSCONFIG, unix.SYS_FSMOUNT,
	unix.SYS_FSPICK, unix.SYS_MOUNT_SETATTR,
	// Kernel modules and kernels.
	unix.SYS_INIT_MODULE, unix.SYS_FINIT_MODULE, unix.SYS_DELETE_MODULE, unix.SYS_KEXEC_LOAD,
	// Keyrings, BPF, perf, userfaultfd, io_uring.
	unix.SYS_ADD_KEY, unix.SYS_KEYCTL, unix.SYS_REQUEST_KEY, unix.SYS_BPF, unix.SYS_PERF_EVENT_OPEN,
	unix.SYS_USERFAULTFD, unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER,
	// File handles (the "shocker" escape), process comparison.
	unix.SYS_NAME_TO_HANDLE_AT, unix.SYS_OPEN_BY_HANDLE_AT, unix.SYS_KCMP,
	// Host-wide state: clocks, swap, accounting, quotas, reboot, names,
	// kernel log, terminals, NUMA policies.
	unix.SYS_CLOCK_ADJTIME, unix.SYS_CLOCK_SETTIME, unix.SYS_SETTIMEOFDAY,
	unix.SYS_SWAPON, unix.SYS_SWAPOFF, unix.SYS_ACCT, unix.SYS_QUOTACTL, unix.SYS_REBOOT,
	unix.SYS_SETHOSTNAME, unix.SYS_SETDOMAINNAME, unix.SYS_SYSLOG, unix.SYS_VHANGUP,
	unix.SYS_LOOKUP_DCOOKIE, unix.SYS_MBIND, unix.SYS_SET_MEMPOLICY, unix.SYS_MOVE_PAGES,
}

// Load installs the default filter on the calling thread, which the program
// it executes next inherits. Like every seccomp filter, it can never be
// removed. Installing it needs no_new_privs or CAP_SYS_ADMIN.
func Load() error {
	prog := Program()
	fprog := unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&fprog)), 0, 0); err != nil {
		return fmt.Errorf("install the seccomp filter: %w", err)
	}
	return nil
}

func stmt(code uint16, k uint32) unix.SockFilter { return unix.SockFilter{Code: code, K: k} }

func jump(code uint16, k uint32, jt, jf uint8) unix.SockFilter {
	return unix.SockFilter{Code: code, Jt: jt, Jf: jf, K: k}
}

const (
	retAllow  = unix.SECCOMP_RET_ALLOW
	retEPERM  = unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)
	retENOSYS = unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)

	// Offsets in struct seccomp_data.
	offNr   = 0
	offArch = 4
	offArg0 = 16 // low 32 bits of args[0] on little-endian architectures
)

// Program returns the BPF program of the default filter.
func Program() []unix.SockFilter {
	ld := uint16(unix.BPF_LD | unix.BPF_W | unix.BPF_ABS)
	jeq := uint16(unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K)
	ret := uint16(unix.BPF_RET | unix.BPF_K)

	p := []unix.SockFilter{
		// Other architectures (e.g. 32-bit programs on a 64-bit kernel)
		// have other system call numbers: refuse them all.
		stmt(ld, offArch),
		jump(jeq, auditArch, 1, 0),
		stmt(ret, retEPERM),
		stmt(ld, offNr),
	}
	p = append(p, archPrologue()...)
	p = append(p,
		// clone: allowed without namespace flags (threads, fork).
		jump(jeq, uint32(sysClone), 0, 4),
		stmt(ld, offArg0),
		jump(uint16(unix.BPF_JMP|unix.BPF_JSET|unix.BPF_K), namespaceFlags, 0, 1),
		stmt(ret, retEPERM),
		stmt(ret, retAllow),
		// clone3: its flags are out of reach; ENOSYS makes libc use clone.
		jump(jeq, uint32(sysClone3), 0, 1),
		stmt(ret, retENOSYS),
	)
	blocked := append(append([]uintptr{}, blockedCommon...), blockedArch...)
	for i, nr := range blocked {
		// Jump to the final EPERM, after the remaining checks and the
		// ALLOW.
		p = append(p, jump(jeq, uint32(nr), uint8(len(blocked)-i), 0))
	}
	return append(p, stmt(ret, retAllow), stmt(ret, retEPERM))
}
