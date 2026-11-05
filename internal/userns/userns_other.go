//go:build !linux

package userns

import (
	"errors"
	"runtime"
)

var errUnsupported = errors.New("user namespaces are not supported on " + runtime.GOOS)

// Init does nothing outside Linux.
func Init() {}

// KeepParentDeathSignal does nothing outside Linux.
func KeepParentDeathSignal() error { return nil }

// CurrentMapping is only supported on Linux.
func CurrentMapping() (Mapping, error) { return Mapping{}, errUnsupported }

// Run is only supported on Linux.
func Run(Mapping) error { return errUnsupported }
