// Package systemd carries the protocols of the systemd service manager
// through droot to COMMAND: notifications (sd_notify(3), for Type=notify
// and the watchdog) and socket activation (sd_listen_fds(3)).
//
// systemd describes both in the environment of the main process of the
// service, and the variables name that process by its PID. As root, droot
// replaces itself with COMMAND, which keeps its PID and its descriptors;
// rootless, droot passes them on to the droot it starts in a user
// namespace (see Adopt), which then replaces itself with COMMAND.
package systemd

import (
	"os"
	"strconv"
	"strings"
)

// The variables of systemd (systemd.exec(5)).
const (
	// listenPID is the PID of the process the sockets of socket
	// activation are for.
	listenPID = "LISTEN_PID"
	// listenPIDFDID is the inode number of the pidfd of that process
	// (systemd 259 and later): sd_listen_fds(3) checks it too.
	listenPIDFDID = "LISTEN_PIDFDID"
	// listenFDs is the number of sockets, from FirstListenFD on.
	listenFDs = "LISTEN_FDS"
	// listenFDNames are their names, separated by colons.
	listenFDNames = "LISTEN_FDNAMES"
	// watchdogUSec is the timeout of the watchdog, in microseconds.
	watchdogUSec = "WATCHDOG_USEC"
	// watchdogPID is the PID of the process expected to send the
	// keep-alive notifications: any process, if unset.
	watchdogPID = "WATCHDOG_PID"
)

// NotifySocket is the variable giving the address of the socket of
// sd_notify(3).
const NotifySocket = "NOTIFY_SOCKET"

// FirstListenFD is the first descriptor of socket activation
// (SD_LISTEN_FDS_START).
const FirstListenFD = 3

// maxListenFDs bounds LISTEN_FDS: more descriptors than a process may
// have by default (fs.nr_open).
const maxListenFDs = 1 << 20

// Env returns the variables of systemd that lookup (droot's environment)
// has and that are for COMMAND, the process pid:
//
//   - NOTIFY_SOCKET;
//   - LISTEN_PID, LISTEN_PIDFDID, LISTEN_FDS and LISTEN_FDNAMES, if
//     LISTEN_PID is pid: the sockets are droot's, which COMMAND inherits;
//   - WATCHDOG_USEC and WATCHDOG_PID, if WATCHDOG_PID is pid or unset.
//
// The others are for another process.
func Env(lookup func(string) (string, bool), pid int) []string {
	var env []string
	add := func(keys ...string) {
		for _, k := range keys {
			if v, ok := lookup(k); ok {
				env = append(env, k+"="+v)
			}
		}
	}
	add(NotifySocket)
	if isPID(lookup, listenPID, pid) {
		add(listenPID, listenPIDFDID, listenFDs, listenFDNames)
	}
	if _, ok := lookup(watchdogUSec); ok {
		if _, set := lookup(watchdogPID); !set || isPID(lookup, watchdogPID, pid) {
			add(watchdogUSec, watchdogPID)
		}
	}
	return env
}

// ListenFDs returns the number of sockets that socket activation passed to
// the process pid, from FirstListenFD on: LISTEN_FDS, if LISTEN_PID is
// pid, and 0 otherwise, or if LISTEN_FDS is not a valid number.
func ListenFDs(lookup func(string) (string, bool), pid int) int {
	if !isPID(lookup, listenPID, pid) {
		return 0
	}
	v, _ := lookup(listenFDs)
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > maxListenFDs {
		return 0
	}
	return n
}

// Adopt takes over, in this process, the sockets and the watchdog of the
// process parent, which started it and passed them on to it (rootless,
// the supervising droot): LISTEN_PID and WATCHDOG_PID, if they are
// parent, become the PID of this process. LISTEN_PIDFDID, which names
// parent by its pidfd, is then unset: sd_listen_fds(3) checks LISTEN_PID
// alone.
func Adopt(parent int) {
	self := strconv.Itoa(os.Getpid())
	if isPID(os.LookupEnv, listenPID, parent) {
		_ = os.Setenv(listenPID, self)
		_ = os.Unsetenv(listenPIDFDID)
	}
	if isPID(os.LookupEnv, watchdogPID, parent) {
		_ = os.Setenv(watchdogPID, self)
	}
}

// NotifySocketPath returns the path of the socket of addr, an address of
// NOTIFY_SOCKET, or "" if it is not a path: an abstract socket ("@NAME"),
// or an AF_VSOCK address ("vsock:CID:PORT").
func NotifySocketPath(addr string) string {
	if strings.HasPrefix(addr, "/") {
		return addr
	}
	return ""
}

// isPID reports whether the variable key of lookup is the PID pid.
func isPID(lookup func(string) (string, bool), key string, pid int) bool {
	v, ok := lookup(key)
	if !ok {
		return false
	}
	n, err := strconv.Atoi(v)
	return err == nil && n > 0 && n == pid
}
