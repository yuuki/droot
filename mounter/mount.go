package mounter

import (
	"fmt"
	"os"
	fp "path/filepath"

	"github.com/docker/docker/pkg/fileutils"

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
		return dir, fmt.Errorf("No such directory %s:", dir)
	}

	dir, err = fp.Abs(dir)
	if err != nil {
		return dir, err
	}

	dir, err = os.Readlink(dir)
	if err != nil {
		return dir, err
	}

	return fp.Clean(dir), nil
}

func (m *Mounter) MountSysProc() error {
	// mount -t proc none {{rootDir}}/proc
	if err := osutil.MountIfNotMounted("none", fp.Join(m.rootDir, "/proc"), "proc", ""); err != nil {
		return fmt.Errorf("Failed to mount /proc: %s", err)
	}
	// mount --rbind /sys {{rootDir}}/sys
	if err := osutil.MountIfNotMounted("/sys", fp.Join(m.rootDir, "/sys"), "none", "rbind"); err != nil {
		return fmt.Errorf("Failed to mount /sys: %s", err)
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
	containerDir = fp.Join(m.rootDir, containerDir)

	if err := m.BindMount(hostDir, containerDir); err != nil {
		return err
	}

	if err := osutil.MountIfNotMounted(hostDir, containerDir, "none", "remount,ro,bind"); err != nil {
		return err
	}

	return nil
}

