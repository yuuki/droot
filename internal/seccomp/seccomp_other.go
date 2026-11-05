//go:build !linux || !(amd64 || arm64 || arm || 386)

package seccomp

// Load is only supported on Linux on amd64, arm64, arm and 386.
func Load() error { return ErrUnsupported }
