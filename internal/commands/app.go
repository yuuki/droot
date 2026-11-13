// Package commands implements the droot command line interface.
package commands

import (
	"context"

	"github.com/yuuki/droot/internal/cli"
	"github.com/yuuki/droot/internal/log"
)

// NewApp returns the definition of the droot command line.
func NewApp(version string) *cli.App {
	return &cli.App{
		Name:     "droot",
		Usage:    "a super-easy container with chroot, without docker",
		Version:  version,
		Homepage: "https://github.com/yuuki/droot",
		Description: "droot exports the file system of a container image as a tar archive (export),\n" +
			"runs a command inside an extracted archive with chroot(2) (run), as root or\n" +
			"rootless in a user namespace, cleans up the mounts made by run (umount) and\n" +
			"shows them (status), and runs commands in the user namespace of rootless runs\n" +
			"(unshare). It also prints the settings of a systemd service that runs an\n" +
			"extracted archive like run, with systemd alone (unit).",
		Flags: []*cli.Flag{
			{
				Name:    "debug",
				Aliases: []string{"D"},
				Kind:    cli.Bool,
				Usage:   "enable debug output",
				EnvVar:  "DROOT_DEBUG",
			},
			{
				Name:    "quiet",
				Aliases: []string{"q"},
				Kind:    cli.Bool,
				Usage:   "print errors only: no warnings, informational messages nor progress",
				EnvVar:  "DROOT_QUIET",
			},
			{
				Name:        "log-format",
				Kind:        cli.String,
				Placeholder: "FORMAT",
				Usage:       "format of the messages on STDERR: text or json (one log/slog JSON object per line)",
				EnvVar:      "DROOT_LOG_FORMAT",
			},
		},
		Commands: []*cli.Command{
			exportCommand(),
			runCommand(),
			unitCommand(),
			umountCommand(),
			statusCommand(),
			unshareCommand(),
		},
	}
}

// Run runs droot with the command line args (os.Args).
func Run(ctx context.Context, version string, args []string) error {
	return NewApp(version).Run(ctx, args)
}

// setup applies the global flags. It is called by every subcommand so that
// they also work when given after the subcommand name.
func setup(inv *cli.Invocation) error {
	log.SetDebug(inv.Bool("debug"))
	log.SetQuiet(inv.Bool("quiet"))
	if err := log.SetFormat(inv.String("log-format")); err != nil {
		return inv.UsageError("%v", err)
	}
	return nil
}
