//go:build unix

package osutil

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"golang.org/x/sys/unix"
)

// lockPoll is how often LockDir retries a lock held by another process.
const lockPoll = 100 * time.Millisecond

// LockDir takes an exclusive flock(2) lock on the directory path, waiting
// until the other holders release it or ctx is done; waiting is called once
// if the lock is busy. The lock is released by unlock, and in any case when
// the process executes another program (the descriptor is close-on-exec) or
// exits.
func LockDir(ctx context.Context, path string, waiting func()) (unlock func(), err error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	for first := true; ; first = false {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Close(fd) }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if first && waiting != nil {
			waiting()
		}
		select {
		case <-ctx.Done():
			_ = unix.Close(fd)
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}
