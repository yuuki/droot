package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/yuuki/droot/internal/cli"
	"github.com/yuuki/droot/internal/osutil"
	"github.com/yuuki/droot/internal/userns"
)

func unshareCommand() *cli.Command {
	return &cli.Command{
		Name:      "unshare",
		Usage:     "Run a command as root of the user namespace of rootless runs",
		ArgsUsage: "[COMMAND [ARG...]]",
		Description: "For users other than root. Runs COMMAND (default: $SHELL, or /bin/sh) as root\n" +
			"in a user namespace with the ID mapping of 'droot run' without root: your user\n" +
			"as root, and your subordinate IDs (/etc/subuid and /etc/subgid, with the\n" +
			"newuidmap and newgidmap commands) as the other users. There, files keep the\n" +
			"owners of an archive, so that the users of an image can write their files:\n\n" +
			"  droot unshare tar -xpf app.tar -C app\n\n" +
			"Files owned by subordinate IDs can only be deleted in the namespace too:\n\n" +
			"  droot unshare rm -rf app",
		StopAtFirstArg: true,
		Action:         doUnshare,
	}
}

func doUnshare(ctx context.Context, cmd *cli.Invocation) error {
	if err := setup(cmd); err != nil {
		return err
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("droot unshare is only supported on Linux (running on %s)", runtime.GOOS)
	}
	argv := cmd.Args()
	if len(argv) == 0 {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		argv = []string{shell}
	}
	if os.Geteuid() != 0 {
		m, err := userns.CurrentMapping()
		if err != nil {
			return err
		}
		return userns.Run(ctx, m, userns.Options{})
	}
	if !osutil.InUserNamespace() {
		return cmd.UsageError("droot unshare is for users other than root: root extracts archives as they are")
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	// The parent-death signal is an attribute of the thread that calls
	// execve(2): set it on this one.
	runtime.LockOSThread()
	if err := userns.KeepParentDeathSignal(); err != nil {
		return err
	}
	signal.Reset(os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	return osutil.Exec(path, argv, os.Environ())
}
