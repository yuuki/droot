package osutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
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
