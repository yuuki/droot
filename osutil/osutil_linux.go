package osutil

import (
	"os/exec"
	"os/user"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/pkg/errors"

	"github.com/yuuki/droot/log"
)

func LookupGroup(id string) (int, error) {
	var g *user.Group

	if _, err := strconv.Atoi(id); err == nil {
		g, err = user.LookupGroupId(id)
		if err != nil {
			return -1, err
		}
	} else {
		g, err = user.LookupGroup(id)
		if err != nil {
			return -1, err
		}
	}

	return strconv.Atoi(g.Gid)
}

func LookupUser(id string) (int, error) {
	var u *user.User

	if _, err := strconv.Atoi(id); err == nil {
		u, err = user.LookupId(id)
		if err != nil {
			return -1, err
		}
	} else {
		u, err = user.Lookup(id)
		if err != nil {
			return -1, err
		}
	}

	return strconv.Atoi(u.Uid)
}

// Setuid sets the uid of the calling thread to the specified uid.
func Setuid(uid int) (err error) {
	_, _, e1 := syscall.RawSyscall(syscall.SYS_SETUID32, uintptr(uid), 0, 0)
	if e1 != 0 {
		err = e1
	}
	return
}

// Setgid sets the gid of the calling thread to the specified gid.
func Setgid(gid int) (err error) {
	_, _, e1 := syscall.RawSyscall(syscall.SYS_SETGID32, uintptr(gid), 0, 0)
	if e1 != 0 {
		err = e1
	}
	return
}

func DropCapabilities(keepCaps map[uint]bool) error {
	var i uint
	for i = 0; ; i++ {
		if keepCaps[i] {
			continue
		}
		log.Debug("prctl", "PR_CAPBSET_READ", i)
		if err := unix.Prctl(unix.PR_CAPBSET_READ, uintptr(i), 0, 0, 0); err != nil {
			// Regard EINVAL as the condition of loop finish.
			if errno, ok := err.(syscall.Errno); ok && errno == unix.EINVAL {
				break
			}
			return err
		}
		log.Debug("prctl", "PR_CAPBSET_DROP", i)
		if err := unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(i), 0, 0, 0); err != nil {
			// Ignore EINVAL since the capability may not be supported in this system.
			if errno, ok := err.(syscall.Errno); ok && errno == unix.EINVAL {
				continue
			} else if errno, ok := err.(syscall.Errno); ok && errno == unix.EPERM {
				return errors.New("required CAP_SETPCAP capabilities")
			} else {
				return err
			}
		}
	}

	if i == 0 {
		return errors.New("Failed to drop capabilities")
	}

	return nil
}

func Execv(cmd string, args []string, env []string) error {
	name, err := exec.LookPath(cmd)
	if err != nil {
		return err
	}

	log.Debug("execv", name, args)

	return syscall.Exec(name, args, env)
}
