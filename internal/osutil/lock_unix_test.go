//go:build unix

package osutil

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLockDir(t *testing.T) {
	dir := t.TempDir()
	unlock, err := LockDir(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}

	// A second holder waits.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	waited := false
	if _, err := LockDir(ctx, dir, func() { waited = true }); !errors.Is(err, context.DeadlineExceeded) || !waited {
		t.Fatalf("a busy lock must make the caller wait: %v, waited=%v", err, waited)
	}

	got := make(chan error, 1)
	go func() {
		unlock2, err := LockDir(context.Background(), dir, nil)
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

	if _, err := LockDir(context.Background(), dir+"/missing", nil); err == nil {
		t.Fatal("locking a missing directory must fail")
	}
}
