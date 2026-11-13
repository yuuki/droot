// Package seccomp builds and installs the seccomp filter of
// `droot run --seccomp default`.
//
// The filter is a classic BPF program written here directly, so that droot
// needs neither libseccomp nor cgo. Like Docker's default profile, it denies
// the system calls an application in a container has no business making
// and that enlarge the attack surface of the kernel — creating namespaces,
// mounting, loading modules or kernels, keyrings, bpf, perf events,
// userfaultfd, io_uring, setting clocks, swapping, rebooting, ... — and
// lets everything else through. Denied calls fail with EPERM; clone3 fails
// with ENOSYS, so that the C library falls back to clone, whose flags can
// be checked (clone3 passes them in memory, out of reach of the filter).
package seccomp

import "fmt"

// Profiles lists the values of --seccomp.
var Profiles = []string{"default", "unconfined"}

// DeniedSystemCalls are the names of the system calls the default filter
// denies, on any of the architectures it supports, for the filters of other
// tools (SystemCallFilter= of the units of droot unit). Keep it in sync with
// blockedCommon and blockedArch. The filter also denies clone(2) with
// namespace flags, which no name can express (systemd's
// RestrictNamespaces= does), and makes clone3(2) fail with ENOSYS.
var DeniedSystemCalls = []string{
	// blockedCommon.
	"unshare", "setns", "mount", "umount2", "pivot_root",
	"open_tree", "move_mount", "fsopen", "fsconfig", "fsmount",
	"fspick", "mount_setattr",
	"init_module", "finit_module", "delete_module", "kexec_load",
	"add_key", "keyctl", "request_key", "bpf", "perf_event_open",
	"userfaultfd", "io_uring_setup", "io_uring_enter", "io_uring_register",
	"name_to_handle_at", "open_by_handle_at", "kcmp",
	"clock_adjtime", "clock_settime", "settimeofday",
	"swapon", "swapoff", "acct", "quotactl", "reboot",
	"sethostname", "setdomainname", "syslog", "vhangup",
	"lookup_dcookie", "mbind", "set_mempolicy", "move_pages",
	// blockedArch of amd64, 386, arm and arm64.
	"iopl", "ioperm", "vm86", "vm86old", "create_module", "get_kernel_syms",
	"query_module", "nfsservctl", "_sysctl", "uselib", "ustat", "sysfs",
	"stime", "umount", "kexec_file_load", "get_mempolicy", "quotactl_fd",
}

// ErrUnsupported is returned on architectures without a filter.
var ErrUnsupported = fmt.Errorf("--seccomp default is not supported on this architecture")
