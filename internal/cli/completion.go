package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Shells lists the shells for which completion scripts are generated.
var Shells = []string{"bash", "zsh", "fish"}

func (a *App) completionCommand() *Command {
	return &Command{
		Name:      "completion",
		Usage:     "Print a shell completion script (" + strings.Join(Shells, ", ") + ")",
		ArgsUsage: strings.Join(Shells, "|"),
		Description: "Prints a script that completes the commands and options of " + a.Name + ".\n\n" +
			"   bash:  source <(" + a.Name + " completion bash)            # e.g. in ~/.bashrc\n" +
			"   zsh:   source <(" + a.Name + " completion zsh)             # e.g. in ~/.zshrc\n" +
			"   fish:  " + a.Name + " completion fish > ~/.config/fish/completions/" + a.Name + ".fish",
		ArgsComplete: "shell",
		Action: func(_ context.Context, inv *Invocation) error {
			args := inv.Args()
			if len(args) != 1 {
				return inv.UsageError("exactly one shell is required: %s", strings.Join(Shells, ", "))
			}
			return a.WriteCompletion(a.stdout(), args[0])
		},
	}
}

// WriteCompletion writes the completion script for shell.
func (a *App) WriteCompletion(w io.Writer, shell string) error {
	var s string
	switch shell {
	case "bash":
		s = a.bashCompletion()
	case "zsh":
		s = a.zshCompletion()
	case "fish":
		s = a.fishCompletion()
	default:
		return &UsageError{
			msg:  fmt.Sprintf("unsupported shell %q: want one of %s", shell, strings.Join(Shells, ", ")),
			help: a.Name + " completion --help",
		}
	}
	_, err := io.WriteString(w, s)
	return err
}

func (a *App) visibleCommands() []*Command {
	var cmds []*Command
	for _, c := range a.allCommands() {
		if !c.Hidden {
			cmds = append(cmds, c)
		}
	}
	return cmds
}

func dashedNames(f *Flag) []string {
	var names []string
	for _, n := range f.Names() {
		names = append(names, Dashed(n))
	}
	return names
}

// shellFuncName turns the app name into an identifier.
func shellFuncName(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, name)
}

func (a *App) bashCompletion() string {
	fn := "_" + shellFuncName(a.Name)
	var b strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	var cmdNames []string
	for _, c := range a.visibleCommands() {
		cmdNames = append(cmdNames, c.Name)
	}
	cmdNames = append(cmdNames, "help")
	var globalNames []string
	for _, f := range append(slices.Clone(a.Flags), helpFlag, versionFlag) {
		globalNames = append(globalNames, dashedNames(f)...)
	}

	p("# bash completion for %s. Load it with: source <(%s completion bash)\n", a.Name, a.Name)
	p("%s_value() {\n", fn)
	p("    case $1 in\n")
	p("    file) COMPREPLY=($(compgen -f -- \"$cur\")); compopt -o filenames 2>/dev/null ;;\n")
	p("    dir) COMPREPLY=($(compgen -d -- \"$cur\")); compopt -o filenames 2>/dev/null ;;\n")
	p("    user) COMPREPLY=($(compgen -u -- \"$cur\")) ;;\n")
	p("    group) COMPREPLY=($(compgen -g -- \"$cur\")) ;;\n")
	p("    *) COMPREPLY=() ;;\n")
	p("    esac\n")
	p("}\n\n")
	p("%s() {\n", fn)
	p("    local cur=${COMP_WORDS[COMP_CWORD]} prev=${COMP_WORDS[COMP_CWORD-1]}\n")
	p("    local i=1 cmd=\n")
	p("    while ((i < COMP_CWORD)); do\n")
	p("        case ${COMP_WORDS[i]} in\n")
	p("        -*) ;;\n")
	p("        *) cmd=${COMP_WORDS[i]}; break ;;\n")
	p("        esac\n")
	p("        ((i++))\n")
	p("    done\n")
	p("    if [[ -z $cmd ]]; then\n")
	p("        if [[ $cur == -* ]]; then\n")
	p("            COMPREPLY=($(compgen -W %q -- \"$cur\"))\n", strings.Join(globalNames, " "))
	p("        else\n")
	p("            COMPREPLY=($(compgen -W %q -- \"$cur\"))\n", strings.Join(cmdNames, " "))
	p("        fi\n")
	p("        return\n")
	p("    fi\n")
	p("    local cmdi=$i\n")
	p("    case $cmd in\n")
	for _, c := range a.visibleCommands() {
		flags := append(slices.Clone(c.Flags), a.Flags...)
		flags = append(flags, helpFlag)
		var all, valued []string
		for _, f := range flags {
			all = append(all, dashedNames(f)...)
			if f.Kind != Bool {
				valued = append(valued, dashedNames(f)...)
			}
		}
		p("    %s)\n", c.Name)
		// Complete the value of the previous flag.
		for _, f := range flags {
			if f.Kind != Bool {
				p("        case $prev in %s) %s_value %q; return ;; esac\n", strings.Join(dashedNames(f), "|"), fn, f.Complete)
			}
		}
		if c.StopAtFirstArg {
			// Find where the positional arguments start.
			p("        local j=$((cmdi + 1)) start=\n")
			p("        while ((j < COMP_CWORD)); do\n")
			p("            case ${COMP_WORDS[j]} in\n")
			p("            --) start=$((j + 1)); break ;;\n")
			if len(valued) > 0 {
				p("            %s) ((j++)) ;;\n", strings.Join(valued, "|"))
			}
			p("            -*) ;;\n")
			p("            *) start=$j; break ;;\n")
			p("            esac\n")
			p("            ((j++))\n")
			p("        done\n")
			p("        if [[ -n $start ]]; then\n")
			if c.ArgsComplete == "command" {
				p("            if ((COMP_CWORD == start)); then COMPREPLY=($(compgen -c -- \"$cur\" | sort -u)); else %s_value file; fi\n", fn)
			} else {
				p("            %s_value %q\n", fn, c.ArgsComplete)
			}
			p("            return\n")
			p("        fi\n")
		}
		p("        if [[ $cur == -* ]]; then\n")
		p("            COMPREPLY=($(compgen -W %q -- \"$cur\"))\n", strings.Join(all, " "))
		switch c.ArgsComplete {
		case "command":
			p("        else\n")
			p("            COMPREPLY=($(compgen -c -- \"$cur\" | sort -u))\n")
		case "shell":
			p("        else\n")
			p("            COMPREPLY=($(compgen -W %q -- \"$cur\"))\n", strings.Join(Shells, " "))
		case "file", "dir":
			p("        else\n")
			p("            %s_value %s\n", fn, c.ArgsComplete)
		}
		p("        fi\n")
		p("        ;;\n")
	}
	p("    help)\n")
	p("        COMPREPLY=($(compgen -W %q -- \"$cur\"))\n", strings.Join(cmdNames[:len(cmdNames)-1], " "))
	p("        ;;\n")
	p("    esac\n")
	p("}\n\n")
	p("complete -F %s %s\n", fn, a.Name)
	return b.String()
}

// zshQuote quotes s for a single-quoted zsh string used as an _arguments
// description, where brackets and colons are special.
func zshQuote(s string) string {
	r := strings.NewReplacer(`'`, `'\''`, `[`, `\[`, `]`, `\]`, `:`, `\:`)
	return r.Replace(s)
}

func zshAction(complete string) string {
	switch complete {
	case "file":
		return "_files"
	case "dir":
		return "_directories"
	case "user":
		return "_users"
	case "group":
		return "_groups"
	}
	return " "
}

func zshFlagSpecs(flags []*Flag) []string {
	var specs []string
	for _, f := range flags {
		names := dashedNames(f)
		forms := make([]string, len(names))
		for i, n := range names {
			switch {
			case f.Kind == Bool:
				forms[i] = n
			case len(n) == 2:
				forms[i] = n + "+" // value in the same or the next word
			default:
				forms[i] = n + "=" // --name=value or --name value
			}
		}
		excl := "(" + strings.Join(names, " ") + ")"
		if f.Kind == Strings {
			excl = "*"
		}
		if f == helpFlag || f == versionFlag {
			excl = "(- *)"
		}
		desc := "[" + zshQuote(f.Usage) + "]"
		value := ""
		if f.Kind != Bool {
			value = ":" + zshQuote(f.Placeholder) + ":" + zshAction(f.Complete)
		}
		if len(forms) == 1 {
			specs = append(specs, "'"+excl+forms[0]+desc+value+"'")
		} else {
			specs = append(specs, "'"+excl+"'{"+strings.Join(forms, ",")+"}'"+desc+value+"'")
		}
	}
	return specs
}

func (a *App) zshCompletion() string {
	fn := "_" + shellFuncName(a.Name)
	var b strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	p("#compdef %s\n", a.Name)
	p("# zsh completion for %s. Load it with: source <(%s completion zsh)\n\n", a.Name, a.Name)
	p("%s() {\n", fn)
	p("    local curcontext=$curcontext state line ret=1\n")
	p("    local -a commands\n")
	p("    commands=(\n")
	for _, c := range a.visibleCommands() {
		p("        '%s:%s'\n", c.Name, strings.ReplaceAll(c.Usage, "'", `'\''`))
	}
	p("        'help:Show the help of %s or of a command'\n", a.Name)
	p("    )\n")
	p("    _arguments -C \\\n")
	for _, s := range zshFlagSpecs(append(slices.Clone(a.Flags), helpFlag, versionFlag)) {
		p("        %s \\\n", s)
	}
	p("        '1: :->command' \\\n")
	p("        '*:: :->args' && ret=0\n")
	p("    case $state in\n")
	p("    command)\n")
	p("        _describe -t commands '%s command' commands && ret=0\n", a.Name)
	p("        ;;\n")
	p("    args)\n")
	p("        curcontext=${curcontext%%:*:*}:%s-$words[1]:\n", a.Name)
	p("        case $words[1] in\n")
	for _, c := range a.visibleCommands() {
		flags := append(slices.Clone(c.Flags), a.Flags...)
		flags = append(flags, helpFlag)
		p("        %s)\n", c.Name)
		opts := "-s -S"
		if c.StopAtFirstArg {
			opts += " -A '-*'"
		}
		p("            _arguments %s \\\n", opts)
		for _, s := range zshFlagSpecs(flags) {
			p("                %s \\\n", s)
		}
		switch c.ArgsComplete {
		case "command":
			p("                '(-)1:command: _command_names -e' \\\n")
			p("                '*::arguments: _normal' && ret=0\n")
		case "shell":
			p("                '1:shell:(%s)' && ret=0\n", strings.Join(Shells, " "))
		case "file", "dir":
			p("                '*:argument:%s' && ret=0\n", zshAction(c.ArgsComplete))
		default:
			p("                '*: :' && ret=0\n")
		}
		p("            ;;\n")
	}
	p("        help)\n")
	p("            _describe -t commands '%s command' commands && ret=0\n", a.Name)
	p("            ;;\n")
	p("        esac\n")
	p("        ;;\n")
	p("    esac\n")
	p("    return ret\n")
	p("}\n\n")
	p("if [[ $zsh_eval_context[-1] == loadautofunc ]]; then\n")
	p("    %s \"$@\"\n", fn)
	p("else\n")
	p("    compdef %s %s\n", fn, a.Name)
	p("fi\n")
	return b.String()
}

// fishQuote quotes s as a single-quoted fish string.
func fishQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

func (a *App) fishCompletion() string {
	var b strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	name := a.Name
	var cmdNames []string
	for _, c := range a.visibleCommands() {
		cmdNames = append(cmdNames, c.Name)
	}
	cmdNames = append(cmdNames, "help")

	flagLine := func(cond string, f *Flag) {
		line := "complete -c " + name + " -n " + fishQuote(cond)
		for _, n := range f.Names() {
			if len(n) == 1 {
				line += " -s " + n
			} else {
				line += " -l " + n
			}
		}
		if f.Kind != Bool {
			line += " -r"
			switch f.Complete {
			case "file":
				line += " -F"
			case "dir":
				line += " -f -a '(__fish_complete_directories)'"
			case "user":
				line += " -f -a '(__fish_complete_users)'"
			case "group":
				line += " -f -a '(__fish_complete_groups)'"
			default:
				line += " -f"
			}
		}
		p("%s -d %s\n", line, fishQuote(f.Usage))
	}

	fn := "__" + shellFuncName(name)
	p("# fish completion for %s. Install it with:\n", name)
	p("#   %s completion fish > ~/.config/fish/completions/%s.fish\n\n", name, name)
	// For "app SUB [OPTIONS] COMMAND [ARG...]": succeed and print the
	// words from COMMAND on when the words before the cursor reach COMMAND
	// (or "--"). $argv[1] is SUB, the other arguments are the options
	// taking a value.
	p("function %s_args\n", fn)
	p("    set -l valued $argv[2..-1]\n")
	p("    set -l words (commandline -opc)\n")
	p("    set -l n (count $words)\n")
	p("    set -l i 2\n")
	p("    while test $i -le $n; and string match -q -- '-*' $words[$i]\n")
	p("        set i (math $i + 1)\n")
	p("    end\n")
	p("    test $i -le $n; and test $words[$i] = $argv[1]; or return 1\n")
	p("    set i (math $i + 1)\n")
	p("    while test $i -le $n\n")
	p("        set -l w $words[$i]\n")
	p("        if test $w = --\n")
	p("            test $i -lt $n; and printf '%%s\\n' $words[(math $i + 1)..-1]\n")
	p("            return 0\n")
	p("        else if contains -- $w $valued\n")
	p("            set i (math $i + 2)\n")
	p("        else if string match -q -- '-*' $w\n")
	p("            set i (math $i + 1)\n")
	p("        else\n")
	p("            printf '%%s\\n' $words[$i..-1]\n")
	p("            return 0\n")
	p("        end\n")
	p("    end\n")
	p("    return 1\n")
	p("end\n\n")
	p("function %s_complete_args\n", fn)
	p("    set -l words (%s_args $argv)\n", fn)
	p("    if test (count $words) -eq 0\n")
	p("        __fish_complete_command\n")
	p("    else\n")
	p("        complete -C (string join -- ' ' (string escape -- $words) (commandline -ct))\n")
	p("    end\n")
	p("end\n\n")
	p("complete -c %s -f\n", name)
	for _, f := range append(slices.Clone(a.Flags), helpFlag, versionFlag) {
		flagLine("__fish_use_subcommand", f)
	}
	for _, c := range a.visibleCommands() {
		p("complete -c %s -n __fish_use_subcommand -a %s -d %s\n", name, c.Name, fishQuote(c.Usage))
	}
	p("complete -c %s -n __fish_use_subcommand -a help -d %s\n", name, fishQuote("Show the help of "+name+" or of a command"))
	for _, c := range a.visibleCommands() {
		flags := append(slices.Clone(c.Flags), a.Flags...)
		flags = append(flags, helpFlag)
		cond := "__fish_seen_subcommand_from " + c.Name
		argsCond := ""
		if c.StopAtFirstArg {
			argsCond = fn + "_args " + c.Name
			for _, f := range flags {
				if f.Kind != Bool {
					argsCond += " " + strings.Join(dashedNames(f), " ")
				}
			}
		}
		flagCond := cond
		if argsCond != "" {
			flagCond += "; and not " + argsCond
		}
		for _, f := range flags {
			flagLine(flagCond, f)
		}
		switch c.ArgsComplete {
		case "command":
			if argsCond != "" {
				p("complete -c %s -n %s -a '(%s_complete_args %s)'\n", name, fishQuote(argsCond), fn, strings.TrimPrefix(argsCond, fn+"_args "))
				p("complete -c %s -n %s -a '(__fish_complete_command)'\n", name, fishQuote(flagCond))
			} else {
				p("complete -c %s -n %s -a '(__fish_complete_command)'\n", name, fishQuote(cond))
			}
		case "shell":
			p("complete -c %s -n %s -a %s\n", name, fishQuote(cond), fishQuote(strings.Join(Shells, " ")))
		case "file", "dir":
			p("complete -c %s -n %s -F\n", name, fishQuote(cond))
		}
	}
	p("complete -c %s -n %s -a %s\n", name, fishQuote("__fish_seen_subcommand_from help"), fishQuote(strings.Join(cmdNames[:len(cmdNames)-1], " ")))
	return b.String()
}
