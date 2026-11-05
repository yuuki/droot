// Package commands implements the droot command line interface.
package commands

import (
	"context"
	"fmt"
	"slices"
	"strings"

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
	for _, c := range app.Commands {
		// urfave/cli adds a "help" (alias "h") subcommand to every
		// command, which would hijack `droot run -- help` or an image named
		// "help". -h/--help and `droot help COMMAND` still work.
		c.HideHelpCommand = true
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

// Run runs droot with the command line args (os.Args).
func Run(ctx context.Context, version string, args []string) error {
	app := NewApp(version)
	return app.Run(ctx, prepareArgs(app, args))
}

// completionFlag is appended by the shell completion scripts.
const completionFlag = "--generate-shell-completion"

// prepareArgs makes sure everything after COMMAND in `droot run [OPTIONS]
// COMMAND [ARG...]` reaches COMMAND verbatim. urfave/cli handles a "--"
// before StopOnNthArg, so `droot run -r R echo -- x` would lose the "--";
// inserting "--" right before COMMAND avoids that (urfave/cli removes only
// the first one). For the same reason, a COMMAND ending in the shell
// completion flag must not be taken as a completion request.
func prepareArgs(app *cli.Command, args []string) []string {
	sub := -1
	for i := 1; i < len(args); i++ {
		if args[i] == "--" {
			return args
		}
		if !strings.HasPrefix(args[i], "-") {
			sub = i
			break
		}
	}
	if sub < 0 || args[sub] != "run" {
		return args
	}
	var run *cli.Command
	for _, c := range app.Commands {
		if c.Name == "run" {
			run = c
		}
	}
	takesValue := map[string]bool{}
	for _, f := range run.Flags {
		if _, isBool := f.(*cli.BoolFlag); !isBool {
			for _, n := range f.Names() {
				takesValue[n] = true
			}
		}
	}

	dash := -1
	for j := sub + 1; j < len(args); j++ {
		a := args[j]
		if a == "--" {
			dash = j
			break
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			if name := strings.TrimLeft(a, "-"); !strings.Contains(name, "=") && takesValue[name] {
				j++ // skip the value
			}
			continue
		}
		args = slices.Concat(args[:j:j], []string{"--"}, args[j:])
		dash = j
		break
	}
	if dash >= 0 && dash < len(args)-1 && args[len(args)-1] == completionFlag {
		app.EnableShellCompletion = false
	}
	return args
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
