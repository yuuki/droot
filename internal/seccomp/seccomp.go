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

// ErrUnsupported is returned on architectures without a filter.
var ErrUnsupported = fmt.Errorf("--seccomp default is not supported on this architecture")
