//go:build !unix

package osutil

import "io/fs"

// FileOwner is not supported on this platform.
func FileOwner(fs.FileInfo) (uid, gid, nlink int, ok bool) { return 0, 0, 0, false }
