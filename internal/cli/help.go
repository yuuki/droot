package cli

import (
	"fmt"
	"io"
	"strings"
)

// helpWidth is the width the help text is wrapped to.
const helpWidth = 80

// WriteHelp writes the help of cmd, or of the app if cmd is nil.
func (a *App) WriteHelp(w io.Writer, cmd *Command) error {
	var b strings.Builder
	section := func(title string) { fmt.Fprintf(&b, "\n%s:\n", title) }
	indent := func(text string) {
		for line := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
			if line == "" {
				b.WriteString("\n")
			} else {
				b.WriteString("   " + line + "\n")
			}
		}
	}

	if cmd == nil {
		b.WriteString("NAME:\n")
		indent(a.Name + " - " + a.Usage)
		section("USAGE")
		indent(a.Name + " [global options] COMMAND [options] [ARG...]")
		if a.Version != "" {
			section("VERSION")
			indent(a.Version)
		}
		if a.Description != "" {
			section("DESCRIPTION")
			indent(a.Description)
		}
		section("COMMANDS")
		var rows [][2]string
		for _, c := range a.allCommands() {
			if !c.Hidden {
				rows = append(rows, [2]string{c.Name, c.Usage})
			}
		}
		rows = append(rows, [2]string{"help", "Show the help of " + a.Name + " or of a command"})
		indent(table(rows, 3))
		section("GLOBAL OPTIONS")
		indent(flagTable(append(append(a.Flags[:len(a.Flags):len(a.Flags)], helpFlag), versionFlag)))
	} else {
		b.WriteString("NAME:\n")
		indent(a.Name + " " + cmd.Name + " - " + cmd.Usage)
		section("USAGE")
		usage := a.Name + " " + cmd.Name
		if len(cmd.Flags) > 0 {
			usage += " [options]"
		}
		if cmd.ArgsUsage != "" {
			usage += " " + cmd.ArgsUsage
		}
		indent(usage)
		if cmd.Description != "" {
			section("DESCRIPTION")
			indent(cmd.Description)
		}
		section("OPTIONS")
		indent(flagTable(append(cmd.Flags[:len(cmd.Flags):len(cmd.Flags)], helpFlag)))
		if len(a.Flags) > 0 {
			section("GLOBAL OPTIONS")
			indent(flagTable(a.Flags))
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// FlagSynopsis returns the flag as shown in the help: "-r, --root ROOT_DIR".
func FlagSynopsis(f *Flag) string {
	var short, long []string
	for _, n := range f.Names() {
		if len(n) == 1 {
			short = append(short, "-"+n)
		} else {
			long = append(long, "--"+n)
		}
	}
	s := strings.Join(append(short, long...), ", ")
	if f.Kind != Bool {
		s += " " + f.Placeholder
	}
	return s
}

// FlagUsage returns the description of the flag as shown in the help.
func FlagUsage(f *Flag) string {
	u := f.Usage
	if f.Kind == Strings {
		u += " (repeatable)"
	}
	if f.EnvVar != "" {
		u += " [$" + f.EnvVar + "]"
	}
	return u
}

func flagTable(flags []*Flag) string {
	rows := make([][2]string, 0, len(flags))
	for _, f := range flags {
		rows = append(rows, [2]string{FlagSynopsis(f), FlagUsage(f)})
	}
	return table(rows, 3)
}

// table formats two columns, wrapping the second one. A first column wider
// than a third of the width puts the second one on the next line.
func table(rows [][2]string, indent int) string {
	width := 0
	for _, r := range rows {
		width = max(width, len(r[0]))
	}
	maxCol := (helpWidth - indent) / 3
	width = min(width, maxCol)
	var b strings.Builder
	for _, r := range rows {
		textCol := width + 2
		lines := wrap(r[1], helpWidth-indent-textCol)
		if len(lines) == 0 {
			b.WriteString(r[0] + "\n")
			continue
		}
		if len(r[0]) > width {
			b.WriteString(r[0] + "\n")
			b.WriteString(strings.Repeat(" ", textCol))
		} else {
			b.WriteString(r[0] + strings.Repeat(" ", textCol-len(r[0])))
		}
		for i, l := range lines {
			if i > 0 {
				b.WriteString(strings.Repeat(" ", textCol))
			}
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

// wrap splits text into lines of at most width bytes, breaking at spaces.
func wrap(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case len(line)+1+len(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}
