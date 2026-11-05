//go:build linux

package seccomp

import "golang.org/x/sys/unix"

const (
	auditArch = unix.AUDIT_ARCH_X86_64
	sysClone  = unix.SYS_CLONE
	sysClone3 = unix.SYS_CLONE3
)

var blockedArch = []uintptr{
	unix.SYS_IOPL, unix.SYS_IOPERM, unix.SYS_CREATE_MODULE, unix.SYS_GET_KERNEL_SYMS,
	unix.SYS_QUERY_MODULE, unix.SYS_NFSSERVCTL, unix.SYS__SYSCTL, unix.SYS_USELIB, unix.SYS_USTAT,
	unix.SYS_SYSFS, unix.SYS_KEXEC_FILE_LOAD, unix.SYS_GET_MEMPOLICY, unix.SYS_QUOTACTL_FD,
}

// archPrologue denies the x32 system calls, which have the same
// architecture as x86-64 but numbers with bit 30 set.
func archPrologue() []unix.SockFilter {
	return []unix.SockFilter{
		jump(uint16(unix.BPF_JMP|unix.BPF_JGE|unix.BPF_K), 0x40000000, 0, 1),
		stmt(uint16(unix.BPF_RET|unix.BPF_K), retEPERM),
	}
}
