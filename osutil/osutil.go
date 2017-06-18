package osutil

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/docker/docker/pkg/mount"

	"github.com/yuuki/droot/log"
)

func ExistsFile(file string) bool {
	f, err := os.Stat(file)
	return err == nil && !f.IsDir()
}

func IsSymlink(file string) bool {
	f, err := os.Lstat(file)
	return err == nil && f.Mode()&os.ModeSymlink == os.ModeSymlink
}

func ExistsDir(dir string) bool {
	if f, err := os.Stat(dir); os.IsNotExist(err) || !f.IsDir() {
		return false
	}
	return true
}

func IsDirEmpty(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		log.Debugf("Failed to open %s: %s\n", dir, err)
		return false
	}
	defer f.Close()

	_, err = f.Readdirnames(1)
	if err == io.EOF {
		return true
	}
	return false
}

func RunCmd(name string, arg ...string) error {
	log.Debug("runcmd: ", name, arg)
	out, err := exec.Command(name, arg...).CombinedOutput()
	if len(out) > 0 {
		log.Debug(string(out))
	}
	if err != nil {
		log.Debugf("Failed to exec %s %s: %s", name, arg, err)
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

func MountIfNotMounted(device, target, mType, options string) error {
	mounted, err := mount.Mounted(target)
	if err != nil {
		return err
	}

	if !mounted {
		log.Debug("mount", device, target, mType, options)
		if err := mount.Mount(device, target, mType, options); err != nil {
			return err
		}
	}

	return nil
}

func ForceMount(device, target, mType, options string) error {
	log.Debug("mount", device, target, mType, options)
	if err := mount.ForceMount(device, target, mType, options); err != nil {
		return err
	}

	return nil
}

// Mknod unless path does not exists.
func Mknod(path string, mode uint32, dev int) error {
	if ExistsFile(path) {
		return nil
	}

	log.Debugf("mknod %s %d %d", path, mode, dev)
	if err := unix.Mknod(path, mode, dev); err != nil {
		return err
	}
	return nil
}

// Symlink, but ignore already exists file.
func Symlink(oldname, newname string) error {
	log.Debug("symlink", oldname, newname)
	if err := os.Symlink(oldname, newname); err != nil {
		// Ignore already created symlink
		if _, ok := err.(*os.LinkError); !ok {
			log.Debugf("Failed to symlink %s %s: %s", oldname, newname, err)
			return err
		}
	}
	return nil
}

func Chroot(rootDir string) error {
	log.Debug("chroot", rootDir)

	if err := unix.Chroot(rootDir); err != nil {
		return err
	}
	if err := unix.Chdir("/"); err != nil {
		return err
	}

	return nil
}

// CreateIfNotExists creates a file or a directory only if it does not already exist.
//
// Apache License 2.0 https://github.com/moby/moby/blob/master/LICENSE
// Copyright 2013-2017 Docker, Inc.
// https://github.com/moby/moby/blob/89658bed6/pkg/fileutils/fileutils.go#L281
func CreateIfNotExists(path string, isDir bool) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			if isDir {
				return os.MkdirAll(path, 0755)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(path, os.O_CREATE, 0755)
			if err != nil {
				return err
			}
			f.Close()
		}
	}
	return nil
}
