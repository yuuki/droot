package systemd

import (
	"errors"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// unquoteWord decodes a word written by quote as systemd reads it (checked
// with systemd 255): "%%" is a percent sign (specifiers), the word is
// unquoted with C escapes, and, in ExecStart= (dollar), "$$" is a dollar
// sign. It fails on anything quote never writes, such as a lone "%" or an
// unescaped quote, which systemd would read differently.
func unquoteWord(w string, dollar bool) (string, error) {
	if len(w) < 2 || w[0] != '"' || w[len(w)-1] != '"' {
		return "", errors.New("not a double-quoted word")
	}
	in := []rune(w[1 : len(w)-1])
	var b strings.Builder
	for i := 0; i < len(in); i++ {
		c := in[i]
		switch {
		case c == '\\':
			if i+1 == len(in) || (in[i+1] != '\\' && in[i+1] != '"') {
				return "", errors.New("unexpected escape")
			}
			i++
			b.WriteRune(in[i])
		case c == '"':
			return "", errors.New("unescaped quote")
		case c == '%' || (dollar && c == '$'):
			if i+1 == len(in) || in[i+1] != c {
				return "", errors.New("lone " + string(c))
			}
			i++
			b.WriteRune(c)
		case unicode.IsControl(c):
			return "", errors.New("control character")
		default:
			b.WriteRune(c)
		}
	}
	return b.String(), nil
}

func TestQuoting(t *testing.T) {
	for _, tt := range []struct {
		in, word, exec string
	}{
		{"", `""`, `""`},
		{"plain", `"plain"`, `"plain"`},
		{"a b", `"a b"`, `"a b"`},
		{`say "hi"`, `"say \"hi\""`, `"say \"hi\""`},
		{`C:\dir`, `"C:\\dir"`, `"C:\\dir"`},
		{"100%", `"100%%"`, `"100%%"`},
		{"%n %h", `"%%n %%h"`, `"%%n %%h"`},
		{"$HOME ${X}", `"$HOME ${X}"`, `"$$HOME $${X}"`},
		{";", `";"`, `";"`},
		{"日本語", `"日本語"`, `"日本語"`},
	} {
		if got, err := Word("w", tt.in); err != nil || got != tt.word {
			t.Errorf("Word(%q) = %q, %v; want %q", tt.in, got, err, tt.word)
		}
		if got, err := ExecArg("w", tt.in); err != nil || got != tt.exec {
			t.Errorf("ExecArg(%q) = %q, %v; want %q", tt.in, got, err, tt.exec)
		}
	}
	for _, bad := range []string{"a\nExecStartPre=+/bin/sh", "a\rb", "tab\there", "nul\x00", "del\x7f", "c1\u0085", "\xff"} {
		if got, err := Word("w", bad); err == nil {
			t.Errorf("Word(%q) = %q, want an error", bad, got)
		}
		if got, err := ExecArg("w", bad); err == nil {
			t.Errorf("ExecArg(%q) = %q, want an error", bad, got)
		}
	}
}

func TestEnvAssignment(t *testing.T) {
	for _, tt := range []struct{ name, value, want string }{
		{"PATH", "/usr/bin:/bin", `"PATH=/usr/bin:/bin"`},
		{"_X1", "", `"_X1="`},
		{"OPTS", `-Da="b c" 50% $HOME \n`, `"OPTS=-Da=\"b c\" 50%% $HOME \\n"`},
	} {
		if got, err := EnvAssignment(tt.name, tt.value); err != nil || got != tt.want {
			t.Errorf("EnvAssignment(%q, %q) = %q, %v; want %q", tt.name, tt.value, got, err, tt.want)
		}
	}
	for _, tt := range []struct{ name, value string }{
		{"1X", "v"}, {"my.var", "v"}, {"my-var", "v"}, {"", "v"}, {"A B", "v"}, {"X", "line1\nline2"}, {"X", "a\tb"},
	} {
		if got, err := EnvAssignment(tt.name, tt.value); err == nil {
			t.Errorf("EnvAssignment(%q, %q) = %q, want an error", tt.name, tt.value, got)
		}
	}
}

func TestPaths(t *testing.T) {
	for _, tt := range []struct{ in, value, word string }{
		{"/var/containers/app", "/var/containers/app", `"/var/containers/app"`},
		{"/srv/a b", "/srv/a b", `"/srv/a b"`},
		{"/srv/100%", "/srv/100%%", `"/srv/100%%"`},
		{"/", "/", `"/"`},
	} {
		if got, err := PathValue("p", tt.in); err != nil || got != tt.value {
			t.Errorf("PathValue(%q) = %q, %v; want %q", tt.in, got, err, tt.value)
		}
		if got, err := PathWord("p", tt.in); err != nil || got != tt.word {
			t.Errorf("PathWord(%q) = %q, %v; want %q", tt.in, got, err, tt.word)
		}
	}
	for _, bad := range []string{"relative", "/a/../b", "/a/", "/a//b", `/a"b`, "/a'b", `/a\b`, "/a\nb", "/trailing ", ""} {
		if got, err := PathValue("p", bad); err == nil {
			t.Errorf("PathValue(%q) = %q, want an error", bad, got)
		}
		if got, err := PathWord("p", bad); err == nil {
			t.Errorf("PathWord(%q) = %q, want an error", bad, got)
		}
	}
	var e *UnitValueError
	if _, err := PathValue("the root directory", "rel"); !errors.As(err, &e) || !strings.Contains(err.Error(), "the root directory") {
		t.Errorf("want a UnitValueError naming the value, got %v", err)
	}
}

// FuzzQuoting checks that every value is either refused or written as one
// line that systemd reads back as the value itself.
func FuzzQuoting(f *testing.F) {
	for _, s := range []string{"", "a b", `"\`, "%%n", "$$x", "\n", "日本", "\xff", "x\"\\%$y"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, dollar := range []bool{false, true} {
			write := Word
			if dollar {
				write = ExecArg
			}
			w, err := write("v", s)
			if err != nil {
				if utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0 {
					t.Fatalf("%q refused: %v", s, err)
				}
				continue
			}
			if strings.ContainsAny(w, "\n\r") {
				t.Fatalf("%q written over several lines: %q", s, w)
			}
			got, err := unquoteWord(w, dollar)
			if err != nil || got != s {
				t.Fatalf("%q written as %q, read back as %q, %v", s, w, got, err)
			}
		}
	})
}
