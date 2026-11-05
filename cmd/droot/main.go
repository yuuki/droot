// Command droot is a super-easy container with chroot, without docker.
package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/yuuki/droot/internal/commands"
	"github.com/yuuki/droot/internal/log"
	"github.com/yuuki/droot/internal/userns"
)

// version, commit and date are set at build time with -ldflags
// "-X main.version=... -X main.commit=... -X main.date=..." (GoReleaser and
// make build do).
var version, commit, date string

// getVersion returns the version shown by --version, e.g.
// "0.8.0 (commit 1a2b3c4d5e6f, 2026-11-05T10:00:00Z, go1.26.0)".
func getVersion() string {
	bi, _ := debug.ReadBuildInfo()
	return buildVersion(version, commit, date, bi)
}

func buildVersion(version, commit, date string, bi *debug.BuildInfo) string {
	modified := false
	if bi != nil {
		// `go install github.com/yuuki/droot/cmd/droot@vX.Y.Z` records
		// the module version, and builds from a git checkout the commit.
		if version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			version = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if commit == "" {
					commit = s.Value
				}
			case "vcs.time":
				if date == "" {
					date = s.Value
				}
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
	}
	if version == "" {
		version = "dev"
	}
	var details []string
	if commit != "" {
		if len(commit) > 12 {
			commit = commit[:12]
		}
		if modified {
			commit += "-dirty"
		}
		details = append(details, "commit "+commit)
	}
	if date != "" {
		details = append(details, date)
	}
	details = append(details, runtime.Version())
	return strings.TrimPrefix(version, "v") + " (" + strings.Join(details, ", ") + ")"
}

func main() {
	// A droot started by a rootless droot in a user namespace.
	userns.Init()

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

	// Until the options are parsed, errors already follow DROOT_LOG_FORMAT.
	_ = log.SetFormat(os.Getenv("DROOT_LOG_FORMAT"))
	err := commands.Run(ctx, getVersion(), os.Args)
	stop()
	// The exit status of the droot run rootless, which reported its
	// errors itself.
	var exit *userns.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.Code)
	}
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			err = errors.New("interrupted")
		}
		log.Errorf("%v", err)
		os.Exit(1)
	}
}
