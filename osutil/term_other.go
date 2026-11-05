//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package osutil

import "os"

// IsTerminal always reports false on platforms without termios.
func IsTerminal(*os.File) bool { return false }
