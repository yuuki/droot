//go:build linux

package systemd

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/yuuki/droot/internal/log"
)

// maxNotifyMessage is the size of the largest notification systemd accepts
// (NOTIFY_BUFFER_MAX): it ignores longer ones.
const maxNotifyMessage = 4096

// maxNotifyFDs is the largest number of descriptors a message can carry
// (SCM_MAX_FD).
const maxNotifyFDs = 253

// mainPIDKeys are the keys of a notification that make another process the
// main process of the service.
var mainPIDKeys = [][]byte{[]byte("MAINPID="), []byte("MAINPIDFD="), []byte("MAINPIDFDID=")}

// Relay relays the notifications (sd_notify(3)) that one process, COMMAND,
// sends to a socket of the relay, to the notification socket of the service
// manager, from the process running the relay.
//
// Rootless, droot stays the main process of the service and COMMAND is its
// child, and systemd only accepts notifications from the main process
// unless the unit sets NotifyAccess=all. The supervising droot gives
// COMMAND the socket of a Relay instead of systemd's, so that the default
// NotifyAccess=main accepts what it forwards.
//
// Only the notifications of COMMAND are relayed: the kernel tells who sent
// each (SCM_CREDENTIALS), and those of its children are dropped, as
// NotifyAccess=main would. The descriptors a notification carries
// (FDSTORE=1, BARRIER=1) are passed on, then closed. MAINPID=, MAINPIDFD=
// and MAINPIDFDID= are left out: systemd, which is not the parent of
// COMMAND, would lose its exit status, and the supervising droot would be
// left behind as a stray process of the unit.
type Relay struct {
	target string // the notification socket of the service manager
	dir    string // the private directory of the socket
	path   string // the socket COMMAND sends to
	in     int    // the socket at path
	out    int    // the socket that sends to target
	stop   [2]int // a pipe: closing its write end stops the relay
	pid    int    // the PID of COMMAND
	// warned is set once a notification of another process was dropped.
	warned  bool
	started bool
	done    chan struct{}
	once    sync.Once
}

// NewRelay creates the socket of a relay of the notifications to target,
// the path of a notification socket, in a new private directory created in
// the first of dirs where it can be. Start starts it.
func NewRelay(target string, dirs []string) (*Relay, error) {
	var errs []error
	for _, d := range dirs {
		r, err := newRelayIn(target, d)
		if err == nil {
			return r, nil
		}
		errs = append(errs, err)
	}
	if len(errs) == 0 {
		return nil, errors.New("no directory for the socket of the notification relay")
	}
	return nil, errors.Join(errs...)
}

// newRelayIn creates a relay whose socket is in a new directory in base.
func newRelayIn(target, base string) (r *Relay, err error) {
	// Mode 0700, and a name nobody else can guess: only the user reaches
	// the socket on the host, the container through its bind mount.
	dir, err := os.MkdirTemp(base, "droot-notify-")
	if err != nil {
		return nil, err
	}
	r = &Relay{target: target, dir: dir, path: filepath.Join(dir, "notify"), in: -1, out: -1, stop: [2]int{-1, -1}, done: make(chan struct{})}
	defer func() {
		if err != nil {
			r.release()
		}
	}()
	if len(r.path) >= len(unix.RawSockaddrUnix{}.Path) {
		return nil, fmt.Errorf("%s: the path of a socket must be shorter than %d bytes", r.path, len(unix.RawSockaddrUnix{}.Path))
	}
	if r.in, err = unix.Socket(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0); err != nil {
		return nil, fmt.Errorf("create the socket of the notification relay: %w", err)
	}
	// Before any notification can arrive, so that each has credentials.
	if err := unix.SetsockoptInt(r.in, unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		return nil, fmt.Errorf("setsockopt(SO_PASSCRED): %w", err)
	}
	if err := unix.Bind(r.in, &unix.SockaddrUnix{Name: r.path}); err != nil {
		return nil, &fs.PathError{Op: "bind", Path: r.path, Err: err}
	}
	// Every user of the container may send notifications, as to
	// systemd's socket: the directory keeps the other users of the host
	// out.
	if err := os.Chmod(r.path, 0o666); err != nil {
		return nil, err
	}
	if r.out, err = unix.Socket(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0); err != nil {
		return nil, fmt.Errorf("create the socket of the notification relay: %w", err)
	}
	if err := unix.Pipe2(r.stop[:], unix.O_CLOEXEC); err != nil {
		return nil, err
	}
	return r, nil
}

// Path returns the path of the socket of the relay, the NOTIFY_SOCKET to
// give COMMAND.
func (r *Relay) Path() string { return r.path }

// Start relays, in the background, the notifications the process pid
// (COMMAND) sends to the socket of the relay; those of other processes are
// dropped.
func (r *Relay) Start(pid int) {
	r.pid = pid
	r.started = true
	go r.serve()
}

// Close stops the relay, once it has relayed the notifications sent so far
// (COMMAND may send STOPPING=1 right before it exits), and removes its
// socket.
func (r *Relay) Close() error {
	r.once.Do(func() {
		if r.started {
			_ = unix.Close(r.stop[1])
			r.stop[1] = -1
			<-r.done
		}
		r.release()
	})
	return nil
}

// release closes the descriptors of the relay and removes its directory.
func (r *Relay) release() {
	for _, fd := range []int{r.in, r.out, r.stop[0], r.stop[1]} {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
	}
	_ = os.RemoveAll(r.dir)
}

// serve relays the notifications until Close, then those still queued.
func (r *Relay) serve() {
	defer close(r.done)
	fds := []unix.PollFd{{Fd: int32(r.in), Events: unix.POLLIN}, {Fd: int32(r.stop[0]), Events: unix.POLLIN}}
	for {
		fds[0].Revents, fds[1].Revents = 0, 0
		if _, err := unix.Poll(fds, -1); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			log.Warnf("the notifications of COMMAND are no longer relayed to systemd: poll: %v", err)
			return
		}
		switch {
		case fds[0].Revents&unix.POLLIN != 0:
			r.relay()
		case fds[0].Revents != 0:
			log.Warnf("the notifications of COMMAND are no longer relayed to systemd: error on the socket of the relay")
			return
		case fds[1].Revents != 0:
			for r.relay() {
			}
			return
		}
	}
}

// relay relays the next notification, if one is queued: it returns false
// when none is.
func (r *Relay) relay() bool {
	buf := make([]byte, maxNotifyMessage+1)
	oob := make([]byte, unix.CmsgSpace(unix.SizeofUcred)+unix.CmsgSpace(4*maxNotifyFDs))
	n, oobn, flags, _, err := unix.Recvmsg(r.in, buf, oob, unix.MSG_DONTWAIT|unix.MSG_CMSG_CLOEXEC)
	switch {
	case errors.Is(err, unix.EINTR):
		return true
	case err != nil:
		if !errors.Is(err, unix.EAGAIN) {
			log.Debugf("notification relay: recvmsg: %v", err)
		}
		return false
	}
	cred, fds := parseControl(oob[:oobn])
	defer func() {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}()
	switch {
	case cred == nil:
		log.Debugf("notification relay: dropping a notification without credentials")
	case int(cred.Pid) != r.pid:
		r.dropFrom(int(cred.Pid))
	case flags&unix.MSG_TRUNC != 0 || n > maxNotifyMessage:
		log.Warnf("dropping a notification of COMMAND larger than systemd accepts (%d bytes)", maxNotifyMessage)
	case flags&unix.MSG_CTRUNC != 0:
		log.Warnf("dropping a notification of COMMAND with more than %d descriptors", maxNotifyFDs)
	default:
		r.forward(buf[:n], fds)
	}
	return true
}

// dropFrom drops a notification of the process pid, which is not COMMAND.
func (r *Relay) dropFrom(pid int) {
	if r.warned {
		log.Debugf("notification relay: dropping a notification of PID %d", pid)
		return
	}
	r.warned = true
	log.Warnf("dropping the notifications of PID %d, which is not COMMAND (PID %d): rootless, droot only relays those of COMMAND to systemd", pid, r.pid)
}

// forward sends msg, a notification of COMMAND, and the descriptors fds it
// carries to the service manager, without the keys that change the main
// process.
func (r *Relay) forward(msg []byte, fds []int) {
	msg, left := withoutMainPID(msg)
	if left {
		log.Debugf("notification relay: leaving MAINPID= out: droot remains the main process of the service")
	}
	if len(bytes.TrimSpace(msg)) == 0 {
		return
	}
	var oob []byte
	if len(fds) > 0 {
		oob = unix.UnixRights(fds...)
	}
	if err := unix.Sendmsg(r.out, msg, oob, &unix.SockaddrUnix{Name: r.target}, 0); err != nil {
		log.Warnf("cannot relay a notification of COMMAND to %s: %v", r.target, err)
	}
}

// parseControl returns the credentials of the sender and the descriptors
// that the control messages oob carry.
func parseControl(oob []byte) (cred *unix.Ucred, fds []int) {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, nil
	}
	for i := range msgs {
		m := &msgs[i]
		if m.Header.Level != unix.SOL_SOCKET {
			continue
		}
		switch m.Header.Type {
		case unix.SCM_CREDENTIALS:
			if c, err := unix.ParseUnixCredentials(m); err == nil {
				cred = c
			}
		case unix.SCM_RIGHTS:
			if rights, err := unix.ParseUnixRights(m); err == nil {
				fds = append(fds, rights...)
			}
		}
	}
	return cred, fds
}

// withoutMainPID returns the notification msg without its lines setting
// the main process (mainPIDKeys), and whether it had any.
func withoutMainPID(msg []byte) ([]byte, bool) {
	lines := bytes.Split(msg, []byte("\n"))
	kept := lines[:0]
	for _, l := range lines {
		if !hasAnyPrefix(l, mainPIDKeys) {
			kept = append(kept, l)
		}
	}
	if len(kept) == len(lines) {
		return msg, false
	}
	return bytes.Join(kept, []byte("\n")), true
}

func hasAnyPrefix(b []byte, prefixes [][]byte) bool {
	for _, p := range prefixes {
		if bytes.HasPrefix(b, p) {
			return true
		}
	}
	return false
}
