package commands

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yuuki/droot/internal/cli"
	"github.com/yuuki/droot/internal/log"
	"github.com/yuuki/droot/internal/mounter"
	"github.com/yuuki/droot/internal/osutil"
)

type parsedRun struct {
	args                 []string
	root, user, group    string
	bind, robind, env    []string
	copyFiles, noDrop, D bool
}

// parseRun runs the real CLI definition with the run action replaced, so that
// only flag parsing is exercised.
func parseRun(t *testing.T, argv ...string) (parsedRun, error) {
	t.Helper()
	var got parsedRun
	app := NewApp("test")
	app.Stdout = io.Discard
	app.Command("run").Action = func(_ context.Context, cmd *cli.Invocation) error {
		got = parsedRun{
			args: cmd.Args(), root: cmd.String("root"), user: cmd.String("user"), group: cmd.String("group"),
			bind: cmd.Strings("bind"), robind: cmd.Strings("robind"), env: cmd.Strings("env"),
			copyFiles: cmd.Bool("copy-files"), noDrop: cmd.Bool("no-dropcaps"), D: cmd.Bool("debug"),
		}
		return nil
	}
	err := app.Run(context.Background(), append([]string{"droot"}, argv...))
	return got, err
}

func TestRunFlagParsing(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want parsedRun
	}{
		{
			name: "README example",
			argv: []string{"run", "--cp", "--user", "appuser", "--group", "appgroup", "--bind", "/var/log/app", "--root", "/var/containers/app", "--", "command"},
			want: parsedRun{args: []string{"command"}, root: "/var/containers/app", user: "appuser", group: "appgroup", bind: []string{"/var/log/app"}, copyFiles: true},
		},
		{
			name: "flags of the command are not parsed by droot",
			argv: []string{"run", "-r", "/c", "--", "ls", "-la", "--root", "/x"},
			want: parsedRun{args: []string{"ls", "-la", "--root", "/x"}, root: "/c"},
		},
		{
			name: "without -- parsing stops at the command",
			argv: []string{"run", "-r", "/c", "ls", "-la"},
			want: parsedRun{args: []string{"ls", "-la"}, root: "/c"},
		},
		{
			name: "-- after COMMAND is kept (droot 0.7 behavior)",
			argv: []string{"run", "-r", "/c", "echo", "--", "x"},
			want: parsedRun{args: []string{"echo", "--", "x"}, root: "/c"},
		},
		{
			name: "-- after COMMAND with a leading --",
			argv: []string{"run", "--root=/c", "--", "printf", "--", "-n"},
			want: parsedRun{args: []string{"printf", "--", "-n"}, root: "/c"},
		},
		{
			name: "trailing --",
			argv: []string{"run", "-e", "A=1", "-r", "/c", "--cp", "echo", "--"},
			want: parsedRun{args: []string{"echo", "--"}, root: "/c", env: []string{"A=1"}, copyFiles: true},
		},
		{
			name: "a COMMAND named help",
			argv: []string{"run", "-r", "/c", "--", "help"},
			want: parsedRun{args: []string{"help"}, root: "/c"},
		},
		{
			name: "a COMMAND named h without --",
			argv: []string{"run", "-r", "/c", "h", "x"},
			want: parsedRun{args: []string{"h", "x"}, root: "/c"},
		},
		{
			name: "flags of droot after COMMAND belong to COMMAND",
			argv: []string{"run", "-r", "/c", "echo", "--help", "-D", "--root", "/x"},
			want: parsedRun{args: []string{"echo", "--help", "-D", "--root", "/x"}, root: "/c"},
		},
		{
			name: "boolean flags with a value",
			argv: []string{"run", "--cp=true", "--no-dropcaps=false", "-D=1", "-r", "/c", "true"},
			want: parsedRun{args: []string{"true"}, root: "/c", copyFiles: true, D: true},
		},
		{
			name: "a lone dash is COMMAND",
			argv: []string{"run", "-r", "/c", "-", "x"},
			want: parsedRun{args: []string{"-", "x"}, root: "/c"},
		},
		{
			name: "repeatable flags and commas",
			argv: []string{"run", "-r", "/c", "-b", "/a", "--bind", "/b,c:/d", "--robind", "/ro", "-e", "JAVA_OPTS=-Xmx1g,-Xms1g", "-e", "A=1", "--", "true"},
			want: parsedRun{args: []string{"true"}, root: "/c", bind: []string{"/a", "/b,c:/d"}, robind: []string{"/ro"}, env: []string{"JAVA_OPTS=-Xmx1g,-Xms1g", "A=1"}},
		},
		{
			name: "long names, single dash alias and debug after the subcommand",
			argv: []string{"run", "--copy-files", "--no-dropcaps", "-D", "-u", "1000", "-g", "1000", "--root=/c", "--", "id"},
			want: parsedRun{args: []string{"id"}, root: "/c", user: "1000", group: "1000", copyFiles: true, noDrop: true, D: true},
		},
		{
			name: "debug before the subcommand",
			argv: []string{"--debug", "run", "-r", "/c", "id"},
			want: parsedRun{args: []string{"id"}, root: "/c", D: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRun(t, tt.argv...)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.args, tt.want.args) || got.root != tt.want.root || got.user != tt.want.user ||
				got.group != tt.want.group || !slices.Equal(got.bind, tt.want.bind) || !slices.Equal(got.robind, tt.want.robind) ||
				!slices.Equal(got.env, tt.want.env) || got.copyFiles != tt.want.copyFiles || got.noDrop != tt.want.noDrop || got.D != tt.want.D {
				t.Fatalf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestHelpStillWorks(t *testing.T) {
	for _, argv := range [][]string{{"help", "run"}, {"h", "run"}, {"run", "--help"}, {"run", "-h"}, {"run", "-r", "/x", "-h"}, {"export", "-h"}, {"export", "img", "--help"}, {"--help"}, {"help"}, {}} {
		var out strings.Builder
		app := NewApp("test")
		app.Stdout = &out
		for _, c := range app.Commands {
			c.Action = func(context.Context, *cli.Invocation) error {
				t.Errorf("droot %q ran the action instead of showing help", argv)
				return nil
			}
		}
		if err := app.Run(context.Background(), append([]string{"droot"}, argv...)); err != nil {
			t.Errorf("droot %q: %v", argv, err)
		}
		if !strings.Contains(out.String(), "USAGE") {
			t.Errorf("droot %q printed no help: %q", argv, out.String())
		}
	}
}

func TestDebugFromEnv(t *testing.T) {
	t.Setenv("DROOT_DEBUG", "1")
	got, err := parseRun(t, "run", "-r", "/c", "true")
	if err != nil {
		t.Fatal(err)
	}
	if !got.D {
		t.Fatal("DROOT_DEBUG=1 should enable debug output")
	}
}

func TestUsageErrors(t *testing.T) {
	defer log.SetDebug(false)
	for _, argv := range [][]string{
		{"nosuch"},
		{"run", "--bogus"},
		{"run", "--root", "/tmp"},          // no command
		{"run", "--", "true"},              // no --root
		{"export"},                         // no image
		{"export", "a", "b"},               // two images
		{"umount"},                         // no --root
		{"umount", "--root", "/", "extra"}, // stray argument
		{"status"},                         // no --root
		{"status", "--root", "/tmp", "x"},  // stray argument
		{"run", "--root", "/tmp", "--hostname", "", "true"},
		{"run", "--root", "/tmp", "--user", "", "true"},
		{"run", "--root", "/tmp", "--group=", "true"},
	} {
		app := NewApp("test")
		app.Stdout = io.Discard
		err := app.Run(context.Background(), append([]string{"droot"}, argv...))
		if err == nil || !strings.Contains(err.Error(), "--help") {
			t.Errorf("droot %q: want usage error, got %v", argv, err)
		}
	}
}

func TestUmountRefusesRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		// If the refusal ever regressed, this would detach every mount of
		// the machine. mounter.TestResolveRootDir covers it without risk.
		t.Skip("not run as root")
	}
	app := NewApp("test")
	app.Stdout = io.Discard
	err := app.Run(context.Background(), []string{"droot", "umount", "--root", "/"})
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("umount --root / must be refused, got %v", err)
	}
}

func TestParseBindOption(t *testing.T) {
	ro := mounter.BindOptions{ReadOnly: true}
	tests := []struct {
		spec     string
		readonly bool
		want     bindMount
		wantErr  bool
	}{
		{spec: "/var/log/app", want: bindMount{host: "/var/log/app", container: "/var/log/app"}},
		{spec: "/var/log/app/", want: bindMount{host: "/var/log/app", container: "/var/log/app"}},
		{spec: "/srv/data:/data", want: bindMount{host: "/srv/data", container: "/data"}},
		{spec: "/srv/data:", want: bindMount{host: "/srv/data", container: "/srv/data"}},
		{spec: "/a:/b:c", want: bindMount{host: "/a", container: "/b:c"}},
		{spec: "/a:/b:c:ro", want: bindMount{host: "/a", container: "/b:c", opts: ro}},
		{spec: "/a:/b:ro", want: bindMount{host: "/a", container: "/b", opts: ro}},
		{spec: "/a:/b:rw", want: bindMount{host: "/a", container: "/b"}},
		{spec: "/a:/b:rbind,ro", want: bindMount{host: "/a", container: "/b", opts: mounter.BindOptions{ReadOnly: true, Recursive: true}}},
		{spec: "/a:ro", want: bindMount{host: "/a", container: "/a", opts: ro}},
		{spec: "/a::ro", want: bindMount{host: "/a", container: "/a", opts: ro}},
		{spec: "/a:/b:rox", want: bindMount{host: "/a", container: "/b:rox"}},
		{spec: "/a:/b", readonly: true, want: bindMount{host: "/a", container: "/b", opts: ro}},
		{spec: "/a:/b:rbind", readonly: true, want: bindMount{host: "/a", container: "/b", opts: mounter.BindOptions{ReadOnly: true, Recursive: true}}},
		{spec: "/a:/b:rw", readonly: true, wantErr: true},
		{spec: "/a:/b:ro,rw", wantErr: true},
		{spec: "/a:rbind:ro", wantErr: true}, // DEST "rbind" is not absolute
		{spec: "relative", wantErr: true},
		{spec: "/abs:relative", wantErr: true},
		{spec: "", wantErr: true},
	}
	for _, tt := range tests {
		got, err := parseBindOption(tt.spec, tt.readonly)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseBindOption(%q, %v) should fail, got %+v", tt.spec, tt.readonly, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("parseBindOption(%q, %v) = %+v, %v; want %+v", tt.spec, tt.readonly, got, err, tt.want)
		}
	}
}

func TestWriteFileIsAtomic(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "app.tar")
	if err := os.WriteFile(out, []byte("previous"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := writeFile(context.Background(), out, func(w io.Writer) error {
		_, _ = io.WriteString(w, "partial")
		return errors.New("export failed")
	})
	if err == nil {
		t.Fatal("want error")
	}
	if b, _ := os.ReadFile(out); string(b) != "previous" {
		t.Fatalf("a failed export must not clobber the output, got %q", b)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}

	if err := writeFile(context.Background(), out, func(w io.Writer) error {
		_, err := io.WriteString(w, "archive")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); string(b) != "archive" || fi.Mode().Perm() != 0o644 {
		t.Fatalf("got %q mode %v", b, fi.Mode())
	}

	if err := writeFile(context.Background(), dir, func(io.Writer) error { return nil }); err == nil {
		t.Fatal("writing to a directory should fail")
	}
}

func TestCancelableWriterUnblocks(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cw := cancelableWriter{ctx: ctx, w: w}

	done := make(chan error, 1)
	go func() {
		// Nobody reads the pipe: this blocks once its buffer is full.
		buf := make([]byte, 1<<20)
		for {
			if _, err := cw.Write(buf); err != nil {
				done <- err
				return
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a blocked write was not interrupted by the cancellation")
	}
}

func TestOpenCancelableFIFO(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := openCancelable(ctx, fifo); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("opening a FIFO without reader should give up, got %v", err)
	}
}

func TestWriteFileLikeCreate(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	write := func(content string) func(io.Writer) error {
		return func(w io.Writer) error {
			_, err := io.WriteString(w, content)
			return err
		}
	}
	mode := func(p string) os.FileMode {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Mode().Perm()
	}

	old := syscall.Umask(0o077)
	newFile := filepath.Join(dir, "new.tar")
	err := writeFile(ctx, newFile, write("a"))
	syscall.Umask(old)
	if err != nil {
		t.Fatal(err)
	}
	if m := mode(newFile); m != 0o600 {
		t.Errorf("a new file must follow the umask: mode %v", m)
	}

	existing := filepath.Join(dir, "existing.tar")
	if err := os.WriteFile(existing, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existing, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(ctx, existing, write("b")); err != nil {
		t.Fatal(err)
	}
	if m := mode(existing); m != 0o640 {
		t.Errorf("an existing file must keep its mode: %v", m)
	}

	link := filepath.Join(dir, "link.tar")
	if err := os.Symlink("existing.tar", link); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(ctx, link, write("c")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&fs.ModeSymlink == 0 {
		t.Error("the symlink must be kept")
	}
	if b, _ := os.ReadFile(existing); string(b) != "c" {
		t.Errorf("the symlink target must be written, got %q", b)
	}

	dangling := filepath.Join(dir, "dangling.tar")
	if err := os.Symlink("created.tar", dangling); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(ctx, dangling, write("d")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "created.tar")); string(b) != "d" {
		t.Errorf("the target of a dangling symlink must be created, got %q", b)
	}

	hard := filepath.Join(dir, "hard.tar")
	if err := os.Link(existing, hard); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(ctx, hard, write("e")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(existing); string(b) != "e" {
		t.Errorf("hard links must be kept, the other link reads %q", b)
	}

	long := filepath.Join(dir, strings.Repeat("x", 250)+".tar")
	if err := writeFile(ctx, long, write("f")); err != nil {
		t.Fatalf("a long file name must work: %v", err)
	}
}

func TestErrorMessages(t *testing.T) {
	tests := []struct {
		argv []string
		want string
	}{
		{[]string{"help", "nosuch"}, "no help topic for 'nosuch' (see 'droot --help')"},
		{[]string{"help", "rnu"}, "did you mean 'run'?"},
		{[]string{"rnu"}, "'rnu' is not a droot command; did you mean 'run'?"},
		{[]string{"exprot", "img"}, "did you mean 'export'?"},
		{[]string{"run", "--rot", "/x", "--", "true"}, "unknown flag --rot; did you mean --root?"},
		{[]string{"run", "-rot=/x", "--", "true"}, "unknown flag -rot; did you mean --root?"},
		{[]string{"run", "--bogus", "--", "true"}, "unknown flag --bogus (see 'droot run --help')"},
		{[]string{"umount", "--rooot", "/x"}, "did you mean --root?"},
		{[]string{"run", "---root", "/x", "true"}, "unknown flag ---root; did you mean --root?"},
		{[]string{"run", "-r"}, "flag -r needs an argument (see 'droot run --help')"},
		{[]string{"export", "-o"}, "flag -o needs an argument (see 'droot export --help')"},
		{[]string{"run", "--cp=maybe", "-r", "/x", "true"}, `invalid value "maybe" for flag --cp`},
		{[]string{"--root", "/x", "run"}, "unknown flag --root (see 'droot --help')"},
		{[]string{"completion"}, "exactly one shell is required"},
		{[]string{"completion", "tcsh"}, `unsupported shell "tcsh"`},
	}
	for _, tt := range tests {
		app := NewApp("test")
		app.Stdout = io.Discard
		err := app.Run(context.Background(), append([]string{"droot"}, tt.argv...))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("droot %q: got %v, want %q", tt.argv, err, tt.want)
		}
	}

	// Known help topics still work.
	var out strings.Builder
	app := NewApp("test")
	app.Stdout = &out
	if err := app.Run(context.Background(), []string{"droot", "help", "export"}); err != nil || !strings.Contains(out.String(), "droot export") {
		t.Errorf("help export: %v, %q", err, out.String())
	}
}

func TestDebugFromInvalidEnv(t *testing.T) {
	t.Setenv("DROOT_DEBUG", "yes please")
	if _, err := parseRun(t, "run", "-r", "/c", "true"); err == nil || !strings.Contains(err.Error(), "DROOT_DEBUG") {
		t.Fatalf("want an error about DROOT_DEBUG, got %v", err)
	}
	// The flag takes precedence over the environment.
	got, err := parseRun(t, "run", "--debug=false", "-r", "/c", "true")
	if err != nil || got.D {
		t.Fatalf("--debug=false must win over DROOT_DEBUG: %+v, %v", got, err)
	}
}

func TestVersion(t *testing.T) {
	for _, argv := range [][]string{{"--version"}, {"-v"}} {
		var out strings.Builder
		app := NewApp("1.2.3")
		app.Stdout = &out
		if err := app.Run(context.Background(), append([]string{"droot"}, argv...)); err != nil || out.String() != "droot version 1.2.3\n" {
			t.Errorf("droot %q: %v, %q", argv, err, out.String())
		}
	}
}

func TestBoundingSet(t *testing.T) {
	names := func(m map[uintptr]bool) []string {
		var out []string
		for c := range m {
			out = append(out, osutil.CapabilityName(c))
		}
		slices.Sort(out)
		return out
	}
	tests := []struct {
		add, drop []string
		want      []string
		wantErr   bool
	}{
		{want: []string{"CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_FOWNER", "CAP_NET_BIND_SERVICE", "CAP_SETGID", "CAP_SETUID"}},
		{add: []string{"net_raw"}, drop: []string{"CAP_CHOWN", "dac_override"}, want: []string{"CAP_FOWNER", "CAP_NET_BIND_SERVICE", "CAP_NET_RAW", "CAP_SETGID", "CAP_SETUID"}},
		{add: []string{"NET_BIND_SERVICE"}, drop: []string{"ALL"}, want: []string{"CAP_NET_BIND_SERVICE"}},
		{drop: []string{"all"}, want: nil},
		{add: []string{"SYS_PTRACE", "SYS_PTRACE"}, drop: []string{"SYS_PTRACE"}, want: []string{"CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_FOWNER", "CAP_NET_BIND_SERVICE", "CAP_SETGID", "CAP_SETUID"}},
		{add: []string{"NOPE"}, wantErr: true},
		{drop: []string{""}, wantErr: true},
	}
	for _, tt := range tests {
		got, err := boundingSet(tt.add, tt.drop)
		if tt.wantErr {
			if err == nil {
				t.Errorf("add %q drop %q: want an error", tt.add, tt.drop)
			}
			continue
		}
		if err != nil || !slices.Equal(names(got), tt.want) {
			t.Errorf("add %q drop %q: got %q, %v; want %q", tt.add, tt.drop, names(got), err, tt.want)
		}
	}
	all, err := boundingSet([]string{"ALL"}, []string{"SYS_ADMIN"})
	if err != nil || len(all) != len(osutil.AllCapabilities())-1 || all[21] {
		t.Errorf("--cap-add ALL --cap-drop SYS_ADMIN: %d capabilities, %v", len(all), err)
	}
	if len(keepCaps) != 6 {
		t.Error("boundingSet must not modify keepCaps")
	}
}

func TestPrintable(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/bin/app --port 80": "/usr/bin/app --port 80",
		"日本語 ok":                 "日本語 ok",
		"\x1b]0;pwned\x07":       `\x1b]0;pwned\x07`,
		"a\tb\nc":                `a\x09b\x0ac`,
		"bad\xff":                `bad\xff`,
		" ":                      `\xa0`,
	} {
		if got := printable(in); got != want {
			t.Errorf("printable(%q) = %q, want %q", in, got, want)
		}
	}
	// A right-to-left override, which would reorder what follows.
	if got, want := printable(string(rune(0x202e))+"evil"), "\\u202eevil"; got != want {
		t.Errorf("printable(RLO) = %q, want %q", got, want)
	}
}

func TestRunConfigFile(t *testing.T) {
	dir := t.TempDir()
	write := func(content string) string {
		p := filepath.Join(dir, "app.toml")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	var got parsedRun
	run := func(argv ...string) error {
		app := NewApp("test")
		app.Stdout = io.Discard
		app.Command("run").Action = func(_ context.Context, cmd *cli.Invocation) error {
			args := cmd.Args()
			command, err := applyConfigFile(cmd, cmd.String("config"))
			if err != nil {
				return err
			}
			if len(args) == 0 {
				args = command
			}
			got = parsedRun{
				args: args, root: cmd.String("root"), user: cmd.String("user"), bind: cmd.Strings("bind"),
				env: cmd.Strings("env"), copyFiles: cmd.Bool("copy-files"), noDrop: cmd.Bool("no-dropcaps"),
			}
			return nil
		}
		return app.Run(context.Background(), append([]string{"droot", "run"}, argv...))
	}
	conf := write(`root = "/var/containers/app"
user = "app"
copy-files = true
bind = ["/var/log/app"]
env = ["A=1"]
command = ["/app/server", "--port=80"]
`)
	if err := run("--config", conf); err != nil {
		t.Fatal(err)
	}
	want := parsedRun{args: []string{"/app/server", "--port=80"}, root: "/var/containers/app", user: "app", bind: []string{"/var/log/app"}, env: []string{"A=1"}, copyFiles: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if err := run("--config", conf, "-u", "root", "-e", "B=2", "--cp=false", "--", "sh"); err != nil {
		t.Fatal(err)
	}
	want = parsedRun{args: []string{"sh"}, root: "/var/containers/app", user: "root", bind: []string{"/var/log/app"}, env: []string{"A=1", "B=2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the command line must take precedence:\ngot  %+v\nwant %+v", got, want)
	}
	for content, wantErr := range map[string]string{
		"rot = '/x'":         `unknown option "rot"; did you mean "root"?`,
		"command = []":       "command must be a non-empty array",
		"command = 'sh'":     "command must be a non-empty array",
		"config = 'x.toml'":  "cannot include another one",
		"unshare = 'yes'":    "unshare must be true or false",
		"debug = true":       `unknown option "debug"`,
		"[run]\nroot = '/x'": "line 1, column 1: tables are not supported",
	} {
		if err := run("--config", write(content)); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("%q: got %v, want %q", content, err, wantErr)
		}
	}
	if err := run("--config", filepath.Join(dir, "missing.toml")); err == nil {
		t.Error("a missing file must be an error")
	}
}

// TestExampleConfig keeps examples/droot/app.toml valid.
func TestExampleConfig(t *testing.T) {
	app := NewApp("test")
	app.Stdout = io.Discard
	var command []string
	var root string
	app.Command("run").Action = func(_ context.Context, cmd *cli.Invocation) error {
		var err error
		command, err = applyConfigFile(cmd, cmd.String("config"))
		root = cmd.String("root")
		return err
	}
	if err := app.Run(context.Background(), []string{"droot", "run", "--config", filepath.Join("..", "..", "examples", "droot", "app.toml")}); err != nil {
		t.Fatal(err)
	}
	if root == "" || len(command) == 0 {
		t.Fatalf("root %q, command %q", root, command)
	}
}
