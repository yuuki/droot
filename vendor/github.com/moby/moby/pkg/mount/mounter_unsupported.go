// +build !linux,!freebsd,!solaris freebsd,!cgo solaris,!cgo

// Apache License 2.0 https://github.com/moby/moby/blob/master/LICENSE
// Copyright 2013-2017 Docker, Inc.
// https://github.com/moby/moby/blob/89658bed6/pkg/mount/mounter_unsupported.go

package mount

func mount(device, target, mType string, flag uintptr, data string) error {
	panic("Not implemented")
}

func unmount(target string, flag int) error {
	panic("Not implemented")
}
