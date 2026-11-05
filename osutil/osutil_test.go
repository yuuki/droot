package osutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExistsFileAndDir(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	mustWrite(t, file, "x")

	if ExistsFile("/path/to/notexist") || ExistsDir("/path/to/notexist") {
		t.Error("nonexistent paths should not exist")
	}
	if !ExistsFile(file) || ExistsFile(dir) {
		t.Error("ExistsFile is wrong")
	}
	if !ExistsDir(dir) || ExistsDir(file) {
		t.Error("ExistsDir is wrong")
	}
}

func TestIsDirEmpty(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "nonempty", "file"), "x")

	if IsDirEmpty("/path/to/notexist") {
		t.Error("nonexistent dir should not be empty")
	}
	if !IsDirEmpty(empty) {
		t.Error("empty dir should be empty")
	}
	if IsDirEmpty(filepath.Join(dir, "nonempty")) {
		t.Error("nonempty dir should not be empty")
	}
}

func TestSecureJoin(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "real", "file"), "x")
	symlinks := map[string]string{
		"abs":         "/real",               // absolute: relative to root
		"rel":         "real",                // relative to the link's directory
		"escape":      "../../../../etc",     // cannot climb above root
		"absescape":   "/../../etc/passwd",   // neither can absolute links
		"real/up":     "../abs",              // chained links
		"loop1":       "loop2",               // infinite loop
		"loop2":       "loop1",               //
		"conf":        "/real",               // a directory link in the middle of a path
		"real/dangle": "/does/not/exist/yet", // dangling link
	}
	for link, target := range symlinks {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}

	tests := map[string]string{
		"":                   "/",
		"/":                  "/",
		"/real/file":         "/real/file",
		"real/./file":        "/real/file",
		"/../../real/file":   "/real/file",
		"/abs/file":          "/real/file",
		"/rel/file":          "/real/file",
		"/escape":            "/etc",
		"/escape/passwd":     "/etc/passwd",
		"/absescape":         "/etc/passwd",
		"/real/up/file":      "/real/file",
		"/conf/hosts":        "/real/hosts",
		"/missing/a/b":       "/missing/a/b",
		"/real/file/sub":     "/real/file/sub",
		"/real/dangle/child": "/does/not/exist/yet/child",
	}
	for in, want := range tests {
		got, err := SecureJoin(root, in)
		if err != nil {
			t.Errorf("SecureJoin(%q): %v", in, err)
			continue
		}
		if want := filepath.Join(root, want); got != want {
			t.Errorf("SecureJoin(%q) = %q, want %q", in, got, want)
		}
	}

	if _, err := SecureJoin(root, "/loop1"); !errors.Is(err, syscall.ELOOP) {
		t.Errorf("want ELOOP for a symlink loop, got %v", err)
	}
}

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	mustWrite(t, src, "127.0.0.1 localhost\n")
	if err := os.Chmod(src, 0o640); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(src, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	// Copying through a symlinked source copies the target's content.
	srcLink := filepath.Join(dir, "srclink")
	if err := os.Symlink(src, srcLink); err != nil {
		t.Fatal(err)
	}

	// A symlink at the destination must be replaced, not followed.
	victim := filepath.Join(dir, "victim")
	mustWrite(t, victim, "do not touch")
	dst := filepath.Join(dir, "root", "etc", "hosts")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, dst); err != nil {
		t.Fatal(err)
	}

	if err := CopyFile(srcLink, dst); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Lstat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o640 || !fi.ModTime().Equal(mtime) {
		t.Errorf("unexpected destination: mode=%v mtime=%v", fi.Mode(), fi.ModTime())
	}
	if b, _ := os.ReadFile(dst); string(b) != "127.0.0.1 localhost\n" {
		t.Errorf("unexpected content %q", b)
	}
	if b, _ := os.ReadFile(victim); string(b) != "do not touch" {
		t.Errorf("symlink target was overwritten: %q", b)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "root", "etc", ".hosts.droot-*")); len(matches) != 0 {
		t.Errorf("temporary files left behind: %v", matches)
	}

	if err := CopyFile(dir, filepath.Join(dir, "x")); err == nil {
		t.Error("copying a directory should fail")
	}
	if err := CopyFile(filepath.Join(dir, "missing"), filepath.Join(dir, "x")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("want ErrNotExist, got %v", err)
	}
}

func TestLookPath(t *testing.T) {
	executables := map[string]bool{"/usr/local/bin/app": true, "/bin/sh": true, "/usr/bin/sh": true}
	isExec := func(p string) error {
		if executables[p] {
			return nil
		}
		if p == "/opt/bin/noexec" {
			return fs.ErrPermission
		}
		return fs.ErrNotExist
	}

	tests := []struct {
		file, path, want string
		wantErr          bool
	}{
		{file: "app", path: "/usr/bin:/usr/local/bin", want: "/usr/local/bin/app"},
		{file: "sh", path: "/usr/bin:/bin", want: "/usr/bin/sh"},
		{file: "sh", path: "", want: "/usr/bin/sh"},                                // DefaultPath
		{file: "app", path: "relative:/usr/local/bin", want: "/usr/local/bin/app"}, // relative dirs ignored
		{file: "/bin/sh", path: "/nowhere", want: "/bin/sh"},
		{file: "./app", path: "/usr/local/bin", wantErr: true},
		{file: "missing", path: "/bin", wantErr: true},
		{file: "noexec", path: "/opt/bin", wantErr: true},
	}
	for _, tt := range tests {
		got, err := LookPath(tt.file, tt.path, isExec)
		if tt.wantErr {
			if err == nil {
				t.Errorf("LookPath(%q, %q) = %q, want error", tt.file, tt.path, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("LookPath(%q, %q) = %q, %v; want %q", tt.file, tt.path, got, err, tt.want)
		}
	}

	if _, err := LookPath("noexec", "/opt/bin", isExec); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("want permission error to be reported, got %v", err)
	}
}
