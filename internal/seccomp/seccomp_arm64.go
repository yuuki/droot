//go:build linux

package seccomp

import "golang.org/x/sys/unix"

const (
	auditArch = unix.AUDIT_ARCH_AARCH64
	sysClone  = unix.SYS_CLONE
	sysClone3 = unix.SYS_CLONE3
)

var blockedArch = []uintptr{
	unix.SYS_NFSSERVCTL, unix.SYS_KEXEC_FILE_LOAD, unix.SYS_GET_MEMPOLICY, unix.SYS_QUOTACTL_FD,
}

func archPrologue() []unix.SockFilter { return nil }
