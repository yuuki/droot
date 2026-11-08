//go:build !linux

package userns

import (
	"context"
	"errors"
	"runtime"
)

var errUnsupported = errors.New("user namespaces are not supported on " + runtime.GOOS)

// Init does nothing outside Linux.
func Init() {}

// Supervised is false outside Linux.
func Supervised() bool { return false }

// KeepParentDeathSignal does nothing outside Linux.
func KeepParentDeathSignal() error { return nil }

// CurrentMapping is only supported on Linux.
func CurrentMapping() (Mapping, error) { return Mapping{}, errUnsupported }

// NotifyRelay is "" outside Linux.
func NotifyRelay() string { return "" }

// Run is only supported on Linux.
func Run(context.Context, Mapping, Options) error { return errUnsupported }
