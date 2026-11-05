//go:build unix

package osutil

import (
	"io/fs"
	"syscall"
)

// FileOwner returns the owner and the number of hard links of a file.
func FileOwner(fi fs.FileInfo) (uid, gid, nlink int, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, 0, false
	}
	return int(st.Uid), int(st.Gid), int(st.Nlink), true
}
