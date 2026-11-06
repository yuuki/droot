// Package conffile parses the configuration files of `droot run --config`.
//
// The format is a strict subset of TOML (https://toml.io), enough to write
// down command line options:
//
//	# A comment.
//	root = "/var/containers/app/current"   # basic string, with \ escapes
//	user = 'app'                           # literal string
//	unshare = true                         # boolean
//	bind = [                               # array of strings
//	    "/var/log/app",
//	    "/srv/data:/data:ro",
//	]
//
// Keys are bare (letters, digits, '-', '_') or quoted. Everything else in
// TOML — tables, dotted keys, numbers, dates, multi-line strings, inline
// tables, arrays of other types — is refused with an error, so that a file
// is never silently read differently than TOML would read it.
package conffile

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Parse parses a configuration file. The values are bool, string or
// []string.
func Parse(data []byte) (map[string]any, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("the file is not valid UTF-8")
	}
	p := &parser{s: strings.ReplaceAll(string(data), "\r\n", "\n"), line: 1, col: 1}
	values := map[string]any{}
	for {
		p.skipSpaceAndComments(true)
		if p.eof() {
			return values, nil
		}
		if p.peek() == '[' {
			return nil, p.errorf("tables are not supported")
		}
		key, err := p.key()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.peek() == '.' {
			return nil, p.errorf("dotted keys are not supported")
		}
		if !p.consume('=') {
			return nil, p.errorf("expected '=' after the key %q", key)
		}
		p.skipSpace()
		if _, dup := values[key]; dup {
			return nil, p.errorf("the key %q is defined twice", key)
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		values[key] = v
		p.skipSpace()
		if p.peek() == '#' {
			p.skipComment()
		}
		if !p.eof() && !p.consume('\n') {
			return nil, p.errorf("expected the end of the line after the value of %q", key)
		}
	}
}

type parser struct {
	s         string
	pos       int
	line, col int
}

// Error is a syntax error, with its position.
type Error struct {
	Line, Col int
	Msg       string
}

func (e *Error) Error() string { return fmt.Sprintf("line %d, column %d: %s", e.Line, e.Col, e.Msg) }

func (p *parser) errorf(format string, a ...any) error {
	return &Error{Line: p.line, Col: p.col, Msg: fmt.Sprintf(format, a...)}
}

func (p *parser) eof() bool { return p.pos >= len(p.s) }

func (p *parser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.s[p.pos]
}

func (p *parser) next() rune {
	r, size := utf8.DecodeRuneInString(p.s[p.pos:])
	p.pos += size
	if r == '\n' {
		p.line++
		p.col = 1
	} else {
		p.col++
	}
	return r
}

func (p *parser) consume(c byte) bool {
	if p.peek() == c {
		p.next()
		return true
	}
	return false
}

func (p *parser) skipSpace() {
	for p.peek() == ' ' || p.peek() == '\t' {
		p.next()
	}
}

func (p *parser) skipComment() {
	for !p.eof() && p.peek() != '\n' {
		p.next()
	}
}

// skipSpaceAndComments skips white space and comments, and newlines too if
// newlines is set.
func (p *parser) skipSpaceAndComments(newlines bool) {
	for !p.eof() {
		switch c := p.peek(); {
		case c == ' ' || c == '\t' || (newlines && c == '\n'):
			p.next()
		case c == '#':
			p.skipComment()
		default:
			return
		}
	}
}

func isBareKeyChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

func (p *parser) key() (string, error) {
	switch p.peek() {
	case '"', '\'':
		k, err := p.str()
		if err != nil {
			return "", err
		}
		return k, nil
	}
	start := p.pos
	for !p.eof() && isBareKeyChar(p.peek()) {
		p.next()
	}
	if p.pos == start {
		return "", p.errorf("expected a key")
	}
	return p.s[start:p.pos], nil
}

func (p *parser) value() (any, error) {
	switch c := p.peek(); {
	case c == '"' || c == '\'':
		return p.str()
	case c == '[':
		return p.array()
	case strings.HasPrefix(p.s[p.pos:], "true") && !p.bareCharAt(p.pos+4):
		for range 4 {
			p.next()
		}
		return true, nil
	case strings.HasPrefix(p.s[p.pos:], "false") && !p.bareCharAt(p.pos+5):
		for range 5 {
			p.next()
		}
		return false, nil
	case c == '{':
		return nil, p.errorf("inline tables are not supported")
	case c == '\n' || c == 0:
		return nil, p.errorf("expected a value")
	}
	return nil, p.errorf("unsupported value: only strings, booleans and arrays of strings are supported")
}

func (p *parser) bareCharAt(i int) bool { return i < len(p.s) && isBareKeyChar(p.s[i]) }

func (p *parser) array() ([]string, error) {
	p.next() // [
	out := []string{}
	for {
		p.skipSpaceAndComments(true)
		if p.consume(']') {
			return out, nil
		}
		if c := p.peek(); c != '"' && c != '\'' {
			if p.eof() {
				return nil, p.errorf("unterminated array")
			}
			return nil, p.errorf("only arrays of strings are supported")
		}
		s, err := p.str()
		if err != nil {
			return nil, err
		}
		out = append(out, s)
		p.skipSpaceAndComments(true)
		if p.eof() {
			return nil, p.errorf("unterminated array")
		}
		if p.consume(',') {
			continue
		}
		if p.consume(']') {
			return out, nil
		}
		return nil, p.errorf("expected ',' or ']' in the array")
	}
}

// str parses a basic ("...") or literal ('...') single-line string.
func (p *parser) str() (string, error) {
	quote := p.peek()
	if strings.HasPrefix(p.s[p.pos:], strings.Repeat(string(quote), 3)) {
		return "", p.errorf("multi-line strings are not supported")
	}
	p.next()
	var b strings.Builder
	for {
		if p.eof() || p.peek() == '\n' {
			return "", p.errorf("unterminated string")
		}
		r := p.next()
		switch {
		case r == rune(quote):
			return b.String(), nil
		case r == '\\' && quote == '"':
			e, err := p.escape()
			if err != nil {
				return "", err
			}
			b.WriteRune(e)
		case r < 0x20 && r != '\t' || r == 0x7f:
			return "", p.errorf("control characters must be escaped in strings")
		default:
			b.WriteRune(r)
		}
	}
}

func (p *parser) escape() (rune, error) {
	if p.eof() {
		return 0, p.errorf("unterminated string")
	}
	switch c := p.next(); c {
	case 'b':
		return '\b', nil
	case 't':
		return '\t', nil
	case 'n':
		return '\n', nil
	case 'f':
		return '\f', nil
	case 'r':
		return '\r', nil
	case 'e':
		return 0x1b, nil
	case '"', '\\':
		return c, nil
	case 'u', 'U':
		n := 4
		if c == 'U' {
			n = 8
		}
		if p.pos+n > len(p.s) {
			return 0, p.errorf("invalid unicode escape")
		}
		v, err := strconv.ParseUint(p.s[p.pos:p.pos+n], 16, 32)
		r := rune(v)
		if err != nil || !utf8.ValidRune(r) {
			return 0, p.errorf("invalid unicode escape")
		}
		for range n {
			p.next()
		}
		return r, nil
	}
	return 0, p.errorf("invalid escape sequence")
}
