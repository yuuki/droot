package imageconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"

	"github.com/yuuki/droot/internal/osutil"
)

func TestEncodeParse(t *testing.T) {
	c := &Config{
		Version: Version, Image: "app:1", ImageID: "sha256:x", Env: []string{"A=1", "CERT=a\nb", "#K=v"},
		WorkingDir: "/app", User: "app:staff", Entrypoint: []string{"/entry"}, Cmd: []string{"run"}, StopSignal: "SIGQUIT",
	}
	b, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(b, "config.json")
	if err != nil || !reflect.DeepEqual(got, c) {
		t.Fatalf("round trip: %+v, %v", got, err)
	}
	for _, bad := range []string{"", "{", "{}", `{"Version": 0}`, `{"Version": "1"}`} {
		if _, err := Parse([]byte(bad), "config.json"); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
	// Unknown fields of newer versions are ignored.
	got, err = Parse([]byte(`{"Version": 2, "Cmd": ["x"], "Future": true}`), "config.json")
	if err != nil || !slices.Equal(got.Cmd, []string{"x"}) {
		t.Fatalf("newer version: %+v, %v", got, err)
	}
}

func TestCommand(t *testing.T) {
	var none *Config
	tests := []struct {
		cfg  *Config
		args []string
		want []string
	}{
		{none, nil, nil},
		{none, []string{"ls"}, []string{"ls"}},
		{&Config{Entrypoint: []string{"/e", "-x"}, Cmd: []string{"c"}}, nil, []string{"/e", "-x", "c"}},
		{&Config{Cmd: []string{"c", "d"}}, nil, []string{"c", "d"}},
		{&Config{Entrypoint: []string{"/e"}}, nil, []string{"/e"}},
		// COMMAND replaces the entrypoint too, unlike docker run.
		{&Config{Entrypoint: []string{"/e"}, Cmd: []string{"c"}}, []string{"sh"}, []string{"sh"}},
		{&Config{}, nil, []string{}},
	}
	for _, tt := range tests {
		if got := tt.cfg.Command(tt.args); !slices.Equal(got, tt.want) {
			t.Errorf("%+v.Command(%q) = %q, want %q", tt.cfg, tt.args, got, tt.want)
		}
	}
	c := &Config{Entrypoint: []string{"/e"}, Cmd: []string{"c"}}
	got := c.Command(nil)
	got[0] = "changed"
	if c.Entrypoint[0] != "/e" {
		t.Fatal("Command must not alias the configuration")
	}
}

func TestRead(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("osutil.Root is only implemented on Linux")
	}
	dir := t.TempDir()
	root, err := osutil.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if c, err := Read(root); c != nil || err != nil {
		t.Fatalf("a missing file is not an error: %+v, %v", c, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".droot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".droot", "config.json"), []byte(`{"Version": 1, "User": "app"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if c, err := Read(root); err != nil || c.User != "app" {
		t.Fatalf("Read: %+v, %v", c, err)
	}
	// A symlink to the host's file system is resolved inside the root.
	outside := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(outside, []byte(`{"Version": 1, "User": "evil"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, ".droot", "config.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, ".droot", "config.json")); err != nil {
		t.Fatal(err)
	}
	if c, err := Read(root); c != nil || err != nil {
		t.Fatalf("an absolute symlink must be resolved inside the root (missing there): %+v, %v", c, err)
	}
}
