package osutil

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// newTree creates <tmp>/root and <tmp>/outside. Escapes from the root are
// detected as new entries in outside.
func newTree(t *testing.T) (root *Root, rootDir, outside string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rootDir = filepath.Join(base, "root")
	outside = filepath.Join(base, "outside")
	for _, d := range []string{rootDir, outside} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root, err = OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root, rootDir, outside
}

func readFile(t *testing.T, root *Root, path string) string {
	t.Helper()
	f, err := root.OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile(%q): %v", path, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRootResolutionStaysInside(t *testing.T) {
	root, rootDir, outside := newTree(t)
	mustWrite(t, filepath.Join(rootDir, "real", "file"), "inside")
	mustWrite(t, filepath.Join(rootDir, "etc", "passwd"), "inside passwd")
	mustWrite(t, filepath.Join(outside, "file"), "OUTSIDE")
	for link, target := range map[string]string{
		"abs":       "/real",
		"rel":       "real",
		"escape":    "../outside",         // relative, climbing above the root
		"absescape": outside,              // absolute host path
		"dotdot":    "/../../../../etc",   // cannot climb above the root
		"real/up":   "../abs",             // chained
		"loop1":     "loop2",              //
		"loop2":     "loop1",              //
		"real/self": "/",                  // the root itself
		"proc-root": "/proc/self/root/..", // magic links are plain paths inside
	} {
		if err := os.Symlink(target, filepath.Join(rootDir, link)); err != nil {
			t.Fatal(err)
		}
	}

	for path, want := range map[string]string{
		"/real/file":           "inside",
		"/abs/file":            "inside",
		"/rel/file":            "inside",
		"/real/up/file":        "inside",
		"/real/self/real/file": "inside",
		"/../../real/file":     "inside",
		"/dotdot/passwd":       "inside passwd",
	} {
		if got := readFile(t, root, path); got != want {
			t.Errorf("OpenFile(%q) = %q, want %q", path, got, want)
		}
	}
	for _, path := range []string{"/escape/file", "/absescape/file", "/loop1", "/proc-root/etc/passwd"} {
		if f, err := root.OpenFile(path); err == nil {
			b, _ := io.ReadAll(f)
			f.Close()
			t.Errorf("OpenFile(%q) should fail, read %q", path, b)
		}
	}

	// Creating through escaping symlinks lands inside the root.
	for _, path := range []string{"/escape/newdir/sub", "/absescape/newdir2"} {
		d, err := root.OpenDir(path, true, nil)
		if err != nil {
			t.Fatalf("OpenDir(%q): %v", path, err)
		}
		d.Close()
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 1 {
		t.Fatalf("something was created outside the root: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(rootDir, "outside", "newdir", "sub")); err != nil {
		t.Errorf("../outside should resolve to <root>/outside: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootDir, outside, "newdir2")); err != nil {
		t.Errorf("an absolute link should resolve inside the root: %v", err)
	}
}

func TestOpenMountTarget(t *testing.T) {
	root, rootDir, _ := newTree(t)
	if err := os.Symlink("/", filepath.Join(rootDir, "toroot")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "", "/toroot", "/a/..", "/toroot/.."} {
		if mt, err := root.OpenMountTarget(path, true, nil); err == nil {
			mt.Close()
			t.Errorf("OpenMountTarget(%q) should be refused", path)
		}
	}
	if _, err := root.OpenMountTarget("/toroot", true, nil); !errors.Is(err, ErrIsRoot) {
		t.Errorf("want ErrIsRoot, got %v", err)
	}

	mt, err := root.OpenMountTarget("/data/dir", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	mt.Close()
	if fi, err := os.Stat(filepath.Join(rootDir, "data", "dir")); err != nil || !fi.IsDir() {
		t.Errorf("directory target not created: %v", err)
	}
	mt, err = root.OpenMountTarget("/etc/app/config.yml", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	mt.Close()
	if fi, err := os.Stat(filepath.Join(rootDir, "etc", "app", "config.yml")); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("file target not created: %v", err)
	}
	if _, err := root.OpenMountTarget("/data/dir", false, nil); err == nil {
		t.Error("a directory must not be accepted as a file target")
	}

	errDenied := errors.New("denied")
	if _, err := root.OpenMountTarget("/data/dir/x", true, func(int) error { return errDenied }); !errors.Is(err, errDenied) {
		t.Errorf("check must be applied, got %v", err)
	}
}

func TestOpenMountTargetIgnoresUmask(t *testing.T) {
	root, rootDir, _ := newTree(t)
	old := unix.Umask(0o077) // e.g. UMask=0077 in a unit
	defer unix.Umask(old)
	for _, tt := range []struct {
		path string
		dir  bool
	}{{"/run/systemd/notify", false}, {"/srv/a/b", true}} {
		mt, err := root.OpenMountTarget(tt.path, tt.dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		mt.Close()
	}
	for path, want := range map[string]os.FileMode{
		"/run": os.ModeDir | 0o755, "/run/systemd": os.ModeDir | 0o755, "/run/systemd/notify": 0o644,
		"/srv": os.ModeDir | 0o755, "/srv/a": os.ModeDir | 0o755, "/srv/a/b": os.ModeDir | 0o755,
	} {
		fi, err := os.Lstat(filepath.Join(rootDir, path))
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode() & (os.ModeType | os.ModePerm); got != want {
			t.Errorf("%s: mode %v, want %v", path, got, want)
		}
	}
}

func TestOpenFileRefusesSpecialFiles(t *testing.T) {
	root, rootDir, _ := newTree(t)
	if err := unix.Mkfifo(filepath.Join(rootDir, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := root.OpenFile("/fifo")
		if err == nil {
			f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a FIFO must be refused")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opening a FIFO blocked")
	}
	if _, err := root.OpenFile("/"); err == nil {
		t.Error("a directory must be refused")
	}
}

func TestCopyFileIn(t *testing.T) {
	root, rootDir, outside := newTree(t)
	src := filepath.Join(t.TempDir(), "hosts")
	mustWrite(t, src, "127.0.0.1 localhost\n")
	if err := os.Chmod(src, 0o640); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(src, mtime, mtime); err != nil {
		t.Fatal(err)
	}

	// A malicious image: /etc/hosts points at a host file.
	victim := filepath.Join(outside, "victim")
	mustWrite(t, victim, "do not touch")
	if err := os.MkdirAll(filepath.Join(rootDir, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(rootDir, "etc", "hosts")); err != nil {
		t.Fatal(err)
	}

	if err := root.CopyFileIn(src, "/etc/hosts", nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "do not touch" {
		t.Fatalf("host file overwritten: %q", b)
	}
	// The absolute link is followed inside the root.
	dst := filepath.Join(rootDir, victim)
	fi, err := os.Lstat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o640 || !fi.ModTime().Equal(mtime) {
		t.Errorf("unexpected copy: mode=%v mtime=%v", fi.Mode(), fi.ModTime())
	}
	if b, _ := os.ReadFile(dst); string(b) != "127.0.0.1 localhost\n" {
		t.Errorf("unexpected content %q", b)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(src, 1234, 5678); err != nil {
			t.Fatal(err)
		}
		if err := root.CopyFileIn(src, "/owned", nil); err != nil {
			t.Fatal(err)
		}
		var st unix.Stat_t
		if err := unix.Stat(filepath.Join(rootDir, "owned"), &st); err != nil {
			t.Fatal(err)
		}
		if st.Uid != 1234 || st.Gid != 5678 {
			t.Errorf("ownership not preserved: %d:%d", st.Uid, st.Gid)
		}
	}

	if err := root.CopyFileIn(src, "/etc/new/passwd", nil); err != nil {
		t.Fatal(err)
	}
	if matches, _ := filepath.Glob(filepath.Join(rootDir, "etc", "new", ".droot-*")); len(matches) != 0 {
		t.Errorf("temporary files left behind: %v", matches)
	}
	if err := root.CopyFileIn(t.TempDir(), "/x", nil); err == nil {
		t.Error("copying a directory should fail")
	}
}

func TestSymlinkIfMissing(t *testing.T) {
	root, rootDir, _ := newTree(t)
	if err := os.Mkdir(filepath.Join(rootDir, "var"), 0o755); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := root.SymlinkIfMissing("../run/lock", "/var/lock", nil); err != nil {
			t.Fatal(err)
		}
	}
	if target, err := os.Readlink(filepath.Join(rootDir, "var", "lock")); err != nil || target != "../run/lock" {
		t.Fatalf("got %q, %v", target, err)
	}
	if err := root.SymlinkIfMissing("x", "/missing/lock", nil); err == nil {
		t.Error("a missing parent should fail")
	}
}

func TestMknodChar(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("mknod requires root")
	}
	old := unix.Umask(0o077) // the mode must not depend on the umask
	defer unix.Umask(old)

	root, rootDir, outside := newTree(t)
	mustWrite(t, filepath.Join(rootDir, "dev", "null"), "stale regular file")
	victim := filepath.Join(outside, "zero")
	mustWrite(t, victim, "host file")
	if err := os.Symlink(victim, filepath.Join(rootDir, "dev", "zero")); err != nil {
		t.Fatal(err)
	}

	for range 2 { // idempotent
		for _, d := range []struct {
			name         string
			major, minor uint32
		}{{"/dev/null", 1, 3}, {"/dev/zero", 1, 5}, {"/newdev/random", 1, 8}} {
			if err := root.MknodChar(d.name, d.major, d.minor, 0o666, nil); err != nil {
				if errors.Is(err, unix.EPERM) {
					t.Skipf("mknod not permitted here: %v", err)
				}
				t.Fatal(err)
			}
			var st unix.Stat_t
			if err := unix.Lstat(filepath.Join(rootDir, d.name), &st); err != nil {
				t.Fatal(err)
			}
			if st.Mode&unix.S_IFMT != unix.S_IFCHR || unix.Major(st.Rdev) != d.major ||
				unix.Minor(st.Rdev) != d.minor || st.Mode&0o777 != 0o666 {
				t.Errorf("%s: mode=%o rdev=%d:%d", d.name, st.Mode, unix.Major(st.Rdev), unix.Minor(st.Rdev))
			}
		}
	}
	if b, _ := os.ReadFile(victim); string(b) != "host file" {
		t.Fatalf("the symlink was followed to the host: %q", b)
	}
}

// TestRootRace swaps a directory of the root with a symlink to the outside
// as fast as possible while droot operates in it, like a malicious process
// of a running container could.
func TestRootRace(t *testing.T) {
	root, rootDir, outside := newTree(t)
	src := filepath.Join(t.TempDir(), "src")
	mustWrite(t, src, "data")
	dir := filepath.Join(rootDir, "dev")
	link := filepath.Join(rootDir, "dev-link")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	var stop atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			if err := unix.Renameat2(unix.AT_FDCWD, dir, unix.AT_FDCWD, link, unix.RENAME_EXCHANGE); err != nil {
				t.Errorf("renameat2: %v", err)
				return
			}
		}
	}()

	isRoot := os.Geteuid() == 0
	deadline := time.Now().Add(2 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		_ = root.CopyFileIn(src, "/dev/copied", nil)
		if mt, err := root.OpenMountTarget("/dev/target", false, nil); err == nil {
			mt.Close()
		}
		_ = root.SymlinkIfMissing("x", "/dev/link", nil)
		if isRoot {
			_ = root.MknodChar("/dev/null", 1, 3, 0o666, nil)
		}
	}
	stop.Store(true)
	wg.Wait()

	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("operations escaped the root through the race: %v", names)
	}
}
