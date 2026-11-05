package userns

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"runtime"
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
	if fd := os.Getenv(envSync); fd != "" {
		firstStage(fd)
	}
	if pid := os.Getenv(envParent); pid != "" {
		_ = os.Unsetenv(envParent)
		parent, _ = strconv.Atoi(pid)
		if err := KeepParentDeathSignal(); err != nil {
			fmt.Fprintf(os.Stderr, "droot: %v\n", err)
			os.Exit(1)
		}
	}
}

// parent is the PID of the supervising droot, in the second stage.
var parent int

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
func Run(m Mapping) error {
	// The child is killed when the thread that started it exits: keep it.
	runtime.LockOSThread()
	cmd, release, err := start(m)
	if err != nil {
		return err
	}
	stop := forwardSignals(cmd.Process)
	release()
	err = cmd.Wait()
	stop()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		if err != nil {
			return err
		}
		return nil
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
// maps. release lets it go on.
func start(m Mapping) (*exec.Cmd, func(), error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()
	cmd := exec.Command("/proc/self/exe", os.Args[1:]...) //nolint:gosec // G204: droot itself, with its own arguments
	cmd.Args[0] = os.Args[0]
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{r}
	cmd.Env = append(os.Environ(), envSync+"=3", envParent+"="+strconv.Itoa(os.Getpid()))
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER, Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		_ = w.Close()
		return nil, nil, fmt.Errorf("create a user namespace: %w%s", err, disabledHint(err))
	}
	if err := writeMaps(cmd.Process.Pid, m); err != nil {
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
// unprivileged process may map (setgroups(2) must then be denied).
func writeMaps(pid int, m Mapping) error {
	if m.Helpers {
		for _, h := range []struct {
			name   string
			ranges []Range
		}{{"newuidmap", m.UIDs}, {"newgidmap", m.GIDs}} {
			out, err := exec.Command(h.name, helperArgs(pid, h.ranges)...).CombinedOutput() //nolint:gosec // G204: numeric arguments
			if err != nil {
				return fmt.Errorf("%s: %w: %s", h.name, err, strings.TrimSpace(string(out)))
			}
		}
		return nil
	}
	proc := "/proc/" + strconv.Itoa(pid) + "/"
	for _, f := range []struct{ name, content string }{
		{"setgroups", "deny"},
		{"uid_map", mapString(m.UIDs)},
		{"gid_map", mapString(m.GIDs)},
	} {
		if err := os.WriteFile(proc+f.name, []byte(f.content), 0); err != nil { //nolint:gosec // G703: /proc/PID/ of the child
			return fmt.Errorf("write the ID maps of the user namespace: %w", err)
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
var fromTerminal = []os.Signal{unix.SIGINT, unix.SIGQUIT, unix.SIGWINCH, unix.SIGCONT}

// forwardSignals passes the signals droot receives on to p until stop is
// called. When droot is in the foreground of its terminal, the signals the
// terminal sends are not passed on: the child, in the same process group,
// gets them too.
func forwardSignals(p *os.Process) (stop func()) {
	ch := make(chan os.Signal, 16)
	signal.Notify(ch, forwarded...)
	foreground := inForeground()
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-ch:
				if foreground && containsSignal(fromTerminal, s) {
					continue
				}
				_ = p.Signal(s)
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
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

// inForeground reports whether droot is in the foreground process group of
// the terminal on STDIN.
func inForeground() bool {
	pgrp, err := unix.IoctlGetInt(0, unix.TIOCGPGRP)
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
