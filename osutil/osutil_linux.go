package osutil

import (
	"bufio"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"regexp"
	"syscall"

	"github.com/yuuki1/droot/log"
)

const (
	mountinfoFormat = "%d %d %d:%d %s %s %s %s"
)

func GetMountsByRoot(rootDir string) ([]string, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	mountpoints := make([]string, 0)
	re := regexp.MustCompile(fmt.Sprintf("^%s", rootDir))

	for s.Scan() {
		if err := s.Err(); err != nil {
			return nil, err
		}

		var (
			text           = s.Text()
			mountpoint string
			d1, d2, d3, d4 int
			s1, s2, s3 string
		)

		if _, err := fmt.Sscanf(text, mountinfoFormat, &d1, &d2,
			&d3, &d4, &s1, &mountpoint, &s2, &s3); err != nil {
			return nil, fmt.Errorf("Scanning '%s' failed: %s", text, err)
		}

		if !re.MatchString(mountpoint) {
			continue
		}

		mountpoints = append(mountpoints, mountpoint)
	}
	return mountpoints, nil
}

func UmountRoot(rootDir string) (err error) {
	mounts, err := GetMountsByRoot(rootDir)
	if err != nil {
		return err
	}

	for _, mount := range mounts {
		if err = Unmount(mount, syscall.MNT_DETACH|syscall.MNT_FORCE); err == nil {
			log.Debug("umount:", mount)
		}
	}
	return
}

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

