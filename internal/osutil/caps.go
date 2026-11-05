package osutil

import (
	"fmt"
	"slices"
	"strings"
)

// capNames are the Linux capabilities by number (capabilities(7)).
var capNames = []string{
	"CHOWN", "DAC_OVERRIDE", "DAC_READ_SEARCH", "FOWNER", "FSETID", "KILL", "SETGID", "SETUID",
	"SETPCAP", "LINUX_IMMUTABLE", "NET_BIND_SERVICE", "NET_BROADCAST", "NET_ADMIN", "NET_RAW",
	"IPC_LOCK", "IPC_OWNER", "SYS_MODULE", "SYS_RAWIO", "SYS_CHROOT", "SYS_PTRACE", "SYS_PACCT",
	"SYS_ADMIN", "SYS_BOOT", "SYS_NICE", "SYS_RESOURCE", "SYS_TIME", "SYS_TTY_CONFIG", "MKNOD",
	"LEASE", "AUDIT_WRITE", "AUDIT_CONTROL", "SETFCAP", "MAC_OVERRIDE", "MAC_ADMIN", "SYSLOG",
	"WAKE_ALARM", "BLOCK_SUSPEND", "AUDIT_READ", "PERFMON", "BPF", "CHECKPOINT_RESTORE",
}

// Capability returns the number of the capability called name, with or
// without the CAP_ prefix, in any case.
func Capability(name string) (uintptr, bool) {
	name = strings.TrimPrefix(strings.ToUpper(name), "CAP_")
	i := slices.Index(capNames, name)
	return uintptr(i), i >= 0
}

// CapabilityName returns the name of the capability c, e.g. CAP_CHOWN.
func CapabilityName(c uintptr) string {
	if c < uintptr(len(capNames)) {
		return "CAP_" + capNames[c]
	}
	return fmt.Sprintf("capability %d", c)
}

// AllCapabilities returns the numbers of every capability droot knows.
func AllCapabilities() []uintptr {
	all := make([]uintptr, len(capNames))
	for i := range all {
		all[i] = uintptr(i)
	}
	return all
}
