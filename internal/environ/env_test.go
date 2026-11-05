package environ

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReadEnvFile(t *testing.T) {
	env, err := ReadEnvFile("testdata/drootenv")
	if err != nil {
		t.Fatalf("should not be error: %v", err)
	}
	want := []string{
		"HOME=/root",
		"GOLANG_DOWNLOAD_SHA256=5470eac05d273c74ff8bac7bef5bad0b5abbd1c4052efbdbc8db45332e836b0b",
		"PATH=/go/bin:/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"GOPATH=/go",
		"PWD=/go",
		"GOLANG_DOWNLOAD_URL=https://golang.org/dl/go1.6.linux-amd64.tar.gz",
		"GOLANG_VERSION=1.6",
	}
	if !slices.Equal(env, want) {
		t.Fatalf("got %q, want %q", env, want)
	}
}

func TestReadEnvFileNotFound(t *testing.T) {
	if _, err := ReadEnvFile(filepath.Join(t.TempDir(), "missing")); !os.IsNotExist(err) {
		t.Fatalf("want not-exist error, got %v", err)
	}
}

func TestParseEnv(t *testing.T) {
	input := strings.Join([]string{
		"",
		"# comment",
		"JAVA_OPTS=-Dfoo=bar -Dbaz=qux", // value containing '='
		"EMPTY=",
		"  INDENTED=1",
		"CRLF=yes\r",
		"no_equal_sign",
		"=no_key",
		"TRAILING=space ",
	}, "\n")
	got, err := ParseEnv(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"JAVA_OPTS=-Dfoo=bar -Dbaz=qux",
		"EMPTY=",
		"INDENTED=1",
		"CRLF=yes",
		"TRAILING=space ",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseEnvLongLine(t *testing.T) {
	long := "LONG=" + strings.Repeat("x", 200*1024)
	got, err := ParseEnv(strings.NewReader(long + "\nNEXT=1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != long {
		t.Fatalf("long line was not preserved")
	}
}

func TestValidate(t *testing.T) {
	for _, ok := range [][]string{nil, {"A=1"}, {"A="}, {"A=b=c"}} {
		if err := Validate(ok); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", ok, err)
		}
	}
	for _, ng := range [][]string{{"A"}, {"=1"}, {"A=1", "B"}} {
		if err := Validate(ng); err == nil {
			t.Errorf("Validate(%q) = nil, want error", ng)
		}
	}
}

func TestMerge(t *testing.T) {
	tests := []struct {
		name      string
		base, ovr []string
		want      []string
	}{
		{
			name: "disjoint",
			base: []string{"EDITOR=vim", "LANG=ja_JP.UTF-8", "USER=yuuki"},
			ovr:  []string{"PAGER=less", "PATH=/bin:/usr/bin"},
			want: []string{"EDITOR=vim", "LANG=ja_JP.UTF-8", "USER=yuuki", "PAGER=less", "PATH=/bin:/usr/bin"},
		},
		{
			name: "override keeps position",
			base: []string{"LANG=ja_JP.UTF-8", "EDITOR=vim", "USER=yuuki"},
			ovr:  []string{"EDITOR=emacs", "PATH=/bin:/usr/bin"},
			want: []string{"LANG=ja_JP.UTF-8", "EDITOR=emacs", "USER=yuuki", "PATH=/bin:/usr/bin"},
		},
		{
			name: "duplicates collapse and last wins",
			base: []string{"A=1", "A=2"},
			ovr:  []string{"B=1", "B=2", "A=3"},
			want: []string{"A=3", "B=2"},
		},
		{
			name: "values with equal signs",
			base: []string{"OPTS=a=b"},
			ovr:  []string{"OPTS=c=d"},
			want: []string{"OPTS=c=d"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := slices.Clone(tt.base)
			got, err := Merge(base, tt.ovr)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			if !slices.Equal(base, tt.base) {
				t.Fatalf("Merge modified its input: %q", base)
			}
		})
	}

	if _, err := Merge([]string{"A=1"}, []string{"BROKEN"}); err == nil {
		t.Fatal("want error for invalid override")
	}
}

func TestLookup(t *testing.T) {
	env := []string{"PATH=/bin", "A=1", "PATH=/usr/bin"}
	if v, ok := Lookup(env, "PATH"); !ok || v != "/usr/bin" {
		t.Fatalf("Lookup(PATH) = %q, %v", v, ok)
	}
	if _, ok := Lookup(env, "MISSING"); ok {
		t.Fatal("Lookup(MISSING) should not be found")
	}
}

func TestParseEnvFile(t *testing.T) {
	lookup := func(k string) (string, bool) {
		v, ok := map[string]string{"FROM_HOST": "h=1", "EMPTY": ""}[k]
		return v, ok
	}
	in := "# comment\nA=1\n  FROM_HOST  \nMISSING\nEMPTY\nB= spaced \n=novalue\n\n"
	got, err := ParseEnvFile(strings.NewReader(in), lookup)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"A=1", "FROM_HOST=h=1", "EMPTY=", "B= spaced "}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	// /.drootenv keeps ignoring bare keys.
	got, err = ParseEnv(strings.NewReader(in))
	if err != nil || !slices.Equal(got, []string{"A=1", "B= spaced "}) {
		t.Fatalf("ParseEnv: %q, %v", got, err)
	}
}

func TestExpandBare(t *testing.T) {
	lookup := func(k string) (string, bool) {
		if k == "SET" {
			return "v", true
		}
		return "", false
	}
	got, err := ExpandBare([]string{"A=1", "SET", "UNSET", "B="}, lookup)
	if err != nil || !slices.Equal(got, []string{"A=1", "SET=v", "B="}) {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, bad := range []string{"", "=x"} {
		if _, err := ExpandBare([]string{bad}, lookup); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}
