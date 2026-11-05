package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type result struct {
	cmd   string
	args  []string
	name  string
	names []string
	flag  bool
	debug bool
}

func testApp(got *result) *App {
	action := func(_ context.Context, inv *Invocation) error {
		*got = result{
			cmd: inv.Command.Name, args: inv.Args(), name: inv.String("name"), names: inv.Strings("item"),
			flag: inv.Bool("flag"), debug: inv.Bool("debug"),
		}
		return nil
	}
	flags := []*Flag{
		{Name: "name", Aliases: []string{"n"}, Kind: String, Placeholder: "NAME", Usage: "a name", EnvVar: "APP_TEST_NAME"},
		{Name: "item", Aliases: []string{"i"}, Kind: Strings, Placeholder: "ITEM", Usage: "an item"},
		{Name: "flag", Aliases: []string{"f", "fl"}, Kind: Bool, Usage: "a flag"},
	}
	return &App{
		Name:    "app",
		Usage:   "test app",
		Version: "1.0",
		Stdout:  &strings.Builder{},
		Flags:   []*Flag{{Name: "debug", Aliases: []string{"D"}, Kind: Bool, Usage: "debug", EnvVar: "APP_TEST_DEBUG"}},
		Commands: []*Command{
			{Name: "mixed", Usage: "flags anywhere", Flags: flags, Action: action},
			{Name: "exec", Usage: "flags before COMMAND", Flags: flags, StopAtFirstArg: true, ArgsComplete: "command", Action: action},
		},
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		argv []string
		want result
	}{
		{[]string{"mixed", "a", "-n", "x", "b", "--flag"}, result{cmd: "mixed", args: []string{"a", "b"}, name: "x", flag: true}},
		{[]string{"mixed", "--name=x=y", "-n", "z"}, result{cmd: "mixed", name: "z"}},
		{[]string{"mixed", "-i", "a,b", "--item", "c", "-i=d"}, result{cmd: "mixed", names: []string{"a,b", "c", "d"}}},
		{[]string{"mixed", "--", "-n", "x"}, result{cmd: "mixed", args: []string{"-n", "x"}}},
		{[]string{"mixed", "-n", "--", "x"}, result{cmd: "mixed", name: "--", args: []string{"x"}}},
		{[]string{"mixed", "-", "--fl=false"}, result{cmd: "mixed", args: []string{"-"}}},
		{[]string{"mixed", "-fl", "--f"}, result{cmd: "mixed", flag: true}},
		{[]string{"mixed", "--name", ""}, result{cmd: "mixed"}},
		{[]string{"-D", "mixed"}, result{cmd: "mixed", debug: true}},
		{[]string{"mixed", "--debug"}, result{cmd: "mixed", debug: true}},
		{[]string{"exec", "-n", "x", "ls", "-n", "y", "--", "--help"}, result{cmd: "exec", name: "x", args: []string{"ls", "-n", "y", "--", "--help"}}},
		{[]string{"exec", "--", "--", "x"}, result{cmd: "exec", args: []string{"--", "x"}}},
		{[]string{"exec", "-f", "--", "ls"}, result{cmd: "exec", flag: true, args: []string{"ls"}}},
	}
	for _, tt := range tests {
		var got result
		if err := testApp(&got).Run(context.Background(), append([]string{"app"}, tt.argv...)); err != nil {
			t.Errorf("app %q: %v", tt.argv, err)
			continue
		}
		if got.cmd != tt.want.cmd || !slices.Equal(got.args, tt.want.args) || got.name != tt.want.name ||
			!slices.Equal(got.names, tt.want.names) || got.flag != tt.want.flag || got.debug != tt.want.debug {
			t.Errorf("app %q:\ngot  %+v\nwant %+v", tt.argv, got, tt.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		argv []string
		want string
	}{
		{[]string{"nope"}, "'nope' is not a app command (see 'app --help')"},
		{[]string{"mixd"}, "did you mean 'mixed'?"},
		{[]string{"help", "exce"}, "no help topic for 'exce'; did you mean 'exec'?"},
		{[]string{"mixed", "--nam", "x"}, "unknown flag --nam; did you mean --name? (see 'app mixed --help')"},
		{[]string{"mixed", "-nme=x"}, "unknown flag -nme; did you mean --name?"},
		{[]string{"mixed", "-", "-x"}, "unknown flag -x (see 'app mixed --help')"},
		{[]string{"mixed", "-n"}, "flag -n needs an argument"},
		{[]string{"mixed", "--flag=yes"}, `invalid value "yes" for flag --flag`},
		{[]string{"mixed", "--=x"}, "unknown flag --"},
		{[]string{"--name", "x", "mixed"}, "unknown flag --name (see 'app --help')"},
	}
	for _, tt := range tests {
		var got result
		err := testApp(&got).Run(context.Background(), append([]string{"app"}, tt.argv...))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("app %q: got %v, want %q", tt.argv, err, tt.want)
		} else if !IsUsageError(err) {
			t.Errorf("app %q: %v is not a usage error", tt.argv, err)
		}
	}
}

func TestEnvVar(t *testing.T) {
	var got result
	t.Setenv("APP_TEST_DEBUG", "1")
	if err := testApp(&got).Run(context.Background(), []string{"app", "mixed"}); err != nil || !got.debug {
		t.Fatalf("APP_TEST_DEBUG=1: %+v, %v", got, err)
	}
	if err := testApp(&got).Run(context.Background(), []string{"app", "-D=false", "mixed"}); err != nil || got.debug {
		t.Fatalf("the flag must take precedence: %+v, %v", got, err)
	}
	t.Setenv("APP_TEST_DEBUG", "")
	if err := testApp(&got).Run(context.Background(), []string{"app", "mixed"}); err != nil || got.debug {
		t.Fatalf("an empty variable is ignored: %+v, %v", got, err)
	}
	t.Setenv("APP_TEST_NAME", "from-env")
	if err := testApp(&got).Run(context.Background(), []string{"app", "mixed"}); err != nil || got.name != "from-env" {
		t.Fatalf("APP_TEST_NAME: %+v, %v", got, err)
	}
	if err := testApp(&got).Run(context.Background(), []string{"app", "mixed", "-n", "flag"}); err != nil || got.name != "flag" {
		t.Fatalf("the flag must take precedence: %+v, %v", got, err)
	}
	t.Setenv("APP_TEST_DEBUG", "maybe")
	if err := testApp(&got).Run(context.Background(), []string{"app", "mixed"}); err == nil {
		t.Fatal("an invalid value must be an error")
	}
}

func TestHelp(t *testing.T) {
	for _, tt := range []struct {
		argv []string
		want []string
	}{
		{nil, []string{"USAGE:\n   app [global options] COMMAND", "VERSION:\n   1.0", "mixed  ", "completion  ", "-D, --debug", "[$APP_TEST_DEBUG]", "-v, --version"}},
		{[]string{"-h"}, []string{"COMMANDS:"}},
		{[]string{"help"}, []string{"COMMANDS:"}},
		{[]string{"help", "-x", "exec"}, []string{"app exec - flags before COMMAND", "-f, --flag, --fl"}},
		{[]string{"exec", "--help", "ls"}, []string{"-n, --name NAME", "-i, --item ITEM", "(repeatable)", "GLOBAL OPTIONS:\n   -D, --debug"}},
		{[]string{"mixed", "a", "-h"}, []string{"app mixed [options]"}},
		{[]string{"completion", "-h"}, []string{"bash|zsh|fish"}},
		{[]string{"--version"}, []string{"app version 1.0\n"}},
	} {
		var got result
		app := testApp(&got)
		if err := app.Run(context.Background(), append([]string{"app"}, tt.argv...)); err != nil {
			t.Errorf("app %q: %v", tt.argv, err)
			continue
		}
		out := app.Stdout.(*strings.Builder).String()
		if got.cmd != "" {
			t.Errorf("app %q ran %s", tt.argv, got.cmd)
		}
		for _, w := range tt.want {
			if !strings.Contains(out, w) {
				t.Errorf("app %q: %q not in\n%s", tt.argv, w, out)
			}
		}
		for line := range strings.SplitSeq(out, "\n") {
			if len(line) > helpWidth || strings.TrimRight(line, " ") != line {
				t.Errorf("app %q: line too long or with trailing spaces: %q", tt.argv, line)
			}
		}
	}
}

func TestWrap(t *testing.T) {
	got := wrap("aaa bbb ccc  dddddddddd e", 7)
	want := []string{"aaa bbb", "ccc", "dddddddddd", "e"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEditDistance(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want int
	}{{"run", "run", 0}, {"rnu", "run", 1}, {"rot", "root", 1}, {"exprot", "export", 1}, {"abc", "xyz", 3}, {"", "ab", 2}} {
		if got := EditDistance(tt.a, tt.b); got != tt.want {
			t.Errorf("EditDistance(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
	if got := Closest("exce", []string{"exec", "e", "mixed"}); got != "exec" {
		t.Errorf("Closest = %q", got)
	}
	if got := Closest("zzzz", []string{"exec"}); got != "" {
		t.Errorf("Closest = %q", got)
	}
}

// TestCompletionScripts checks the syntax of the generated scripts with the
// shells that are installed, and runs the bash one.
func TestCompletionScripts(t *testing.T) {
	var got result
	app := testApp(&got)
	dir := t.TempDir()
	for _, shell := range Shells {
		var b strings.Builder
		if err := app.WriteCompletion(&b, shell); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "completion."+shell)
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := exec.LookPath(shell); err != nil {
			t.Logf("%s not installed: syntax not checked", shell)
			continue
		}
		if out, err := exec.Command(shell, "-n", path).CombinedOutput(); err != nil {
			t.Errorf("%s -n: %v\n%s\n%s", shell, err, out, b.String())
		}
	}

	if _, err := exec.LookPath("bash"); err != nil {
		return
	}
	script := `source "$1"
cd "$2"
t() { COMP_WORDS=("$@"); COMP_CWORD=$((${#COMP_WORDS[@]}-1)); COMPREPLY=(); _app; echo "${COMPREPLY[*]}"; }
t app ''
t app mixed --n
t app exec -n x --
t app exec -n x ls --n
t app exec ls -n ''
t app completion ''
t app help m
`
	// After COMMAND, "-n" belongs to COMMAND: its argument is completed as
	// a file, not as the value of droot's -n.
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "only-file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("bash", "-c", script, "bash", filepath.Join(dir, "completion.bash"), cwd).CombinedOutput()
	if err != nil {
		t.Fatalf("bash: %v\n%s", err, out)
	}
	want := "mixed exec completion help\n--name\n--name --item --flag --fl --debug --help\n\nonly-file\nbash zsh fish\nmixed\n"
	if string(out) != want {
		t.Errorf("bash completion:\ngot  %q\nwant %q", out, want)
	}
}

func TestApplyConfig(t *testing.T) {
	var got result
	app := testApp(&got)
	cmd := app.Command("mixed")
	run := func(config map[string]any, argv ...string) error {
		orig := cmd.Action
		cmd.Action = func(ctx context.Context, inv *Invocation) error {
			if err := inv.ApplyConfig(config); err != nil {
				return err
			}
			return orig(ctx, inv)
		}
		defer func() { cmd.Action = orig }()
		return app.Run(context.Background(), append([]string{"app", "mixed"}, argv...))
	}
	config := map[string]any{"name": "from-file", "item": []string{"f1", "f2"}, "flag": true}
	if err := run(config); err != nil || got.name != "from-file" || !slices.Equal(got.names, []string{"f1", "f2"}) || !got.flag {
		t.Fatalf("from the file: %+v, %v", got, err)
	}
	if err := run(config, "-n", "cli", "-i", "c1", "--flag=false"); err != nil || got.name != "cli" ||
		!slices.Equal(got.names, []string{"f1", "f2", "c1"}) || got.flag {
		t.Fatalf("the command line takes precedence: %+v, %v", got, err)
	}
	if err := run(map[string]any{"item": "single"}); err != nil || !slices.Equal(got.names, []string{"single"}) {
		t.Fatalf("a string for a repeatable flag: %+v, %v", got, err)
	}
	for _, bad := range []map[string]any{
		{"nmae": "x"}, {"debug": true}, {"n": "x"},
		{"flag": "yes"}, {"name": true}, {"item": true},
	} {
		if err := run(bad); err == nil {
			t.Errorf("ApplyConfig(%v) should fail", bad)
		}
	}
	if err := run(map[string]any{"nmae": "x"}); err == nil || !strings.Contains(err.Error(), `did you mean "name"?`) {
		t.Errorf("want a suggestion, got %v", err)
	}
}
