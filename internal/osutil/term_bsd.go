//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package osutil

import "golang.org/x/sys/unix"

const ioctlReadTermios = unix.TIOCGETA
