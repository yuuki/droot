//go:build unix

package osutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLockRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		unlock, err := LockRoot(context.Background(), t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		unlock() // nothing to lock without root
		return
	}
	orig := lockDir
	lockDir = filepath.Join(t.TempDir(), "locks")
	t.Cleanup(func() { lockDir = orig })

	root := t.TempDir()
	unlock, err := LockRoot(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(lockDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("lock directory: %v, %v", fi.Mode(), err)
	}

	// A second holder waits, also through another path of the root.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	waited := false
	if _, err := LockRoot(ctx, link, func() { waited = true }); !errors.Is(err, context.DeadlineExceeded) || !waited {
		t.Fatalf("a busy lock must make the caller wait: %v, waited=%v", err, waited)
	}
	// Another root is independent.
	unlockOther, err := LockRoot(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	unlockOther()

	got := make(chan error, 1)
	go func() {
		unlock2, err := LockRoot(context.Background(), root, nil)
		if err == nil {
			unlock2()
		}
		got <- err
	}()
	time.Sleep(50 * time.Millisecond)
	unlock()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the lock was not handed over after unlock")
	}

	// An insecure lock directory is refused.
	if err := os.Chmod(lockDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := LockRoot(context.Background(), root, nil); err == nil {
		t.Fatal("a lock directory writable by others must be refused")
	}
}
