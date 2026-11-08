package userns

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/yuuki/droot/internal/log"
	"github.com/yuuki/droot/internal/systemd"
)

// Init handles the stages of a child started by Run. It must be called
// first in main: the first stage waits for its ID maps and executes droot
// again, without returning; the second stage arranges to be killed if the
// supervising droot dies, and returns.
func Init() {
	initialized = true
	if fd := os.Getenv(envSync); fd != "" {
		firstStage(fd)
	}
	if pid := os.Getenv(envParent); pid != "" {
		_ = os.Unsetenv(envParent)
		parent, _ = strconv.Atoi(pid)
		notifyRelay = os.Getenv(envNotifyRelay)
		_ = os.Unsetenv(envNotifyRelay)
		// The supervising droot passed its sockets on (see start),
		// and its watchdog is for COMMAND, which this droot becomes.
		systemd.Adopt(parent)
		if err := KeepParentDeathSignal(); err != nil {
			fmt.Fprintf(os.Stderr, "droot: %v\n", err)
			os.Exit(1)
		}
	}
}

// parent is the PID of the supervising droot, in the second stage.
var parent int

// notifyRelay is the socket of the relay of the supervising droot, in the
// second stage.
var notifyRelay string

// initialized is set by Init: Run runs the program again, which must handle
// the stages.
var initialized bool

// Supervised reports whether droot is the second stage of a rootless droot,
// which supervises it.
func Supervised() bool { return parent != 0 }

// NotifyRelay returns, in the second stage, the socket of the relay by which
// the supervising droot relays the notifications of COMMAND to systemd, to
// bind in the container instead of systemd's NOTIFY_SOCKET; "" without
// relay (see Options.RelayNotifications).
func NotifyRelay() string { return notifyRelay }

// KeepParentDeathSignal arranges for droot, and the command it executes, to
// be killed when the supervising droot dies, if there is one. It must be
// called again after a change of credentials, which clears it.
func KeepParentDeathSignal() error {
	if parent == 0 {
		return nil
	}
	if err := unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGKILL), 0, 0, 0); err != nil {
		return fmt.Errorf("prctl(PR_SET_PDEATHSIG): %w", err)
	}
	if os.Getppid() != parent {
		return errors.New("the supervising droot is gone")
	}
	return nil
}

// firstStage waits until the parent has written the ID maps (it closes the
// descriptor fd after writing a byte), then executes droot again, as root of
// the namespace. The other descriptors, the sockets of socket activation
// and those droot inherited, are left open.
func firstStage(fd string) {
	n, err := strconv.Atoi(fd)
	if err != nil {
		os.Exit(1)
	}
	f := os.NewFile(uintptr(n), "sync")
	var b [1]byte
	if _, err := io.ReadFull(f, b[:]); err != nil {
		os.Exit(1) // the parent failed and reported it
	}
	_ = f.Close()
	_ = os.Unsetenv(envSync)
	err = syscall.Exec("/proc/self/exe", os.Args, os.Environ()) //nolint:gosec // G204: droot itself, with its own arguments
	fmt.Fprintf(os.Stderr, "droot: exec /proc/self/exe: %v\n", err)
	os.Exit(1)
}

// CurrentMapping returns the mapping of a namespace for the current user:
// their UID and GID as 0, and their subordinate IDs after them if they have
// both, and if newuidmap and newgidmap are installed.
func CurrentMapping() (Mapping, error) {
	uid, gid := os.Getuid(), os.Getgid()
	m := Mapping{UIDs: buildMapping(uid, nil), GIDs: buildMapping(gid, nil)}
	name := ""
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		name = u.Username
	}
	subuid, err := readSubIDs("/etc/subuid", name, uid)
	if err != nil {
		return m, err
	}
	subgid, err := readSubIDs("/etc/subgid", name, uid)
	if err != nil {
		return m, err
	}
	if len(subuid) == 0 || len(subgid) == 0 {
		log.Debugf("no subordinate IDs for %s in /etc/subuid and /etc/subgid: root is the only user of the namespace", userName(name, uid))
		return m, nil
	}
	for _, h := range []string{"newuidmap", "newgidmap"} {
		if _, err := exec.LookPath(h); err != nil {
			log.Warnf("%s is not installed (package uidmap or shadow-utils): the subordinate IDs of %s are not used, root is the only user of the namespace", h, userName(name, uid))
			return m, nil
		}
	}
	m.UIDs, m.GIDs, m.Helpers = buildMapping(uid, subuid), buildMapping(gid, subgid), true
	return m, nil
}

func userName(name string, uid int) string {
	if name != "" {
		return name
	}
	return "uid " + strconv.Itoa(uid)
}

func readSubIDs(file, name string, id int) ([]Range, error) {
	f, err := os.Open(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return SubIDs(f, name, id)
}

// Run runs droot again with the same arguments in a new user namespace with
// the mapping m, and waits for it: the signals droot receives are passed on
// to it, and its exit status is returned as an *ExitError (or droot is
// killed by the same signal). It does not return before the child exits.
// If ctx is done before the child is started, it is not.
func Run(ctx context.Context, m Mapping, o Options) error {
	if !initialized {
		// E.g. a test binary, which would run itself again.
		return errors.New("rootless mode is not available in this program (userns.Init was not called)")
	}
	// The child is killed when the thread that started it exits: keep it.
	runtime.LockOSThread()
	// From now on, the signals are kept for the child.
	sigs := make(chan os.Signal, 16)
	signal.Notify(sigs, forwarded...)
	defer signal.Stop(sigs)
	if err := ctx.Err(); err != nil {
		return err
	}
	var relay *systemd.Relay
	if o.RelayNotifications {
		relay = newNotifyRelay()
	}
	// Close is idempotent: on success, the relay is closed as soon as the
	// child has exited.
	defer closeRelay(relay)
	tty := terminal()
	cmd, release, err := start(m, tty < 0, relay)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil { // interrupted while starting
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	if relay != nil {
		// The child becomes COMMAND without changing its PID.
		relay.Start(cmd.Process.Pid)
	}
	done := make(chan struct{})
	go forward(sigs, done, cmd.Process, tty)
	release()
	err = cmd.Wait()
	close(done)
	// What COMMAND sent before it exited (STOPPING=1, ...), before droot
	// exits too.
	closeRelay(relay)
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return err
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok {
		return &ExitError{Code: 1}
	}
	if ws.Signaled() {
		dieFromSignal(ws.Signal())
		return &ExitError{Code: 128 + int(ws.Signal())}
	}
	return &ExitError{Code: ws.ExitStatus()}
}

// newNotifyRelay creates the relay of the notifications of COMMAND to
// systemd's NOTIFY_SOCKET, if it is the path of a socket, in a directory of
// XDG_RUNTIME_DIR or else of TMPDIR. It returns nil without such a socket
// (the child reports a missing one), or if the relay cannot be created:
// the child then binds systemd's socket itself.
//
// The relay is created whether the child binds it or not (--no-systemd,
// --env NOTIFY_SOCKET=..., a --bind on its path): with --config, the
// supervising droot does not read the options.
func newNotifyRelay() *systemd.Relay {
	target := systemd.NotifySocketPath(os.Getenv(systemd.NotifySocket))
	if target == "" {
		return nil
	}
	if fi, err := os.Stat(target); err != nil || fi.Mode().Type() != os.ModeSocket { //nolint:gosec // G703: the socket systemd gave droot
		return nil
	}
	var dirs []string
	if d := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(d) {
		dirs = append(dirs, d)
	}
	dirs = append(dirs, os.TempDir())
	relay, err := systemd.NewRelay(target, dirs)
	if err != nil {
		log.Warnf("cannot relay the notifications of COMMAND to systemd (%v): COMMAND gets %s=%s itself, and its notifications need NotifyAccess=all", err, systemd.NotifySocket, target)
		return nil
	}
	log.Debugf("relaying the notifications of COMMAND to %s through %s", target, relay.Path())
	return relay
}

func closeRelay(relay *systemd.Relay) {
	if relay != nil {
		_ = relay.Close()
	}
}

// start starts the first stage in a new user namespace and writes its ID
// maps. release lets it go on. With ownGroup, the child is put in a process
// group of its own, so that the signals sent to the process group of droot
// (by timeout(1), kill -- -PGID, ...) reach it only once, passed on by droot.
// relay, if not nil, is the relay of the notifications of COMMAND, whose
// socket the child binds in the container.
//
// The sockets of socket activation keep their descriptors (3, 4, ...) in
// the child, and so do the other descriptors droot inherited; the pipe it
// waits on keeps its own number (see firstStageCommand).
func start(m Mapping, ownGroup bool, relay *systemd.Relay) (*exec.Cmd, func(), error) {
	sockets, err := listenFiles()
	if err != nil {
		return nil, nil, err
	}
	// The child has its own copies.
	defer closeFiles(sockets)
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	cmd, err := firstStageCommand(r, sockets, ownGroup)
	if err == nil && relay != nil {
		cmd.Env = append(cmd.Env, envNotifyRelay+"="+relay.Path())
	}
	if err == nil {
		err = cmd.Start()
		if err != nil {
			err = fmt.Errorf("create a user namespace: %w%s", err, disabledHint(err))
		}
	}
	// The child has its copy; the ID map helpers below must not get one.
	_ = r.Close()
	if err != nil {
		_ = w.Close()
		return nil, nil, err
	}
	if err := writeMaps(cmd.Process.Pid, &m); err != nil {
		_ = w.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, nil, err
	}
	log.Debugf("user namespace of droot %d: %s", cmd.Process.Pid, m)
	return cmd, func() {
		_, _ = w.Write([]byte{0})
		_ = w.Close()
	}, nil
}

// firstStageCommand returns the command of the first stage, which waits on
// r, the read end of a pipe. sockets, those of socket activation (see
// listenFiles), are passed in ExtraFiles, which keeps their numbers (3, 4,
// ...) and clears their close-on-exec flag in the child.
//
// The child inherits r at its own number, named by the environment, rather
// than after them in ExtraFiles: the other descriptors droot inherited
// without close-on-exec, such as the listening socket of server-starter
// (dewy, SERVER_STARTER_PORT=8080=3), must reach COMMAND as they are, with
// their numbers. os/exec leaves them open in the child, and droot does not
// touch them.
func firstStageCommand(r *os.File, sockets []*os.File, ownGroup bool) (*exec.Cmd, error) {
	fd := r.Fd()
	if _, err := unix.FcntlInt(fd, unix.F_SETFD, 0); err != nil {
		return nil, fmt.Errorf("create a user namespace: clear the close-on-exec flag of a pipe: %w", err)
	}
	cmd := exec.Command("/proc/self/exe", os.Args[1:]...) //nolint:gosec // G204: droot itself, with its own arguments
	cmd.Args[0] = os.Args[0]
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.ExtraFiles = sockets
	cmd.Env = append(os.Environ(), envSync+"="+strconv.FormatUint(uint64(fd), 10), envParent+"="+strconv.Itoa(os.Getpid()))
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER, Pdeathsig: syscall.SIGKILL, Setpgid: ownGroup}
	return cmd, nil
}

// listenFiles returns the sockets that socket activation passed to droot
// (descriptors 3, 4, ..., see systemd.ListenFDs), for the child. They are
// made close-on-exec here, so that the helpers droot runs (newuidmap,
// newgidmap) do not inherit them; os/exec clears the flag in the child.
//
// A descriptor passed to droot is not close-on-exec (it would not have
// survived the exec), while every one droot opens is: a LISTEN_FDS
// counting more than droot was passed is refused before droot hands over,
// and closes, descriptors of its own.
func listenFiles() ([]*os.File, error) {
	n := systemd.ListenFDs(os.LookupEnv, os.Getpid())
	first := systemd.FirstListenFD
	for fd := first; fd < first+n; fd++ {
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil {
			return nil, fmt.Errorf("socket activation: LISTEN_FDS is %d, but descriptor %d is not open: %w", n, fd, err)
		}
		if flags&unix.FD_CLOEXEC != 0 {
			return nil, fmt.Errorf("socket activation: LISTEN_FDS is %d, but descriptor %d was not passed to droot", n, fd)
		}
	}
	for fd := first; fd < first+n; fd++ {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
			return nil, err
		}
	}
	files := make([]*os.File, 0, n)
	for fd := first; fd < first+n; fd++ {
		files = append(files, os.NewFile(uintptr(fd), "socket "+strconv.Itoa(fd)))
	}
	if n > 0 {
		log.Debugf("passing the %d socket(s) of socket activation on to the user namespace", n)
	}
	return files, nil
}

func closeFiles(files []*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}

// disabledHint explains the usual reasons why user namespaces cannot be
// created.
func disabledHint(err error) string {
	if !errors.Is(err, unix.EPERM) && !errors.Is(err, unix.ENOSPC) && !errors.Is(err, unix.EACCES) {
		return ""
	}
	return " (unprivileged user namespaces may be disabled: see the sysctls user.max_user_namespaces, " +
		"kernel.unprivileged_userns_clone, and kernel.apparmor_restrict_unprivileged_userns on Ubuntu)"
}

// writeMaps writes the ID maps of the namespace of pid: with the helpers for
// subordinate IDs, or directly for the user's own IDs, which an
// unprivileged process may map (setgroups(2) must then be denied). If a
// helper fails, only the user's own ID is mapped, with a warning; m is
// updated.
func writeMaps(pid int, m *Mapping) error {
	proc := "/proc/" + strconv.Itoa(pid) + "/"
	write := func(name, content string) error {
		if err := os.WriteFile(proc+name, []byte(content), 0); err != nil {
			return fmt.Errorf("write the ID maps of the user namespace: %w", err)
		}
		return nil
	}
	for _, h := range []struct {
		helper, file string
		ranges       *[]Range
	}{{"newuidmap", "uid_map", &m.UIDs}, {"newgidmap", "gid_map", &m.GIDs}} {
		if m.Helpers {
			out, err := exec.Command(h.helper, helperArgs(pid, *h.ranges)...).CombinedOutput() //nolint:gosec // G204: numeric arguments
			if err == nil {
				continue
			}
			log.Warnf("%s failed (%v: %s): only your own ID is mapped in the user namespace", h.helper, err, strings.TrimSpace(string(out)))
			*h.ranges = (*h.ranges)[:1]
		}
		if h.file == "gid_map" {
			if err := write("setgroups", "deny"); err != nil {
				return err
			}
		}
		if err := write(h.file, mapString(*h.ranges)); err != nil {
			return err
		}
	}
	return nil
}

// forwarded are the signals passed on to the child.
var forwarded = []os.Signal{
	unix.SIGHUP, unix.SIGINT, unix.SIGQUIT, unix.SIGTERM, unix.SIGUSR1, unix.SIGUSR2,
	unix.SIGALRM, unix.SIGWINCH, unix.SIGCONT,
}

// fromTerminal are the signals a terminal sends to its whole foreground
// process group, the child included.
var fromTerminal = []os.Signal{unix.SIGHUP, unix.SIGINT, unix.SIGQUIT, unix.SIGWINCH, unix.SIGCONT}

// forward passes the signals of sigs on to p until done is closed. When
// droot is, at that moment, in the foreground of its terminal tty (if not
// negative), the signals the terminal sends are not passed on: the child,
// in the same process group, gets them too.
func forward(sigs <-chan os.Signal, done <-chan struct{}, p *os.Process, tty int) {
	for {
		select {
		case s := <-sigs:
			if tty >= 0 && containsSignal(fromTerminal, s) && inForeground(tty) {
				continue
			}
			_ = p.Signal(s)
		case <-done:
			return
		}
	}
}

func containsSignal(list []os.Signal, s os.Signal) bool {
	for _, l := range list {
		if l == s {
			return true
		}
	}
	return false
}

// terminal returns the first of the descriptors 0, 1 and 2 that is a
// terminal, or -1. The child then stays in the process group of droot, for
// job control (Ctrl-Z stops both).
func terminal() int {
	for fd := range 3 {
		if _, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP); err == nil {
			return fd
		}
	}
	return -1
}

// inForeground reports whether droot is in the foreground process group of
// the terminal on fd.
func inForeground(fd int) bool {
	pgrp, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	return err == nil && pgrp == unix.Getpgrp()
}

// dieFromSignal makes droot die from the signal that killed the child, so
// that its parent sees the same status (systemd counts a stop by SIGTERM as
// a success, not an exit status of 143). It returns for the signals the Go
// runtime keeps for itself (core dumps).
func dieFromSignal(s syscall.Signal) {
	switch s {
	case unix.SIGHUP, unix.SIGINT, unix.SIGTERM, unix.SIGKILL, unix.SIGUSR1, unix.SIGUSR2, unix.SIGALRM:
	default:
		return
	}
	signal.Reset(s)
	_ = unix.Kill(os.Getpid(), s)
	time.Sleep(100 * time.Millisecond)
}
