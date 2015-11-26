package osutil

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"syscall"
)

func DropCapabilities(keepCaps map[uint]bool) error {
	var i uint
	for i = 0; ; i++ {
		if keepCaps[i] {
			continue
		}
		if err := unix.Prctl(syscall.PR_CAPBSET_READ, uintptr(i), 0, 0, 0); err != nil {
			// Regard EINVAL as the condition of loop finish.
			if errno, ok := err.(syscall.Errno); ok && errno == syscall.EINVAL {
				break
			}
			return err
		}
		if err := unix.Prctl(syscall.PR_CAPBSET_DROP, uintptr(i), 0, 0, 0); err != nil {
			// Ignore EINVAL since the capability may not be supported in this system.
			if errno, ok := err.(syscall.Errno); ok && errno == syscall.EINVAL {
				continue
			} else if errno, ok := err.(syscall.Errno); ok && errno == syscall.EPERM {
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

func ChrootAndExec(keepCaps map[uint]bool, rootDir string, command ...string) error {
	if err := syscall.Chroot(rootDir); err != nil {
		return err
	}
	if err := syscall.Chdir("/"); err != nil {
		return err
	}
	if err := DropCapabilities(keepCaps); err != nil {
		return err
	}
	return Execv(command[0], command[0:], os.Environ())
}
