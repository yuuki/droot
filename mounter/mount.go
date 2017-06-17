package mounter

import (
	"os"
	fp "path/filepath"
	"strings"

	"github.com/moby/moby/pkg/fileutils"
	"github.com/moby/moby/pkg/mount"
	"github.com/pkg/errors"

	"github.com/yuuki/droot/log"
	"github.com/yuuki/droot/osutil"
)

type Mounter struct {
	rootDir string
}

func NewMounter(rootDir string) *Mounter {
	return &Mounter{rootDir: rootDir}
}

func ResolveRootDir(dir string) (string, error) {
	var err error

	if !osutil.ExistsDir(dir) {
		return dir, errors.Errorf("No such directory %s:", dir)
	}

	dir, err = fp.Abs(dir)
	if err != nil {
		return dir, err
	}

	if osutil.IsSymlink(dir) {
		dir, err = os.Readlink(dir)
		if err != nil {
			return dir, err
		}
	}

	return fp.Clean(dir), nil
}

func (m *Mounter) MountSysProc() error {
	// mount -t proc none {{rootDir}}/proc
	if err := osutil.MountIfNotMounted("none", fp.Join(m.rootDir, "/proc"), "proc", ""); err != nil {
		return errors.Errorf("Failed to mount /proc: %s", err)
	}
	// mount --rbind /sys {{rootDir}}/sys
	if err := osutil.MountIfNotMounted("/sys", fp.Join(m.rootDir, "/sys"), "none", "rbind"); err != nil {
		return errors.Errorf("Failed to mount /sys: %s", err)
	}
	// mount --make-rslave /sys {{rootDir}}/sys
	if err := osutil.ForceMount("/sys", fp.Join(m.rootDir, "/sys"), "none", "rslave"); err != nil {
		return errors.Errorf("Failed to mount --make-rslave /sys: %s", err)
	}

	return nil
}

func (m *Mounter) BindMount(hostDir, containerDir string) error {
	containerDir = fp.Join(m.rootDir, containerDir)

	if ok := osutil.IsDirEmpty(hostDir); ok {
		if _, err := os.Create(fp.Join(hostDir, ".droot.keep")); err != nil {
			return err
		}
	}

	if err := fileutils.CreateIfNotExists(containerDir, true); err != nil { // mkdir -p
		return err
	}

	if err := osutil.MountIfNotMounted(hostDir, containerDir, "none", "bind,rw"); err != nil {
		return err
	}

	return nil
}

func (m *Mounter) RoBindMount(hostDir, containerDir string) error {
	if err := m.BindMount(hostDir, containerDir); err != nil {
		return err
	}

	containerDir = fp.Join(m.rootDir, containerDir)

	if err := osutil.ForceMount(hostDir, containerDir, "none", "remount,ro,bind"); err != nil {
		return err
	}

	return nil
}

func (m *Mounter) GetMountsRoot() ([]*mount.Info, error) {
	mounts, err := mount.GetMounts()
	if err != nil {
		return nil, err
	}

	targets := make([]*mount.Info, 0)
	for _, mo := range mounts {
		if strings.HasPrefix(mo.Mountpoint, m.rootDir) {
			targets = append(targets, mo)
		}
	}

	return targets, nil
}

func (m *Mounter) UmountRoot() error {
	mounts, err := m.GetMountsRoot()
	if err != nil {
		return err
	}

	for _, mo := range mounts {
		if err := mount.Unmount(mo.Mountpoint); err != nil {
			return err
		}
		log.Debug("umount:", mo.Mountpoint)
	}

	return nil
}
