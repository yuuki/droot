package osutil

import (
	"fmt"
	"maps"
	"slices"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/yuuki/droot/internal/log"
)

// rlimits maps the names of `docker run --ulimit` to resources.
var rlimits = map[string]int{
	"as": unix.RLIMIT_AS, "core": unix.RLIMIT_CORE, "cpu": unix.RLIMIT_CPU, "data": unix.RLIMIT_DATA,
	"fsize": unix.RLIMIT_FSIZE, "locks": unix.RLIMIT_LOCKS, "memlock": unix.RLIMIT_MEMLOCK,
	"msgqueue": unix.RLIMIT_MSGQUEUE, "nice": unix.RLIMIT_NICE, "nofile": unix.RLIMIT_NOFILE,
	"nproc": unix.RLIMIT_NPROC, "rss": unix.RLIMIT_RSS, "rtprio": unix.RLIMIT_RTPRIO,
	"rttime": unix.RLIMIT_RTTIME, "sigpending": unix.RLIMIT_SIGPENDING, "stack": unix.RLIMIT_STACK,
}

func rlimitResource(name string) (int, bool) {
	r, ok := rlimits[name]
	return r, ok
}

// RlimitNames returns the names accepted by ParseRlimit.
func RlimitNames() []string { return slices.Sorted(maps.Keys(rlimits)) }

// SetRlimits sets the resource limits of the process, which COMMAND
// inherits. Raising a hard limit requires CAP_SYS_RESOURCE: call it before
// dropping capabilities.
func SetRlimits(limits []Rlimit) error {
	for _, l := range limits {
		r, _ := rlimitResource(l.Name)
		// syscall.Setrlimit, unlike unix.Setrlimit, tells the Go runtime
		// not to restore its own RLIMIT_NOFILE at exec.
		if err := syscall.Setrlimit(r, &syscall.Rlimit{Cur: l.Soft, Max: l.Hard}); err != nil {
			return fmt.Errorf("setrlimit %s=%d:%d: %w", l.Name, l.Soft, l.Hard, err)
		}
		log.Debugf("ulimit %s=%d:%d", l.Name, l.Soft, l.Hard)
	}
	return nil
}
