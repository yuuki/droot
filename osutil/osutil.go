package osutil

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	fp "path/filepath"
	"syscall"
	"time"

	"github.com/hashicorp/errwrap"

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
		return false, errwrap.Wrapf(fmt.Sprintf("Failed to open %s", dir), err)
	}
	defer f.Close()

	_, err = f.Readdir(1)
	if err == io.EOF {
		return true, errwrap.Wrapf(fmt.Sprintf("Failed to readdir %s", dir), err)
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

func BindMount(src, dest string) error {
	if err := RunCmd("mount", "--bind", src, dest); err != nil {
		return err
	}
	return nil
}

func RObindMount(src, dest string) error {
	if err := RunCmd("mount", "-o", "remount,ro,bind", src, dest); err != nil {
		return err
	}
	return nil
}

func Mounted(mountpoint string) (bool, error) {
	mntpoint, err := os.Stat(mountpoint)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	parent, err := os.Stat(fp.Join(mountpoint, ".."))
	if err != nil {
		return false, err
	}
	mntpointSt := mntpoint.Sys().(*syscall.Stat_t)
	parentSt := parent.Sys().(*syscall.Stat_t)
	return mntpointSt.Dev != parentSt.Dev, nil
}

// Unmount will unmount the target filesystem, so long as it is mounted.
func Unmount(target string, flag int) error {
	if mounted, err := Mounted(target); err != nil || !mounted {
		return errwrap.Wrapf(fmt.Sprintf("Failed to unmount %s: {{err}}", target), err)
	}
	return ForceUnmount(target, flag)
}

// ForceUnmount will force an unmount of the target filesystem, regardless if
// it is mounted or not.
func ForceUnmount(target string, flag int) (err error) {
	// Simple retry logic for unmount
	for i := 0; i < 10; i++ {
		if err = syscall.Unmount(target, flag); err == nil {
			return errwrap.Wrapf(fmt.Sprintf("Failed to force unmount %s: {{err}}", target), err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return
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
