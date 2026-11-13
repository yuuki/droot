package systemd

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The values of the unit files of `droot unit`.
//
// systemd parses a unit file as root, and droot writes into it values that
// the author of the image chose: its environment, its command, its user.
// Every value is therefore checked and escaped, so that it can neither end
// its line (and add a setting, such as ExecStartPre=+... which systemd runs
// on the host with full privileges) nor change its own meaning
// (systemd.syntax(7)):
//
//   - Control characters are refused: a newline ends the setting, and
//     systemd rejects the others in environment variables.
//   - Words are double-quoted, with backslashes and double quotes escaped
//     by a backslash: settings that take lists (ExecStart=, Environment=,
//     BindPaths=, ...) unquote their words, and C-unescape them.
//   - "%" starts a specifier (%h, %n, ...) in most settings: it is doubled.
//   - "$" starts a variable in ExecStart=: it is doubled there.
//   - Paths written as they are, unquoted (RootDirectory=,
//     WorkingDirectory=), cannot contain quotes or backslashes, which some
//     settings would unquote: droot refuses them in every path, as well as
//     a trailing space, which systemd strips from the end of a line.

// UnitValueError is the error of a value that cannot be written to a unit
// file.
type UnitValueError struct {
	What   string // what the value is, e.g. "the environment variable FOO"
	Value  string
	Reason string
}

func (e *UnitValueError) Error() string {
	return fmt.Sprintf("%s (%q) cannot be written to a unit file: %s", e.What, e.Value, e.Reason)
}

// checkText checks that s can be written to a unit file: valid UTF-8,
// without control characters.
func checkText(what, s string) error {
	if !utf8.ValidString(s) {
		return &UnitValueError{What: what, Value: s, Reason: "it is not valid UTF-8"}
	}
	if i := strings.IndexFunc(s, unicode.IsControl); i >= 0 {
		return &UnitValueError{What: what, Value: s, Reason: fmt.Sprintf("it contains the control character %U", []rune(s[i:])[0])}
	}
	return nil
}

// quote returns s double-quoted, with backslashes and double quotes escaped,
// "%" doubled, and "$" doubled if dollar is set.
func quote(s string, dollar bool) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch c {
		case '\\', '"':
			b.WriteByte('\\')
		case '%':
			b.WriteByte('%')
		case '$':
			if dollar {
				b.WriteByte('$')
			}
		}
		b.WriteRune(c)
	}
	b.WriteByte('"')
	return b.String()
}

// Word returns s quoted, for the settings that take lists of words
// (Environment=, TemporaryFileSystem=, ...). what describes s in errors.
func Word(what, s string) (string, error) {
	if err := checkText(what, s); err != nil {
		return "", err
	}
	return quote(s, false), nil
}

// ExecArg returns the argument s of a command line (ExecStart=), quoted.
// what describes s in errors.
func ExecArg(what, s string) (string, error) {
	if err := checkText(what, s); err != nil {
		return "", err
	}
	return quote(s, true), nil
}

// EnvAssignment returns NAME=VALUE quoted for Environment=. systemd ignores
// an assignment whose name is not made of letters, digits and underscores,
// or starts with a digit: such a name is an error.
func EnvAssignment(name, value string) (string, error) {
	what := "the environment variable " + name
	if !validEnvName(name) {
		return "", &UnitValueError{What: "the name of an environment variable", Value: name,
			Reason: "systemd only accepts letters, digits and underscores, not starting with a digit"}
	}
	if err := checkText(what, value); err != nil {
		return "", err
	}
	return quote(name+"="+value, false), nil
}

func validEnvName(name string) bool {
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		return false
	}
	for _, c := range []byte(name) {
		letter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !letter && c != '_' && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// CheckPath checks a path to write to a unit file: absolute, clean, without
// control characters, quotes, backslashes or trailing white space.
func CheckPath(what, p string) error {
	if err := checkText(what, p); err != nil {
		return err
	}
	switch {
	case !filepath.IsAbs(p) || filepath.Clean(p) != p:
		return &UnitValueError{What: what, Value: p, Reason: "it is not an absolute path without . or .. components"}
	case strings.ContainsAny(p, "\"'\\"):
		return &UnitValueError{What: what, Value: p, Reason: "droot does not write quotes or backslashes in the paths of a unit"}
	case strings.TrimRightFunc(p, unicode.IsSpace) != p:
		return &UnitValueError{What: what, Value: p, Reason: "it ends with white space, which systemd strips"}
	}
	return nil
}

// PathValue returns the path p for a setting that takes one path as it is
// (RootDirectory=, WorkingDirectory=), with "%" doubled.
func PathValue(what, p string) (string, error) {
	if err := CheckPath(what, p); err != nil {
		return "", err
	}
	return strings.ReplaceAll(p, "%", "%%"), nil
}

// PathWord returns the path p quoted, for the settings that take lists of
// paths (BindPaths=, TemporaryFileSystem=, ...).
func PathWord(what, p string) (string, error) {
	if err := CheckPath(what, p); err != nil {
		return "", err
	}
	return quote(p, false), nil
}
