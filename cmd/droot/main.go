// Command droot is a super-easy container with chroot, without docker.
package main

import (
	"context"
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := commands.NewApp(getVersion()).Run(ctx, os.Args)
	stop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "droot: %v\n", err)
		os.Exit(1)
	}
}
