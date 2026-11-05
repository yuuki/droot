//go:build linux

package seccomp

import "golang.org/x/sys/unix"

const (
	auditArch = unix.AUDIT_ARCH_I386
	sysClone  = unix.SYS_CLONE
	sysClone3 = unix.SYS_CLONE3
)

var blockedArch = []uintptr{
	unix.SYS_IOPL, unix.SYS_IOPERM, unix.SYS_VM86, unix.SYS_VM86OLD, unix.SYS_CREATE_MODULE,
	unix.SYS_GET_KERNEL_SYMS, unix.SYS_QUERY_MODULE, unix.SYS_NFSSERVCTL, unix.SYS__SYSCTL,
	unix.SYS_USELIB, unix.SYS_USTAT, unix.SYS_SYSFS, unix.SYS_STIME, unix.SYS_UMOUNT,
	unix.SYS_GET_MEMPOLICY, unix.SYS_QUOTACTL_FD,
}

func archPrologue() []unix.SockFilter { return nil }
