package osutil

import (
	"errors"
	"golang.org/x/sys/unix"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"

	"github.com/docker/libcontainer/system"
	"github.com/yuuki1/go-group"

	"github.com/yuuki1/droot/log"
)

func LookupGroup(id string) (int, error) {
	var g *group.Group

	if _, err := strconv.Atoi(id); err == nil {
		g, err = group.LookupId(id)
		if err != nil {
			return -1, err
		}
	} else {
		g, err = group.Lookup(id)
		if err != nil {
			return -1, err
		}
	}

	return strconv.Atoi(g.Gid)
}

func Setgid(id int) error {
	return system.Setgid(id)
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

func Setuid(id int) error {
	return system.Setuid(id)
}

func DropCapabilities(keepCaps map[uint]bool) error {
	var i uint
	for i = 0; ; i++ {
		if keepCaps[i] {
			continue
		}
		if err := unix.Prctl(unix.PR_CAPBSET_READ, uintptr(i), 0, 0, 0); err != nil {
			// Regard EINVAL as the condition of loop finish.
			if errno, ok := err.(syscall.Errno); ok && errno == unix.EINVAL {
				break
			}
			return err
		}
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

	log.Debug("exec: ", name, args)

	return syscall.Exec(name, args, env)
}
