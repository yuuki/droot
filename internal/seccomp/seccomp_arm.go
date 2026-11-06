//go:build linux

package seccomp

import "golang.org/x/sys/unix"

const (
	auditArch = unix.AUDIT_ARCH_ARM
	sysClone  = unix.SYS_CLONE
	sysClone3 = unix.SYS_CLONE3
)

var blockedArch = []uintptr{
	unix.SYS_NFSSERVCTL, unix.SYS_USELIB, unix.SYS_USTAT, unix.SYS_SYSFS, unix.SYS_GET_MEMPOLICY,
	unix.SYS_QUOTACTL_FD,
}

func archPrologue() []unix.SockFilter { return nil }
