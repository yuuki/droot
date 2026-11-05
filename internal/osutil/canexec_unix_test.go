//go:build unix

package osutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanExecute(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to set the owners")
	}
	dir := t.TempDir()
	// t.TempDir creates its parent with mode 0700.
	for _, d := range []string{filepath.Dir(dir), dir} {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name string, mode os.FileMode, uid, gid int) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(p, uid, gid); err != nil {
			t.Fatal(err)
		}
		return p
	}
	rootOnly := write("root-only", 0o700, 0, 0)
	groupOnly := write("group-only", 0o750, 0, 4000)
	everyone := write("everyone", 0o755, 0, 0)
	noExec := write("no-exec", 0o644, 0, 0)
	ownerNoExec := write("owner-no-exec", 0o675, 1500, 0) // owner bits win

	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	inLocked := filepath.Join(locked, "x")
	if err := os.WriteFile(inLocked, nil, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		path   string
		uid    int
		gid    int
		groups []int
		ok     bool
	}{
		{rootOnly, 0, 0, nil, true},
		{rootOnly, 1500, 1500, nil, false},
		{groupOnly, 1500, 4000, nil, true},
		{groupOnly, 1500, 1500, []int{1500, 4000}, true},
		{groupOnly, 1500, 1500, []int{1500}, false},
		{everyone, 1500, 1500, nil, true},
		{noExec, 0, 0, nil, false},
		{ownerNoExec, 1500, 0, nil, false},
		{inLocked, 1500, 1500, nil, false},
		{inLocked, 0, 0, nil, true},
		{dir, 0, 0, nil, false},
	} {
		if err := CanExecute(tt.path, tt.uid, tt.gid, tt.groups); (err == nil) != tt.ok {
			t.Errorf("CanExecute(%s, %d, %d, %v) = %v, want ok=%v", filepath.Base(tt.path), tt.uid, tt.gid, tt.groups, err, tt.ok)
		}
	}
}
