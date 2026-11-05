package osutil

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestParseRlimit(t *testing.T) {
	tests := []struct {
		spec    string
		want    Rlimit
		wantErr bool
	}{
		{spec: "nofile=1024:4096", want: Rlimit{Name: "nofile", Soft: 1024, Hard: 4096}},
		{spec: "NPROC=100", want: Rlimit{Name: "nproc", Soft: 100, Hard: 100}},
		{spec: "core=0:unlimited", want: Rlimit{Name: "core", Soft: 0, Hard: RlimInfinity}},
		{spec: "memlock=-1", want: Rlimit{Name: "memlock", Soft: RlimInfinity, Hard: RlimInfinity}},
		{spec: "nofile=10:5", wantErr: true},
		{spec: "nofile", wantErr: true},
		{spec: "nofile=", wantErr: true},
		{spec: "=1", wantErr: true},
		{spec: "bogus=1", wantErr: true},
		{spec: "nofile=1k", wantErr: true},
		{spec: "nofile=1:2:3", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseRlimit(tt.spec)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseRlimit(%q) should fail, got %+v", tt.spec, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("ParseRlimit(%q) = %+v, %v; want %+v", tt.spec, got, err, tt.want)
		}
	}
}

// TestSetRlimitsSurvivesExec runs itself in a child process that sets the
// limits and execs a shell, which must see them (Go restores its own
// RLIMIT_NOFILE at exec unless told otherwise).
func TestSetRlimitsSurvivesExec(t *testing.T) {
	if os.Getenv("DROOT_TEST_RLIMIT_CHILD") == "1" {
		if err := SetRlimits([]Rlimit{{Name: "nofile", Soft: 123, Hard: 456}, {Name: "core", Soft: 0, Hard: 0}}); err != nil {
			os.Exit(2)
		}
		_ = Exec("/bin/sh", []string{"sh", "-c", "ulimit -Sn; ulimit -Hn; ulimit -c"}, nil)
		os.Exit(3)
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSetRlimitsSurvivesExec$")
	cmd.Env = append(os.Environ(), "DROOT_TEST_RLIMIT_CHILD=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got := strings.Fields(string(out)); strings.Join(got, " ") != "123 456 0" {
		t.Fatalf("limits seen after exec: %q, want 123 456 0", got)
	}
}
