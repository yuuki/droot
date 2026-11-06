//go:build unix

package osutil

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/yuuki/droot/internal/log"
)

// lockPoll is how often LockRoot retries a lock held by another process.
const lockPoll = 100 * time.Millisecond

// lockDir holds the lock files of LockRoot. It is only accessible by root:
// a lock on the root directory itself could be taken, and held forever, by
// any process able to open it — including every process of the container.
var lockDir = "/run/droot"

// LockRoot takes an exclusive lock on the container root directory
// rootDir, waiting until the other holders release it or ctx is done;
// waiting is called once if the lock is busy. The lock is a flock(2) on a
// file of /run/droot named after the device and inode of rootDir, so all
// the paths of a root share it. It is released by unlock, and in any case
// when the process executes another program (the descriptor is
// close-on-exec) or exits.
//
// Only root can take the lock; for other users LockRoot does nothing, as
// they cannot mount anything anyway.
func LockRoot(ctx context.Context, rootDir string, waiting func()) (unlock func(), err error) {
	if os.Geteuid() != 0 {
		return func() {}, nil
	}
	return lockIn(ctx, lockDir, rootDir, waiting, "a directory owned by root with mode 0700")
}

// LockRootUser is LockRoot for rootless runs: the lock files are in
// runtimeDir/droot, runtimeDir being the user's XDG_RUNTIME_DIR. Without
// it, or if it cannot be used, nothing is locked.
func LockRootUser(ctx context.Context, runtimeDir, rootDir string, waiting func()) (unlock func(), err error) {
	if runtimeDir == "" || !filepath.IsAbs(runtimeDir) {
		log.Debugf("not locking %s: XDG_RUNTIME_DIR is not set", rootDir)
		return func() {}, nil
	}
	unlock, err = lockIn(ctx, filepath.Join(runtimeDir, "droot"), rootDir, waiting, "your own directory with mode 0700")
	if err != nil && ctx.Err() == nil {
		log.Warnf("not locking %s: %v", rootDir, err)
		return func() {}, nil
	}
	return unlock, err
}

// lockIn takes the lock of rootDir in lockDir, which must be what wantDir
// says.
func lockIn(ctx context.Context, lockDir, rootDir string, waiting func(), wantDir string) (unlock func(), err error) {
	var st unix.Stat_t
	if err := unix.Stat(rootDir, &st); err != nil {
		return nil, &fs.PathError{Op: "stat", Path: rootDir, Err: err}
	}
	// Its owner and mode are checked below: for LockRootUser, it is in
	// the user's own XDG_RUNTIME_DIR.
	if err := os.Mkdir(lockDir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	var dst unix.Stat_t
	if err := unix.Lstat(lockDir, &dst); err != nil {
		return nil, &fs.PathError{Op: "stat", Path: lockDir, Err: err}
	}
	if dst.Mode&unix.S_IFMT != unix.S_IFDIR || dst.Uid != 0 || dst.Mode&0o077 != 0 {
		return nil, fmt.Errorf("refusing to use the lock directory %s: it must be %s", lockDir, wantDir)
	}
	path := filepath.Join(lockDir, fmt.Sprintf("%d-%d.lock", st.Dev, st.Ino))
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
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
