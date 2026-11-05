//go:build linux

package osutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

type fileID struct{ dev, ino uint64 }

func statID(st *unix.Stat_t) fileID {
	return fileID{uint64(st.Dev), uint64(st.Ino)} //nolint:unconvert // not uint64 on every architecture
}

// lstat returns the identity and the type (S_IFMT) of the file at p.
func lstat(p string) (fileID, uint32, error) {
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		return fileID{}, 0, err
	}
	return statID(&st), st.Mode & unix.S_IFMT, nil
}

// fuzzTree is a container root full of symlinks trying to escape it, next to
// a directory outside of it.
type fuzzTree struct {
	root    *Root
	rootDir string
	inside  map[fileID]string // every file of the tree, the root included
	outside map[fileID]string // the outside directory, its files and the ancestors of the root
}

func newFuzzTree(f *testing.F) *fuzzTree {
	base := f.TempDir()
	rootDir := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	marker := filepath.Join(outside, "marker")
	for _, d := range []string{"etc", "usr/bin", "usr/lib", "deep/a/b/c"} {
		if err := os.MkdirAll(filepath.Join(rootDir, d), 0o755); err != nil {
			f.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(outside, "dir"), 0o755); err != nil {
		f.Fatal(err)
	}
	for name, content := range map[string]string{
		marker:                                  "OUTSIDE",
		filepath.Join(rootDir, "etc/passwd"):    "root:x:0:0::/root:/bin/sh\n",
		filepath.Join(rootDir, "etc/hostname"):  "droot\n",
		filepath.Join(rootDir, "usr/bin/sh"):    "ELF",
		filepath.Join(rootDir, "deep/a/b/c/f"):  "deep",
		filepath.Join(rootDir, "outside"):       "a file named like the outside directory",
		filepath.Join(rootDir, "etc/.hidden"):   "",
		filepath.Join(rootDir, "usr/lib/x.so"):  "lib",
		filepath.Join(rootDir, "deep/a/marker"): "inside marker",
	} {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			f.Fatal(err)
		}
	}
	links := map[string]string{
		"bin":                "usr/bin",
		"dotdot":             "..",
		"up":                 "../../..",
		"escape":             "../outside",
		"escape-marker":      "../outside/marker",
		"abs-etc":            "/etc",
		"abs-root":           "/",
		"abs-dotdot":         "/..",
		"abs-host-marker":    marker,
		"abs-host-base":      base,
		"etc/self":           "/etc",
		"etc/parent":         "../..",
		"etc/passwd-link":    "../etc/passwd",
		"loop1":              "loop2",
		"loop2":              "loop1",
		"selfloop":           "selfloop",
		"deep/a/b/c/up":      "../../../../../../..",
		"deep/a/b/c/out":     "../../../../../outside/marker",
		"deep/a/b/c/sibling": "../../../../outside",
		"file-link":          "etc/passwd",
		"dir-link":           "etc",
		"trailing":           "etc/",
		"dangling":           "nonexistent",
		"through-file":       "etc/passwd/..",
		"chain":              "bin/../../etc/passwd",
	}
	// A chain of symlinks around the limit: c0 needs 42 to be followed.
	for i := range 42 {
		links[fmt.Sprintf("c%d", i)] = fmt.Sprintf("c%d", i+1)
	}
	links["c42"] = "etc/passwd"
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(rootDir, name)); err != nil {
			f.Fatal(err)
		}
	}
	if err := unix.Mkfifo(filepath.Join(rootDir, "fifo"), 0o644); err != nil {
		f.Fatal(err)
	}

	tree := &fuzzTree{rootDir: rootDir, inside: map[fileID]string{}, outside: map[fileID]string{}}
	err := filepath.WalkDir(rootDir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		id, _, err := lstat(p)
		tree.inside[id] = p
		return err
	})
	if err != nil {
		f.Fatal(err)
	}
	outsiders := []string{outside, marker, filepath.Join(outside, "dir")}
	for d := base; ; d = filepath.Dir(d) {
		outsiders = append(outsiders, d)
		if d == filepath.Dir(d) {
			break
		}
	}
	for _, p := range outsiders {
		id, _, err := lstat(p)
		if err != nil {
			f.Fatal(err)
		}
		tree.outside[id] = p
	}

	tree.root, err = OpenRoot(rootDir)
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { _ = tree.root.Close() })
	return tree
}

// identify returns the identity of the file open as fd and checks that it is
// inside the root.
func (tree *fuzzTree) identify(t *testing.T, fd int, what string) (fileID, uint32) {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		t.Fatalf("%s: fstat: %v", what, err)
	}
	id := statID(&st)
	if p, ok := tree.outside[id]; ok {
		t.Fatalf("%s escaped the root: it opened %s", what, p)
	}
	if _, ok := tree.inside[id]; !ok {
		t.Fatalf("%s opened a file that is not in the root (dev %d, inode %d)", what, id.dev, id.ino)
	}
	return id, st.Mode & unix.S_IFMT
}

// resolveRef resolves p inside rootDir like Root does (symlinks are followed
// as if rootDir were "/", ".." stops at the root, a trailing slash is
// ignored), but with path names instead of descriptors, as a reference for a
// tree that does not change. It returns the host path of the result, which
// may not exist if it is the last component.
func resolveRef(rootDir, p string, followLast, noSymlinks bool) (string, error) {
	var stack []string // components below the root
	parts := strings.Split(p, "/")
	followed := 0
	for len(parts) > 0 {
		part := parts[0]
		parts = parts[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		last := true
		for _, rest := range parts {
			if rest != "" && rest != "." {
				last = false
			}
		}
		host := filepath.Join(rootDir, filepath.Join(stack...), part)
		fi, err := os.Lstat(host)
		switch {
		case err != nil:
			if last && errors.Is(err, fs.ErrNotExist) {
				return host, nil
			}
			return "", err
		case fi.Mode()&fs.ModeSymlink != 0:
			if last && !followLast {
				return host, nil
			}
			if noSymlinks {
				return "", errors.New("symlink")
			}
			if followed++; followed > maxSymlinks {
				return "", unix.ELOOP
			}
			target, err := os.Readlink(host)
			if err != nil {
				return "", err
			}
			if strings.HasPrefix(target, "/") {
				stack = nil
			}
			parts = append(strings.Split(target, "/"), parts...)
		case fi.IsDir():
			if last {
				return host, nil
			}
			stack = append(stack, part)
		default:
			if last {
				return host, nil
			}
			return "", unix.ENOTDIR
		}
	}
	return filepath.Join(rootDir, filepath.Join(stack...)), nil
}

// expect returns the identity of the file the reference resolution of p
// finds, if its type (S_IFMT) satisfies ok.
func (tree *fuzzTree) expect(p string, followLast, noSymlinks bool, ok func(uint32) bool) (fileID, bool) {
	host, err := resolveRef(tree.rootDir, p, followLast, noSymlinks)
	if err != nil {
		return fileID{}, false
	}
	id, mode, err := lstat(host)
	if err != nil || !ok(mode) {
		return fileID{}, false
	}
	return id, true
}

func openFds(tb testing.TB) int {
	tb.Helper()
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		tb.Fatal(err)
	}
	return len(fds)
}

// FuzzRootLookup checks that the read-only operations of Root never open
// anything outside the root, whatever the path, and agree with a reference
// resolution.
func FuzzRootLookup(f *testing.F) {
	tree := newFuzzTree(f)
	outsideMarker := filepath.Join(filepath.Dir(tree.rootDir), "outside", "marker")
	for _, seed := range []string{
		"", ".", "/", "..", "/..", "../..", "./././", "//etc//passwd//",
		"etc/passwd", "etc/passwd/", "etc/passwd/.", "etc/passwd/..", "bin/sh", "bin//sh", "usr/bin/../lib/x.so",
		"../outside/marker", "/../../outside/marker", "dotdot/outside/marker", "dotdot/dotdot/etc/passwd",
		"up/outside/marker", "up/root/etc/passwd", "escape/marker", "escape-marker", "escape/dir",
		"abs-etc/passwd", "abs-root/../outside/marker", "abs-dotdot/outside", "abs-host-marker", "abs-host-base/outside/marker",
		"etc/self/self/self/passwd", "etc/parent/outside/marker", "etc/passwd-link",
		"loop1", "loop2/x", "selfloop", "c0", "c1", "c2", "c40",
		"deep/a/b/c/up/outside/marker", "deep/a/b/c/out", "deep/a/b/c/sibling/marker", "deep/a/b/c/../../../../..",
		"file-link", "file-link/", "dir-link/../dotdot", "trailing", "trailing/passwd", "dangling", "dangling/x",
		"through-file", "chain", "fifo", "fifo/", "outside", "deep/a/marker", "nonexistent/..",
		strings.Repeat("../", 30) + strings.TrimPrefix(outsideMarker, "/"),
		outsideMarker,
		"etc/" + strings.Repeat("x", 300),
		"etc/passwd\x00",
	} {
		f.Add(seed)
	}

	isReg := func(mode uint32) bool { return mode == unix.S_IFREG }
	isDir := func(mode uint32) bool { return mode == unix.S_IFDIR }
	notLink := func(mode uint32) bool { return mode != unix.S_IFLNK }

	// Let the runtime open the descriptors it needs once (e.g. for the
	// poller) before counting them.
	if file, err := tree.root.OpenFile("etc/passwd"); err == nil {
		_ = file.Close()
	}
	_ = openFds(f)

	f.Fuzz(func(t *testing.T, p string) {
		fdsBefore := openFds(t)

		file, err := tree.root.OpenFile(p)
		want, wantOK := tree.expect(p, true, false, isReg)
		if err == nil {
			id, mode := tree.identify(t, int(file.Fd()), fmt.Sprintf("OpenFile(%q)", p))
			_ = file.Close()
			if mode != unix.S_IFREG {
				t.Fatalf("OpenFile(%q) opened a file of type %#o", p, mode)
			}
			if !wantOK || id != want {
				t.Fatalf("OpenFile(%q) opened %s, the reference resolution finds %v", p, tree.inside[id], tree.inside[want])
			}
		} else if wantOK {
			t.Fatalf("OpenFile(%q): %v, but the reference resolution finds %s", p, err, tree.inside[want])
		}

		check := func(fd int) error {
			tree.identify(t, fd, fmt.Sprintf("DirCheck while resolving %q", p))
			return nil
		}
		dir, err := tree.root.OpenDir(p, false, check)
		want, wantOK = tree.expect(p, true, false, isDir)
		if err == nil {
			id, mode := tree.identify(t, int(dir.Fd()), fmt.Sprintf("OpenDir(%q)", p))
			_ = dir.Close()
			if mode != unix.S_IFDIR {
				t.Fatalf("OpenDir(%q) opened a file of type %#o", p, mode)
			}
			if !wantOK || id != want {
				t.Fatalf("OpenDir(%q) opened %s, the reference resolution finds %v", p, tree.inside[id], tree.inside[want])
			}
		} else if wantOK {
			t.Fatalf("OpenDir(%q): %v, but the reference resolution finds %s", p, err, tree.inside[want])
		}

		nofollow, err := tree.root.OpenNoFollow(p)
		want, wantOK = tree.expect(p, false, true, notLink)
		if err == nil {
			id, mode := tree.identify(t, int(nofollow.Fd()), fmt.Sprintf("OpenNoFollow(%q)", p))
			_ = nofollow.Close()
			if mode == unix.S_IFLNK {
				t.Fatalf("OpenNoFollow(%q) opened a symlink", p)
			}
			if !wantOK || id != want {
				t.Fatalf("OpenNoFollow(%q) opened %s, the reference resolution finds %v", p, tree.inside[id], tree.inside[want])
			}
		} else if wantOK {
			t.Fatalf("OpenNoFollow(%q): %v, but the reference resolution finds %s", p, err, tree.inside[want])
		}

		if fdsAfter := openFds(t); fdsAfter != fdsBefore {
			t.Fatalf("%d file descriptors leaked while resolving %q", fdsAfter-fdsBefore, p)
		}
	})
}
