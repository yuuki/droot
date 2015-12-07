package osutil

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	fp "path/filepath"
	"strings"
	"syscall"

	"github.com/hashicorp/errwrap"
	"github.com/docker/docker/pkg/mount"

	"github.com/yuuki1/droot/log"
)

func ExistsFile(file string) bool {
	f, err := os.Stat(file)
	return err == nil && !f.IsDir()
}

func ExistsDir(dir string) bool {
	if f, err := os.Stat(dir); os.IsNotExist(err) || !f.IsDir() {
		return false
	}
	return true
}

func IsDirEmpty(dir string) (bool, error) {
	f, err := os.Open(dir)
	if err != nil {
		return false, errwrap.Wrapf(fmt.Sprintf("Failed to open %s: {{err}}", dir), err)
	}
	defer f.Close()

	_, err = f.Readdir(1)
	if err == io.EOF {
		return true, nil
	}
	return false, err // Either not empty or error, suits both cases
}

func RunCmd(name string, arg ...string) error {
	log.Debug("runcmd: ", name, arg)
	out, err := exec.Command(name, arg...).CombinedOutput()
	if len(out) > 0 {
		log.Debug(string(out))
	}
	if err != nil {
		return errwrap.Wrapf(fmt.Sprintf("Failed to exec %s %s: {{err}}", name, arg), err)
	}
	return nil
}

func Cp(from, to string) error {
	if err := RunCmd("cp", "-p", from, to); err != nil {
		return err
	}
	return nil
}

func GetMountsByRoot(rootDir string) ([]*mount.Info, error) {
	mounts, err := mount.GetMounts()
	if err != nil {
		return nil, err
	}

	targets := make([]*mount.Info, 0)
	for _, m := range mounts {
		if strings.HasPrefix(m.Mountpoint, fp.Clean(rootDir)) {
			targets = append(targets, m)
		}
	}

	return targets, nil
}

func UmountRoot(rootDir string) error {
	mounts, err := GetMountsByRoot(rootDir)
	if err != nil {
		return err
	}

	for _, m := range mounts {
		if err := mount.Unmount(m.Mountpoint); err != nil {
			return err
		}
		log.Debug("umount:", m.Mountpoint)
	}

	return nil
}

func BindMount(src, dest string) error {
	return mount.Mount(src, dest, "bind", "")
}

func RObindMount(src, dest string) error {
	return mount.Mount(src, dest, "bind", "remount,ro,bind")
}

// Mknod unless path does not exists.
func Mknod(path string, mode uint32, dev int) error {
	if ExistsFile(path) {
		return nil
	}
	if err := syscall.Mknod(path, mode, dev); err != nil {
		return errwrap.Wrapf(fmt.Sprintf("Failed to mknod %s: {{err}}", path), err)
	}
	return nil
}

// Symlink, but ignore already exists file.
func Symlink(oldname, newname string) error {
	if err := os.Symlink(oldname, newname); err != nil {
		// Ignore already created symlink
		if _, ok := err.(*os.LinkError); !ok {
			return errwrap.Wrapf(fmt.Sprintf("Failed to symlink %s %s: {{err}}", oldname, newname), err)
		}
	}
	return nil
}

func Execv(cmd string, args []string, env []string) error {
	name, err := exec.LookPath(cmd)
	if err != nil {
		return errwrap.Wrapf(fmt.Sprintf("Not found %s: {{err}}", cmd), err)
	}

	log.Debug("exec: ", name, args)

	return syscall.Exec(name, args, env)
}
