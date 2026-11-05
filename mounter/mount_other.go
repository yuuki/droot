//go:build !linux

package mounter

import (
	"fmt"
	"runtime"
)

func unsupported() error {
	return fmt.Errorf("mounting is not supported on %s/%s: droot run and droot umount require Linux", runtime.GOOS, runtime.GOARCH)
}

// MountSysProc is only supported on Linux.
func (m *Mounter) MountSysProc() error { return unsupported() }

// BindMount is only supported on Linux.
func (m *Mounter) BindMount(string, string, bool) error { return unsupported() }

// Mounts is only supported on Linux.
func (m *Mounter) Mounts() ([]string, error) { return nil, unsupported() }

// UmountAll is only supported on Linux.
func (m *Mounter) UmountAll() ([]string, error) { return nil, unsupported() }
