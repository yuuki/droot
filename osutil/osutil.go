package osutil

import (
	"compress/gzip"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"os/user"
	fp "path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/docker/libcontainer/system"
	"github.com/yuuki1/go-group"

	"github.com/yuuki1/dochroot/log"
)

var RsyncDefaultOpts = []string{"-av", "--delete"}

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
		return false, err
	}
	defer f.Close()

	_, err = f.Readdir(1)
	if err == io.EOF {
		return true, nil
	}
	return false, err // Either not empty or error, suits both cases
}

func RunCmd(name string, arg ...string) error {
	out, err := exec.Command(name, arg...).CombinedOutput()
	if len(out) > 0 {
		log.Debug(string(out))
	}
	if err != nil {
		log.Errorf("failed: %s %s", name, arg)
		return err
	}
	log.Debug("runcmd: ", name, arg)
	return nil
}

func Gzip(destWriter io.Writer, srcReader io.Reader) error {
	w := gzip.NewWriter(destWriter)
	defer w.Close()

	bytes, err := ioutil.ReadAll(srcReader)
	if err != nil {
		return err
	}

	nBytes, err := w.Write(bytes)
	if err != nil {
		return err
	}
	log.Debug("gzip bytes", nBytes)

	return nil
}

func ExtractTarGz(filePath string) error {
	if err := RunCmd("tar", "xf", filePath); err != nil {
		return err
	}

	if err := os.Chmod(filePath, os.FileMode(0755)); err != nil {
		return err
	}

	return nil
}

func Rsync(from, to string, arg ...string) error {
	from = from + "/"
	// append "/" when not terminated by "/"
	if strings.LastIndex(to, "/") != len(to)-1 {
		to = to + "/"
	}

	// TODO --exclude, --excluded-from
	rsyncArgs := []string{}
	rsyncArgs = append(rsyncArgs, RsyncDefaultOpts...)
	rsyncArgs = append(rsyncArgs, from, to)
	if err := RunCmd("rsync", rsyncArgs...); err != nil {
		return err
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
		return err
	}
	return ForceUnmount(target, flag)
}

// ForceUnmount will force an unmount of the target filesystem, regardless if
// it is mounted or not.
func ForceUnmount(target string, flag int) (err error) {
	// Simple retry logic for unmount
	for i := 0; i < 10; i++ {
		if err = syscall.Unmount(target, flag); err == nil {
			return err
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
		return err
	}
	return nil
}

// Symlink, but ignore already exists file.
func Symlink(oldname, newname string) error {
	if err := os.Symlink(oldname, newname); err != nil {
		// Ignore already created symlink
		if _, ok := err.(*os.LinkError); !ok {
			return err
		}
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

func SetGroup(id string) error {
	gid, err := LookupGroup(id)
	if err != nil {
		return err
	}
	return system.Setgid(gid)
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

func SetUser(id string) error {
	uid, err := LookupUser(id)
	if err != nil {
		return err
	}
	return system.Setuid(uid)
}

