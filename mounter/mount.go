// Package mounter manages the mounts droot sets up under a container root.
package mounter

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/yuuki/droot/osutil"
)

// Mounter mounts and unmounts file systems under a container root directory.
type Mounter struct {
	rootDir string
	root    *osutil.Root
}

// NewMounter returns a Mounter for root, whose path must have been resolved
// with ResolveRootDir. All mount targets are resolved through root, so that
// symlinks in the container cannot redirect a mount outside of it.
func NewMounter(root *osutil.Root) *Mounter {
	return &Mounter{rootDir: root.Path(), root: root}
}

// RootDir returns the container root directory.
func (m *Mounter) RootDir() string { return m.rootDir }

// ResolveRootDir returns the absolute path of dir with all symlinks resolved,
// which is how mount points appear in /proc/self/mountinfo. It refuses "/".
func ResolveRootDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("no such directory: %s", dir)
		}
		return "", err
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("not a directory: %s", dir)
	}
	if resolved == "/" {
		return "", errors.New("refusing to use / as the container root directory")
	}
	return resolved, nil
}

// MountInfo is an entry of /proc/<pid>/mountinfo (see proc(5)).
type MountInfo struct {
	ID         int
	Parent     int
	Dev        string // major:minor of the file system
	Root       string
	Mountpoint string
	Options    string
	Optional   string // optional fields such as "shared:1 master:2"
	FSType     string
	Source     string
}

// ParseMountInfo parses the content of /proc/<pid>/mountinfo.
func ParseMountInfo(r io.Reader) ([]MountInfo, error) {
	var mounts []MountInfo
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for s.Scan() {
		line := s.Text()
		if line == "" {
			continue
		}
		// 36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw,errors=continue
		pre, post, ok := strings.Cut(line, " - ")
		if !ok {
			return nil, fmt.Errorf("malformed mountinfo line: %q", line)
		}
		f := strings.Fields(pre)
		g := strings.Fields(post)
		if len(f) < 6 || len(g) < 2 {
			return nil, fmt.Errorf("malformed mountinfo line: %q", line)
		}
		id, err1 := strconv.Atoi(f[0])
		parent, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("malformed mountinfo line: %q", line)
		}
		mounts = append(mounts, MountInfo{
			ID:         id,
			Parent:     parent,
			Dev:        f[2],
			Root:       unescape(f[3]),
			Mountpoint: unescape(f[4]),
			Options:    f[5],
			Optional:   strings.Join(f[6:], " "),
			FSType:     unescape(g[0]),
			Source:     unescape(g[1]),
		})
	}
	return mounts, s.Err()
}

// unescape decodes the octal escapes (\040 for a space, \011, \012, \134)
// used by the kernel in mountinfo fields.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			n, _ := strconv.ParseUint(s[i+1:i+4], 8, 8)
			b.WriteByte(byte(n))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }

// isUnder reports whether path is root itself or inside it. Unlike a plain
// prefix match, /var/containers/app2 is not under /var/containers/app.
func isUnder(root, path string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}

// mountsUnder returns the mounts at or below root in the order they must be
// unmounted: deepest first, and later mounts before earlier ones on the same
// mount point.
func mountsUnder(mounts []MountInfo, root string) []MountInfo {
	var res []MountInfo
	for i := len(mounts) - 1; i >= 0; i-- {
		if isUnder(root, mounts[i].Mountpoint) {
			res = append(res, mounts[i])
		}
	}
	slices.SortStableFunc(res, func(a, b MountInfo) int {
		return strings.Count(b.Mountpoint, "/") - strings.Count(a.Mountpoint, "/")
	})
	return res
}

// mountpointsUnder is like mountsUnder but returns only the mount points.
func mountpointsUnder(mounts []MountInfo, root string) []string {
	var mps []string
	for _, mi := range mountsUnder(mounts, root) {
		mps = append(mps, mi.Mountpoint)
	}
	return mps
}
