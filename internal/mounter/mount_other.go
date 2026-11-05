//go:build !linux

package mounter

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/yuuki/droot/internal/osutil"
)

// ErrOtherMount is returned by the check of OnMountOf.
var ErrOtherMount = errors.New("is on another mount than the container root")

// OnMountOf is only supported on Linux.
func OnMountOf(int) osutil.DirCheck { return func(int) error { return unsupported() } }

func unsupported() error {
	return fmt.Errorf("mounting is not supported on %s/%s: droot run and droot umount require Linux", runtime.GOOS, runtime.GOARCH)
}

// MountSysProc is only supported on Linux.
func (m *Mounter) MountSysProc() error { return unsupported() }

// ProtectKernelFS is only supported on Linux.
func (m *Mounter) ProtectKernelFS() error { return unsupported() }

// BindMount is only supported on Linux.
func (m *Mounter) BindMount(string, string, BindOptions) error { return unsupported() }

// MountFS is only supported on Linux.
func (m *Mounter) MountFS(string, string, FSOptions, osutil.DirCheck) error { return unsupported() }

// Rollback is only supported on Linux.
func (m *Mounter) Rollback() error { return unsupported() }

// Release is only supported on Linux.
func (m *Mounter) Release() {}

// BindRootOnItself is only supported on Linux.
func BindRootOnItself(string) error { return unsupported() }

// RemountRootReadonly is only supported on Linux.
func (m *Mounter) RemountRootReadonly() error { return unsupported() }

// MountInfos is only supported on Linux.
func (m *Mounter) MountInfos() ([]MountInfo, error) { return nil, unsupported() }

// Mounts is only supported on Linux.
func (m *Mounter) Mounts() ([]string, error) { return nil, unsupported() }

// UmountAll is only supported on Linux.
func (m *Mounter) UmountAll() ([]string, error) { return nil, unsupported() }
