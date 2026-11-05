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
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.NArg() > 0 {
				return unknownCommand(cmd, cmd.Args().First())
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
			msg := err.Error()
			if name, ok := strings.CutPrefix(msg, "flag provided but not defined: -"); ok {
				msg = "unknown flag " + typedFlag(cmd, name)
				if s := closest(name, flagNames(cmd)); s != "" {
					msg += "; did you mean " + dashed(s) + "?"
				}
			}
			return usageError(cmd, "%s", msg)
		}
	}
	return app
}

// Run runs droot with the command line args (os.Args).
func Run(ctx context.Context, version string, args []string) error {
	return run(ctx, NewApp(version), args)
}

const argsKey = "args"

func run(ctx context.Context, app *cli.Command, args []string) error {
	args = prepareArgs(app, args)
	if err := checkHelpTopic(app, args); err != nil {
		return err
	}
	app.Metadata = map[string]any{argsKey: args}
	return app.Run(ctx, args)
}

// commandNames returns the names and aliases of the subcommands of app,
// including the ones urfave/cli adds.
func commandNames(app *cli.Command) []string {
	names := []string{"help", "h", "completion"}
	for _, c := range app.Commands {
		names = append(names, c.Name)
		names = append(names, c.Aliases...)
	}
	return names
}

func unknownCommand(app *cli.Command, name string) error {
	msg := fmt.Sprintf("'%s' is not a droot command", name)
	if s := closest(name, commandNames(app)); s != "" {
		msg += fmt.Sprintf("; did you mean '%s'?", s)
	}
	return fmt.Errorf("%s (see 'droot --help')", msg)
}

// checkHelpTopic reports `droot help TOPIC` with an unknown TOPIC, for which
// urfave/cli prints a garbled message and exits with status 3.
func checkHelpTopic(app *cli.Command, args []string) error {
	sub := -1
	for i := 1; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "-") {
			sub = i
			break
		}
	}
	if sub < 0 || (args[sub] != "help" && args[sub] != "h") {
		return nil
	}
	for _, topic := range args[sub+1:] {
		if strings.HasPrefix(topic, "-") {
			continue
		}
		if !slices.Contains(commandNames(app), topic) {
			msg := fmt.Sprintf("no help topic for '%s'", topic)
			if s := closest(topic, commandNames(app)); s != "" {
				msg += fmt.Sprintf("; did you mean '%s'?", s)
			}
			return fmt.Errorf("%s (see 'droot --help')", msg)
		}
		break
	}
	return nil
}

// flagNames returns the flag names accepted by cmd, including the global
// ones.
func flagNames(cmd *cli.Command) []string {
	var names []string
	for _, c := range cmd.Lineage() {
		for _, f := range c.Flags {
			names = append(names, f.Names()...)
		}
	}
	return append(names, "help", "h")
}

// typedFlag returns the unknown flag name as the user typed it (urfave/cli
// always reports it with a single dash).
func typedFlag(cmd *cli.Command, name string) string {
	args, _ := cmd.Root().Metadata[argsKey].([]string)
	for _, a := range args {
		if a == "--" {
			break
		}
		for _, form := range []string{"--" + name, "-" + name} {
			if a == form || strings.HasPrefix(a, form+"=") {
				return form
			}
		}
	}
	return dashed(name)
}

func dashed(name string) string {
	if len(name) == 1 {
		return "-" + name
	}
	return "--" + name
}

// closest returns the candidate closest to s, if it is close enough to be a
// typo (optimal string alignment distance, i.e. Levenshtein distance with
// transpositions).
func closest(s string, candidates []string) string {
	best, bestDist := "", len(s)/3+1
	if bestDist > 2 {
		bestDist = 2
	}
	for _, c := range candidates {
		if len(c) == 1 {
			continue
		}
		if d := editDistance(s, c); d <= bestDist && (best == "" || d < editDistance(s, best)) {
			best = c
		}
	}
	return best
}

func editDistance(a, b string) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(a)][len(b)]
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
