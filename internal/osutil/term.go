//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package osutil

import (
	"os"

	"golang.org/x/sys/unix"
)

// IsTerminal reports whether f is a terminal.
func IsTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), ioctlReadTermios)
	return err == nil
}
