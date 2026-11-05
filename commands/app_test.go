package commands

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/yuuki/droot/log"
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
	app.Writer, app.ErrWriter = io.Discard, io.Discard
	for _, c := range app.Commands {
		if c.Name == "run" {
			c.Action = func(_ context.Context, cmd *cli.Command) error {
				got = parsedRun{
					args: cmd.Args().Slice(), root: cmd.String("root"), user: cmd.String("user"), group: cmd.String("group"),
					bind: cmd.StringSlice("bind"), robind: cmd.StringSlice("robind"), env: cmd.StringSlice("env"),
					copyFiles: cmd.Bool("copy-files"), noDrop: cmd.Bool("no-dropcaps"), D: cmd.Bool("debug"),
				}
				return nil
			}
		}
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
	} {
		app := NewApp("test")
		app.Writer, app.ErrWriter = io.Discard, io.Discard
		err := app.Run(context.Background(), append([]string{"droot"}, argv...))
		if err == nil || !strings.Contains(err.Error(), "--help") {
			t.Errorf("droot %q: want usage error, got %v", argv, err)
		}
	}
}

func TestUmountRefusesRoot(t *testing.T) {
	app := NewApp("test")
	app.Writer, app.ErrWriter = io.Discard, io.Discard
	err := app.Run(context.Background(), []string{"droot", "umount", "--root", "/"})
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("umount --root / must be refused, got %v", err)
	}
}

func TestParseBindOption(t *testing.T) {
	tests := []struct {
		spec    string
		want    bindMount
		wantErr bool
	}{
		{spec: "/var/log/app", want: bindMount{host: "/var/log/app", container: "/var/log/app"}},
		{spec: "/var/log/app/", want: bindMount{host: "/var/log/app", container: "/var/log/app"}},
		{spec: "/srv/data:/data", want: bindMount{host: "/srv/data", container: "/data"}},
		{spec: "/srv/data:", want: bindMount{host: "/srv/data", container: "/srv/data"}},
		{spec: "/a:/b:c", want: bindMount{host: "/a", container: "/b:c"}},
		{spec: "relative", wantErr: true},
		{spec: "/abs:relative", wantErr: true},
		{spec: "", wantErr: true},
	}
	for _, tt := range tests {
		got, err := parseBindOption(tt.spec)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseBindOption(%q) should fail", tt.spec)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("parseBindOption(%q) = %+v, %v; want %+v", tt.spec, got, err, tt.want)
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
