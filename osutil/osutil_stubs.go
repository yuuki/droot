// +build darwin freebsd windows !cgo,!linux

package osutil

import (
	"fmt"
	"runtime"
)

func GetMountsByRoot(rootDir string) ([]string, error) {
	return nil, fmt.Errorf("osutil: GetMountsByRoot not implemented on %s/%s", runtime.GOOS, runtime.GOARCH)
}

func UmountRoot(rootDir string) (err error) {
	return fmt.Errorf("osutil: UmountRoot not implemented on %s/%s", runtime.GOOS, runtime.GOARCH)
}

func LookupGroup(id string) (int, error) {
	return -1, fmt.Errorf("osutil: LookupGroup not implemented on %s/%s", runtime.GOOS, runtime.GOARCH)
}

func SetGroup(id string) error {
	return fmt.Errorf("osutil: SetGroup not implemented on %s/%s", runtime.GOOS, runtime.GOARCH)
}

func LookupUser(id string) error {
	return fmt.Errorf("osutil: LookupUser not implemented on %s/%s", runtime.GOOS, runtime.GOARCH)
}

func SetUser(id string) error {
	return fmt.Errorf("osutil: SetUser not implemented on %s/%s", runtime.GOOS, runtime.GOARCH)
}

func DropCapabilities(keepCaps map[uint]bool) error {
	return fmt.Errorf("osutil: DropCapabilities not implemented on %s/%s", runtime.GOOS, runtime.GOARCH)
}
