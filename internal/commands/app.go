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
		Name:    "droot",
		Usage:   "a super-easy container with chroot, without docker",
		Version: version,
		Description: "droot exports the file system of a Docker image as a tar archive (export),\n" +
			"runs a command inside an extracted archive with chroot(2) (run),\n" +
			"and cleans up the mounts made by run (umount).",
		Flags: []*cli.Flag{
			{
				Name:    "debug",
				Aliases: []string{"D"},
				Kind:    cli.Bool,
				Usage:   "enable debug output",
				EnvVar:  "DROOT_DEBUG",
			},
		},
		Commands: []*cli.Command{
			exportCommand(),
			runCommand(),
			umountCommand(),
		},
	}
}

// Run runs droot with the command line args (os.Args).
func Run(ctx context.Context, version string, args []string) error {
	return NewApp(version).Run(ctx, args)
}

// setup applies the global flags. It is called by every subcommand so that
// --debug also works when given after the subcommand name.
func setup(inv *cli.Invocation) {
	log.SetDebug(inv.Bool("debug"))
}
