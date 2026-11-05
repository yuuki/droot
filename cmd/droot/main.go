// Command droot is a super-easy container with chroot, without docker.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/yuuki/droot/commands"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = ""

func getVersion() string {
	if version != "" {
		return strings.TrimPrefix(version, "v")
	}
	// `go install github.com/yuuki/droot/cmd/droot@vX.Y.Z` and builds from
	// a git checkout record the module version.
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return strings.TrimPrefix(bi.Main.Version, "v")
	}
	return "dev"
}

func main() {
	// SIGHUP: the terminal or ssh session that started droot is gone.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	// After the first signal, restore the default behavior so that a
	// second Ctrl-C terminates droot even if the cleanup hangs.
	context.AfterFunc(ctx, stop)
	// Without this, a write to a closed pipe on stdout (`droot export img |
	// head`, or a failing `aws s3 cp -`) kills the process with SIGPIPE
	// before it can remove its temporary container. Writes now fail with
	// EPIPE instead. execve(2) resets handled signals, so the command run
	// by `droot run` still gets the default SIGPIPE behavior.
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)

	err := commands.Run(ctx, getVersion(), os.Args)
	stop()
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			err = errors.New("interrupted")
		}
		fmt.Fprintf(os.Stderr, "droot: %v\n", err)
		os.Exit(1)
	}
}
