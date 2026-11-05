//go:build !linux

package osutil

func rlimitResource(string) (int, bool) { return 0, false }

// RlimitNames returns the names accepted by ParseRlimit: none, resource
// limits are only supported on Linux.
func RlimitNames() []string { return nil }

// SetRlimits is only supported on Linux.
func SetRlimits([]Rlimit) error { return unsupported("resource limits") }
