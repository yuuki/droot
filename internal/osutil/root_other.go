//go:build !linux

package osutil

import (
	"errors"
	"io/fs"
	"os"
)

// ErrIsRoot is returned when a path resolves to the root directory itself
// where a path below it is required.
var ErrIsRoot = errors.New("path resolves to the container root directory itself")

// Root is only supported on Linux.
type Root struct{}

// DirCheck is called for every directory traversed while resolving a path.
type DirCheck func(fd int) error

// OpenRoot is only supported on Linux.
func OpenRoot(string) (*Root, error) { return nil, unsupported("opening a container root") }

// Close is only supported on Linux.
func (*Root) Close() error { return unsupported("closing a container root") }

// Path is only supported on Linux.
func (*Root) Path() string { return "" }

// Fd is only supported on Linux.
func (*Root) Fd() int { return -1 }

// FdPath is only supported on Linux.
func FdPath(int) string { return "" }

// OpenDir is only supported on Linux.
func (*Root) OpenDir(string, bool, DirCheck) (*os.File, error) { return nil, unsupported("OpenDir") }

// MountTarget is only supported on Linux.
type MountTarget struct{ File *os.File }

// Close is only supported on Linux.
func (*MountTarget) Close() error { return unsupported("MountTarget") }

// ParentFd is only supported on Linux.
func (*MountTarget) ParentFd() int { return -1 }

// Reopen is only supported on Linux.
func (*MountTarget) Reopen() (*os.File, error) { return nil, unsupported("MountTarget") }

// OpenMountTarget is only supported on Linux.
func (*Root) OpenMountTarget(string, bool, DirCheck) (*MountTarget, error) {
	return nil, unsupported("OpenMountTarget")
}

// OpenMountTargetWithin is only supported on Linux.
func (*Root) OpenMountTargetWithin(string, bool, DirCheck, DirCheck) (*MountTarget, error) {
	return nil, unsupported("OpenMountTargetWithin")
}

// OpenNoFollow is only supported on Linux.
func (*Root) OpenNoFollow(string) (*os.File, error) { return nil, unsupported("OpenNoFollow") }

// Stat is only supported on Linux.
func (*Root) Stat(string) (fs.FileInfo, error) { return nil, unsupported("Stat") }

// OpenFile is only supported on Linux.
func (*Root) OpenFile(string) (*os.File, error) { return nil, unsupported("OpenFile") }

// MknodChar is only supported on Linux.
func (*Root) MknodChar(string, uint32, uint32, os.FileMode, DirCheck) error {
	return unsupported("mknod")
}

// SymlinkIfMissing is only supported on Linux.
func (*Root) SymlinkIfMissing(string, string, DirCheck) error { return unsupported("symlink") }

// EnsureSymlink is only supported on Linux.
func (*Root) EnsureSymlink(string, string, DirCheck) error { return unsupported("symlink") }

// CopyFileIn is only supported on Linux.
func (*Root) CopyFileIn(string, string, DirCheck) error { return unsupported("copy") }
