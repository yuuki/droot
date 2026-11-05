//go:build !linux

package osutil

import (
	"fmt"
	"runtime"
)

func unsupported(op string) error {
	return fmt.Errorf("%s is not supported on %s/%s: droot run requires Linux", op, runtime.GOOS, runtime.GOARCH)
}

// Chroot is only supported on Linux.
func Chroot(string) error { return unsupported("chroot") }

// DropBoundingCapabilities is only supported on Linux.
func DropBoundingCapabilities(map[uintptr]bool) error { return unsupported("dropping capabilities") }

// SetCredentials is only supported on Linux.
func SetCredentials(int, int, []int) error { return unsupported("changing credentials") }

// IsExecutable is only supported on Linux.
func IsExecutable(string) error { return unsupported("exec") }

// Exec is only supported on Linux.
func Exec(string, []string, []string) error { return unsupported("exec") }
