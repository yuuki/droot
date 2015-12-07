// +build darwin freebsd windows !cgo,!linux

package osutil

import (
	"fmt"
	"runtime"
)

func LookupGroup(id string) (int, error) {
	return -1, fmt.Errorf("osutil: LookupGroup not implemented on %s/%s", runtime.GOOS, runtime.GOARCH)
}

func Setgid(id int) error {
	return fmt.Errorf("osutil: Setgid not implemented on %s/%s", runtime.GOOS, runtime.GOARCH)
}

func LookupUser(id string) (int, error) {
	return -1, fmt.Errorf("osutil: LookupUser not implemented on %s/%s", runtime.GOOS, runtime.GOARCH)
}

func Setuid(id int) error {
	return fmt.Errorf("osutil: Setuid not implemented on %s/%s", runtime.GOOS, runtime.GOARCH)
}

func DropCapabilities(keepCaps map[uint]bool) error {
	return fmt.Errorf("osutil: DropCapabilities not implemented on %s/%s", runtime.GOOS, runtime.GOARCH)
}
