// Package cli is the small command line parser of droot.
//
// It replaces urfave/cli, whose defaults (splitting repeatable flags on
// commas, an implicit "help" subcommand on every command, its handling of
// "--" after positional arguments, ...) droot had to work around. Its rules
// are deliberately simple:
//
//   - Flags may be written -name or --name, with their value either in the
//     next argument or after "=". Boolean flags take no value unless one is
//     given with "=" (--flag=false).
//   - Global flags are accepted before and after the command name.
//   - Repeatable flags collect every occurrence verbatim: commas are not
//     separators.
//   - "--" ends the flags; every argument after it is positional.
//   - Flags and positional arguments may be mixed, unless the command sets
//     StopAtFirstArg: then the first positional argument and everything
//     after it are taken verbatim (droot run COMMAND [ARG...]).
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
)

// FlagKind is the type of value a flag takes.
type FlagKind int

const (
	// Bool flags take no value (--flag, or --flag=true|false).
	Bool FlagKind = iota
	// String flags take one value; the last occurrence wins.
	String
	// Strings flags take one value and may be repeated.
	Strings
)

// Flag describes a command line flag.
type Flag struct {
	// Name is the main name, without dashes.
	Name string
	// Aliases are other names. Names of one letter are shown with a
	// single dash, longer ones with two; both forms are accepted.
	Aliases []string
	Kind    FlagKind
	// Placeholder names the value in the help, e.g. ROOT_DIR.
	Placeholder string
	Usage       string
	// EnvVar is an environment variable giving the default value of a
	// Bool or String flag.
	EnvVar string
	// Complete is a hint for shell completion of the value: "file",
	// "dir", "user", "group" or "" (no completion).
	Complete string
}

// Names returns the name and the aliases of f.
func (f *Flag) Names() []string { return append([]string{f.Name}, f.Aliases...) }

// Command is a subcommand.
type Command struct {
	Name string
	// Usage is a one-line summary.
	Usage string
	// ArgsUsage describes the positional arguments in the usage line.
	ArgsUsage   string
	Description string
	Flags       []*Flag
	// StopAtFirstArg makes the first positional argument and everything
	// after it verbatim positional arguments, even if they look like flags.
	StopAtFirstArg bool
	// ArgsComplete is a hint for shell completion of the positional
	// arguments: "command" (a command line, as for sudo), "file" or "".
	ArgsComplete string
	// Hidden commands are not listed in the help.
	Hidden bool
	Action func(ctx context.Context, inv *Invocation) error
}

// App is the root command.
type App struct {
	Name        string
	Usage       string
	Version     string
	Description string
	// Homepage is shown in the man page.
	Homepage string
	// Flags are global flags, accepted before and after the command name.
	Flags    []*Flag
	Commands []*Command
	// Stdout receives the help and version output. Default: os.Stdout.
	Stdout io.Writer
}

var (
	helpFlag    = &Flag{Name: "help", Aliases: []string{"h"}, Kind: Bool, Usage: "show help"}
	versionFlag = &Flag{Name: "version", Aliases: []string{"v"}, Kind: Bool, Usage: "print the version"}
)

// helpCommandNames are the names of the built-in help command.
var helpCommandNames = []string{"help", "h"}

// Invocation holds the parsed command line of a command.
type Invocation struct {
	App     *App
	Command *Command
	args    []string
	values  map[string]any
	set     map[string]bool
}

// Args returns the positional arguments.
func (inv *Invocation) Args() []string { return inv.args }

// Bool returns the value of a Bool flag.
func (inv *Invocation) Bool(name string) bool { v, _ := inv.values[name].(bool); return v }

// String returns the value of a String flag.
func (inv *Invocation) String(name string) string { v, _ := inv.values[name].(string); return v }

// Strings returns the values of a Strings flag, in order.
func (inv *Invocation) Strings(name string) []string { v, _ := inv.values[name].([]string); return v }

// IsSet reports whether the flag was given on the command line.
func (inv *Invocation) IsSet(name string) bool { return inv.set[name] }

// FullName returns "app command".
func (inv *Invocation) FullName() string {
	if inv.Command == nil {
		return inv.App.Name
	}
	return inv.App.Name + " " + inv.Command.Name
}

// UsageError returns an error that points the user to the command's help.
func (inv *Invocation) UsageError(format string, a ...any) error {
	return &UsageError{msg: fmt.Sprintf(format, a...), help: inv.FullName() + " --help"}
}

// UsageError is an error in the command line.
type UsageError struct {
	msg  string
	help string
}

func (e *UsageError) Error() string { return fmt.Sprintf("%s (see '%s')", e.msg, e.help) }

// Output returns the writer of the help and of the command output: Stdout,
// or os.Stdout if it is nil.
func (a *App) Output() io.Writer { return a.stdout() }

func (a *App) stdout() io.Writer {
	if a.Stdout != nil {
		return a.Stdout
	}
	return os.Stdout
}

// Command returns the command named name (or with alias name).
func (a *App) Command(name string) *Command {
	for _, c := range a.Commands {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// commandNames returns the names a user may type as a command.
func (a *App) commandNames() []string {
	var names []string
	for _, c := range a.allCommands() {
		names = append(names, c.Name)
	}
	return append(names, helpCommandNames...)
}

// allCommands returns the commands including the built-in completion one.
func (a *App) allCommands() []*Command {
	return append(slices.Clone(a.Commands), a.completionCommand())
}

func (a *App) lookup(name string) *Command {
	for _, c := range a.allCommands() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// Run parses args (os.Args) and runs the selected command.
func (a *App) Run(ctx context.Context, args []string) error {
	if len(args) > 0 {
		args = args[1:]
	}
	root := &Invocation{App: a, values: map[string]any{}, set: map[string]bool{}}
	rootFlags := append(slices.Clone(a.Flags), helpFlag, versionFlag)
	rest, err := root.parse(rootFlags, args, true)
	if err != nil {
		return err
	}
	switch {
	case root.Bool("help"):
		return a.WriteHelp(a.stdout(), nil)
	case root.Bool("version"):
		_, err := fmt.Fprintf(a.stdout(), "%s version %s\n", a.Name, a.Version)
		return err
	case len(rest) == 0:
		return a.WriteHelp(a.stdout(), nil)
	}

	name := rest[0]
	if slices.Contains(helpCommandNames, name) {
		return a.helpCommand(rest[1:])
	}
	cmd := a.lookup(name)
	if cmd == nil {
		return a.unknownCommand(fmt.Sprintf("'%s' is not a %s command", name, a.Name), name)
	}

	inv := &Invocation{App: a, Command: cmd, values: root.values, set: root.set}
	flags := append(slices.Clone(cmd.Flags), a.Flags...)
	flags = append(flags, helpFlag)
	inv.args, err = inv.parse(flags, rest[1:], cmd.StopAtFirstArg)
	if err != nil {
		return err
	}
	if inv.Bool("help") {
		return a.WriteHelp(a.stdout(), cmd)
	}
	if err := inv.applyEnv(flags); err != nil {
		return err
	}
	return cmd.Action(ctx, inv)
}

// helpCommand implements "app help [COMMAND]".
func (a *App) helpCommand(args []string) error {
	for _, topic := range args {
		if strings.HasPrefix(topic, "-") {
			continue
		}
		cmd := a.lookup(topic)
		if cmd == nil {
			return a.unknownCommand(fmt.Sprintf("no help topic for '%s'", topic), topic)
		}
		return a.WriteHelp(a.stdout(), cmd)
	}
	return a.WriteHelp(a.stdout(), nil)
}

// unknownCommand returns an error for an unknown command or help topic,
// suggesting the closest command name.
func (a *App) unknownCommand(msg, name string) error {
	if s := Closest(name, a.commandNames()); s != "" {
		msg += fmt.Sprintf("; did you mean '%s'?", s)
	}
	return &UsageError{msg: msg, help: a.Name + " --help"}
}

// parse parses flags in args into inv and returns the positional
// arguments. With stopAtArg, parsing stops at the first positional argument.
func (inv *Invocation) parse(flags []*Flag, args []string, stopAtArg bool) ([]string, error) {
	byName := map[string]*Flag{}
	for _, f := range flags {
		for _, n := range f.Names() {
			byName[n] = f
		}
	}
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return append(positional, args[i+1:]...), nil
		}
		if len(arg) < 2 || arg[0] != '-' {
			if stopAtArg {
				return append(positional, args[i:]...), nil
			}
			positional = append(positional, arg)
			continue
		}
		used, err := inv.parseFlag(flags, byName, arg, args[i+1:])
		if err != nil {
			return nil, err
		}
		i += used
	}
	return positional, nil
}

// parseFlag parses the flag arg into inv: flags are the flags accepted, and
// byName maps their names to them. A String or Strings flag without "=VALUE"
// takes the next argument, the first of rest, as its value: parseFlag
// returns the number of arguments of rest it took.
func (inv *Invocation) parseFlag(flags []*Flag, byName map[string]*Flag, arg string, rest []string) (int, error) {
	name := strings.TrimPrefix(arg[1:], "-")
	name, value, hasValue := strings.Cut(name, "=")
	typed := arg
	if hasValue {
		typed = arg[:len(arg)-len(value)-1]
	}
	f := byName[name]
	if f == nil || name == "" || strings.HasPrefix(name, "-") {
		return 0, inv.unknownFlag(typed, name, flags)
	}
	used := 0
	switch f.Kind {
	case Bool:
		b := true
		if hasValue {
			var err error
			if b, err = strconv.ParseBool(value); err != nil {
				return 0, inv.UsageError("invalid value %q for flag %s: want true or false", value, typed)
			}
		}
		inv.values[f.Name] = b
	case String, Strings:
		if !hasValue {
			if len(rest) == 0 {
				return 0, inv.UsageError("flag %s needs an argument", typed)
			}
			value, used = rest[0], 1
		}
		if f.Kind == String {
			inv.values[f.Name] = value
		} else {
			prev, _ := inv.values[f.Name].([]string)
			inv.values[f.Name] = append(prev, value)
		}
	}
	inv.set[f.Name] = true
	return used, nil
}

// unknownFlag returns the error for the unknown flag typed, of name name,
// suggesting the closest of the names of flags.
func (inv *Invocation) unknownFlag(typed, name string, flags []*Flag) error {
	msg := "unknown flag " + typed
	if s := Closest(strings.TrimLeft(name, "-"), flagNames(flags)); s != "" {
		msg += "; did you mean " + Dashed(s) + "?"
	}
	return inv.UsageError("%s", msg)
}

// applyEnv sets the Bool and String flags that were not given on the
// command line from their environment variable.
func (inv *Invocation) applyEnv(flags []*Flag) error {
	for _, f := range flags {
		if f.EnvVar == "" || f.Kind == Strings || inv.set[f.Name] {
			continue
		}
		s, ok := os.LookupEnv(f.EnvVar)
		if !ok || s == "" {
			continue
		}
		if f.Kind == String {
			inv.values[f.Name] = s
			continue
		}
		b, err := strconv.ParseBool(s)
		if err != nil {
			return fmt.Errorf("invalid value %q in the environment variable %s: want true or false (1 or 0)", s, f.EnvVar)
		}
		inv.values[f.Name] = b
	}
	return nil
}

func flagNames(flags []*Flag) []string {
	var names []string
	for _, f := range flags {
		names = append(names, f.Names()...)
	}
	return names
}

// Dashed returns a flag name as typed: -x for one letter, --name otherwise.
func Dashed(name string) string {
	if len(name) == 1 {
		return "-" + name
	}
	return "--" + name
}

// Closest returns the candidate closest to s if it is close enough to be a
// typo (optimal string alignment distance, i.e. Levenshtein distance with
// transpositions), or "".
func Closest(s string, candidates []string) string {
	best, bestDist := "", min(len(s)/3+1, 2)
	for _, c := range candidates {
		if len(c) == 1 {
			continue
		}
		if d := EditDistance(s, c); d <= bestDist && (best == "" || d < EditDistance(s, best)) {
			best = c
		}
	}
	return best
}

// EditDistance returns the optimal string alignment distance between a and
// b.
func EditDistance(a, b string) int {
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

// IsUsageError reports whether err is a command line error.
func IsUsageError(err error) bool {
	var u *UsageError
	return errors.As(err, &u)
}

// ApplyConfig sets the flags of the command from a configuration file,
// whose keys are the long names of the flags: values given on the command
// line take precedence, except for the repeatable flags, whose values from
// the file come first. Bool flags take a bool, String flags a string and
// Strings flags a []string (or a string). Global flags and unknown keys
// are errors.
func (inv *Invocation) ApplyConfig(values map[string]any) error {
	byName := map[string]*Flag{}
	var names []string
	for _, f := range inv.Command.Flags {
		byName[f.Name] = f
		names = append(names, f.Name)
	}
	for _, key := range slices.Sorted(maps.Keys(values)) {
		f := byName[key]
		if f == nil {
			msg := fmt.Sprintf("unknown option %q", key)
			if s := Closest(key, names); s != "" {
				msg += fmt.Sprintf("; did you mean %q?", s)
			}
			return errors.New(msg)
		}
		v := values[key]
		switch f.Kind {
		case Bool:
			b, ok := v.(bool)
			if !ok {
				return fmt.Errorf("%s must be true or false", key)
			}
			if !inv.set[key] {
				inv.values[key] = b
			}
		case String:
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("%s must be a string", key)
			}
			if !inv.set[key] {
				inv.values[key] = s
			}
		case Strings:
			var list []string
			switch v := v.(type) {
			case string:
				list = []string{v}
			case []string:
				list = v
			default:
				return fmt.Errorf("%s must be an array of strings", key)
			}
			prev, _ := inv.values[key].([]string)
			inv.values[key] = append(slices.Clone(list), prev...)
		}
		inv.set[key] = true
	}
	return nil
}
