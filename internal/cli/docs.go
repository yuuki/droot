package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

// paragraphs splits a description into paragraphs (separated by blank
// lines). A paragraph with an indented line is preformatted; the lines of
// the others are joined.
func paragraphs(text string) (paras []string, pre []bool) {
	for p := range strings.SplitSeq(strings.TrimSpace(text), "\n\n") {
		lines := strings.Split(p, "\n")
		indented := slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, " ") })
		if indented {
			// Remove the common indentation.
			indent := -1
			for _, l := range lines {
				if strings.TrimSpace(l) == "" {
					continue
				}
				n := len(l) - len(strings.TrimLeft(l, " "))
				if indent < 0 || n < indent {
					indent = n
				}
			}
			for i, l := range lines {
				if len(l) >= indent {
					lines[i] = l[indent:]
				}
			}
			paras = append(paras, strings.Join(lines, "\n"))
		} else {
			for i, l := range lines {
				lines[i] = strings.TrimSpace(l)
			}
			paras = append(paras, strings.Join(lines, " "))
		}
		pre = append(pre, indented)
	}
	return paras, pre
}

// docCommands returns the commands to document, in the order of the help.
func (a *App) docCommands() []*Command { return a.visibleCommands() }

func commandSynopsis(a *App, c *Command) string {
	s := a.Name + " " + c.Name
	if len(c.Flags) > 0 {
		s += " [options]"
	}
	if c.ArgsUsage != "" {
		s += " " + c.ArgsUsage
	}
	return s
}

// WriteMarkdown writes a Markdown reference of the commands and options.
func (a *App) WriteMarkdown(w io.Writer) error {
	var b strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	mdText := func(s string) string {
		return strings.NewReplacer("|", `\|`, "<", `\<`, ">", `\>`, "*", `\*`, "_", `\_`).Replace(s)
	}
	description := func(text string) {
		paras, pre := paragraphs(text)
		for i, para := range paras {
			if pre[i] {
				p("```\n%s\n```\n\n", para)
			} else {
				p("%s\n\n", mdText(para))
			}
		}
	}
	options := func(flags []*Flag) {
		p("| Option | Description |\n|---|---|\n")
		for _, f := range flags {
			p("| `%s` | %s |\n", FlagSynopsis(f), mdText(FlagUsage(f)))
		}
		p("\n")
	}

	p("# %s command line reference\n\n", a.Name)
	p("<!-- Generated from the command definitions by `make docs`; do not edit. -->\n\n")
	p("%s — %s.\n\n", a.Name, mdText(a.Usage))
	p("```\n%s [global options] COMMAND [options] [ARG...]\n```\n\n", a.Name)
	description(a.Description)
	p("Global options, accepted before and after COMMAND:\n\n")
	options(append(slices.Clone(a.Flags), helpFlag, versionFlag))
	p("Commands:\n\n")
	for _, c := range a.docCommands() {
		p("- [`%s %s`](#%s-%s): %s\n", a.Name, c.Name, a.Name, c.Name, mdText(c.Usage))
	}
	p("\n")
	for _, c := range a.docCommands() {
		p("## %s %s\n\n", a.Name, c.Name)
		p("%s.\n\n", mdText(c.Usage))
		p("```\n%s\n```\n\n", commandSynopsis(a, c))
		description(c.Description)
		options(append(slices.Clone(c.Flags), helpFlag))
	}
	_, err := io.WriteString(w, strings.TrimRight(b.String(), "\n")+"\n")
	return err
}

// roff escapes text for a man page.
func roff(s string) string {
	s = strings.NewReplacer(`\`, `\e`, "-", `\-`).Replace(s)
	var lines []string
	for l := range strings.SplitSeq(s, "\n") {
		if strings.HasPrefix(l, ".") || strings.HasPrefix(l, "'") {
			l = `\&` + l
		}
		lines = append(lines, l)
	}
	return strings.Join(lines, "\n")
}

// WriteMan writes a man page of section 1 documenting every command.
// environment lists the environment variables to document, as NAME and
// description pairs.
func (a *App) WriteMan(w io.Writer, environment [][2]string) error {
	var b strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	description := func(text string) {
		paras, pre := paragraphs(text)
		for i, para := range paras {
			if pre[i] {
				p(".PP\n.nf\n.RS 4\n%s\n.RE\n.fi\n", roff(para))
			} else {
				p(".PP\n%s\n", roff(para))
			}
		}
	}
	options := func(flags []*Flag) {
		for _, f := range flags {
			syn := FlagSynopsis(f)
			name, placeholder, _ := strings.Cut(syn, " ")
			if f.Kind == Bool {
				name = syn
			}
			p(".TP\n\\fB%s\\fR", roff(name))
			if f.Kind != Bool {
				p(" \\fI%s\\fR", roff(placeholder))
			}
			p("\n%s\n", roff(FlagUsage(f)))
		}
	}
	upper := strings.ToUpper(a.Name)
	p(".\\\" Generated from the command definitions by `make docs`; do not edit.\n")
	p(".TH %s 1 \"\" \"%s\" \"User Commands\"\n", upper, a.Name)
	p(".SH NAME\n%s \\- %s\n", a.Name, roff(a.Usage))
	p(".SH SYNOPSIS\n")
	p(".B %s\n[\\fIglobal options\\fR] \\fICOMMAND\\fR [\\fIoptions\\fR] [\\fIARG\\fR...]\n", a.Name)
	p(".SH DESCRIPTION\n")
	description(a.Description)
	p(".SH GLOBAL OPTIONS\n")
	p(".PP\nAccepted before and after \\fICOMMAND\\fR.\n")
	options(append(slices.Clone(a.Flags), helpFlag, versionFlag))
	p(".SH COMMANDS\n")
	for _, c := range a.docCommands() {
		p(".SS %s\n", roff(commandSynopsis(a, c)))
		p(".PP\n%s.\n", roff(c.Usage))
		description(c.Description)
		options(append(slices.Clone(c.Flags), helpFlag))
	}
	if len(environment) > 0 {
		p(".SH ENVIRONMENT\n")
		for _, e := range environment {
			p(".TP\n.B %s\n%s\n", roff(e[0]), roff(e[1]))
		}
	}
	if a.Homepage != "" {
		p(".SH SEE ALSO\n.PP\n%s\n", roff(a.Homepage))
	}
	_, err := io.WriteString(w, b.String())
	return err
}
