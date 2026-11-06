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
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/yuuki/droot/internal/log"
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
		adoptListenPID(pid)
		if err := KeepParentDeathSignal(); err != nil {
			fmt.Fprintf(os.Stderr, "droot: %v\n", err)
			os.Exit(1)
		}
	}
}

// parent is the PID of the supervising droot, in the second stage.
var parent int

// adoptListenPID takes over the descriptors of systemd socket activation:
// systemd gives them to the process it starts, the supervising droot (PID
// parentPID), which passes them on to the second stage. LISTEN_PID then
// names the second stage, whose PID COMMAND keeps, so that
// sd_listen_fds(3) in COMMAND accepts them (with --env LISTEN_PID).
func adoptListenPID(parentPID string) {
	if os.Getenv("LISTEN_PID") == parentPID {
		_ = os.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	}
}

// initialized is set by Init: Run runs the program again, which must handle
// the stages.
var initialized bool

// Supervised reports whether droot is the second stage of a rootless droot,
// which supervises it.
func Supervised() bool { return parent != 0 }

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
// the namespace.
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
func Run(ctx context.Context, m Mapping) error {
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
	tty := terminal()
	cmd, release, err := start(m, tty < 0)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil { // interrupted while starting
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	done := make(chan struct{})
	go forward(sigs, done, cmd.Process, tty)
	release()
	err = cmd.Wait()
	close(done)
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

// start starts the first stage in a new user namespace and writes its ID
// maps. release lets it go on. With ownGroup, the child is put in a process
// group of its own, so that the signals sent to the process group of droot
// (by timeout(1), kill -- -PGID, ...) reach it only once, passed on by droot.
func start(m Mapping, ownGroup bool) (*exec.Cmd, func(), error) {
	// The descriptors droot inherited keep their numbers in the child, and
	// so in COMMAND; the pipe of the first stage comes after them. Once the
	// child is started, it has its own copies.
	inherited, err := inheritedFiles()
	if err != nil {
		return nil, nil, err
	}
	defer closeFiles(inherited)
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()
	cmd := exec.Command("/proc/self/exe", os.Args[1:]...) //nolint:gosec // G204: droot itself, with its own arguments
	cmd.Args[0] = os.Args[0]
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.ExtraFiles = slices.Concat(inherited, []*os.File{r})
	syncFD := strconv.Itoa(3 + len(inherited))
	cmd.Env = append(os.Environ(), envSync+"="+syncFD, envParent+"="+strconv.Itoa(os.Getpid()))
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER, Pdeathsig: syscall.SIGKILL, Setpgid: ownGroup}
	if err := cmd.Start(); err != nil {
		_ = w.Close()
		return nil, nil, fmt.Errorf("create a user namespace: %w%s", err, disabledHint(err))
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

// inheritedFiles returns the descriptors above 2 that droot inherited open
// across execve(2), i.e. without FD_CLOEXEC — the listening sockets of
// server-starter or of systemd socket activation, for instance — as
// exec.Cmd.ExtraFiles that keep their numbers: entry i is descriptor 3+i, nil
// for the numbers in between, which the child closes. Every descriptor droot
// opens itself has FD_CLOEXEC. os.NewFile leaves the descriptors in their
// blocking or non-blocking mode, which the child shares.
func inheritedFiles() ([]*os.File, error) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return nil, fmt.Errorf("list the open descriptors: %w", err)
	}
	var fds []int
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil || fd < 3 {
			continue
		}
		// Fails for the descriptor of the directory listed, closed since.
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil || flags&unix.FD_CLOEXEC != 0 {
			continue
		}
		fds = append(fds, fd)
	}
	if len(fds) == 0 {
		return nil, nil
	}
	slices.Sort(fds)
	files := make([]*os.File, fds[len(fds)-1]-2)
	for _, fd := range fds {
		files[fd-3] = os.NewFile(uintptr(fd), "inherited descriptor "+strconv.Itoa(fd))
	}
	log.Debugf("descriptors passed on to the user namespace: %v", fds)
	return files, nil
}

func closeFiles(files []*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
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
		if err := os.WriteFile(proc+name, []byte(content), 0); err != nil { //nolint:gosec // G703: /proc/PID/ of the child
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
