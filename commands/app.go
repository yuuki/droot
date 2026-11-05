// Package commands implements the droot command line interface.
package commands

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/yuuki/droot/log"
)

// NewApp returns the root command of droot.
func NewApp(version string) *cli.Command {
	app := &cli.Command{
		Name:    "droot",
		Usage:   "a super-easy container with chroot, without docker",
		Version: version,
		Description: "droot exports the file system of a Docker image as a tar archive (export),\n" +
			"runs a command inside an extracted archive with chroot(2) (run),\n" +
			"and cleans up the mounts made by run (umount).",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:    "debug",
				Aliases: []string{"D"},
				Usage:   "enable debug output",
				Sources: cli.EnvVars("DROOT_DEBUG"),
			},
		},
		Commands: []*cli.Command{
			exportCommand(),
			runCommand(),
			umountCommand(),
		},
		EnableShellCompletion: true,
		Suggest:               true,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.NArg() > 0 {
				return fmt.Errorf("'%s' is not a droot command (see 'droot --help')", cmd.Args().First())
			}
			return cli.ShowRootCommandHelp(cmd)
		},
	}
	for _, c := range append([]*cli.Command{app}, app.Commands...) {
		// --env JAVA_OPTS=-Xmx1g,-Xms1g and paths may contain commas, so
		// never split slice flags (droot <= 0.7 did not either). This is
		// not inherited by subcommands and must be set on each of them.
		c.DisableSliceFlagSeparator = true
		// Report usage errors (unknown flags, ...) with a short hint
		// instead of the full help text.
		c.OnUsageError = func(_ context.Context, cmd *cli.Command, err error, _ bool) error {
			return usageError(cmd, "%v", err)
		}
	}
	return app
}

// setup applies the global flags. It is called by every subcommand so that
// --debug also works when given after the subcommand name.
func setup(cmd *cli.Command) {
	log.SetDebug(cmd.Bool("debug"))
}

// usageError returns an error that points the user to the command's help.
func usageError(cmd *cli.Command, format string, a ...any) error {
	return fmt.Errorf("%s (see '%s --help')", fmt.Sprintf(format, a...), cmd.FullName())
}
