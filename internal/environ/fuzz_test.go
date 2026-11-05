package environ

import (
	"bufio"
	"errors"
	"slices"
	"strings"
	"testing"
)

// FuzzParseEnv checks that ParseEnv never panics and only returns
// well-formed KEY=VALUE entries, which parse back to themselves.
func FuzzParseEnv(f *testing.F) {
	for _, seed := range []string{
		"",
		"A=1\nB=2\n",
		"# comment\n\nJAVA_OPTS=-Dfoo=bar -Dbaz=qux\nEMPTY=\n",
		"  INDENTED=1\r\nCRLF=yes\r\r\n",
		"no_equal_sign\n=no_key\n  =x\n#HASH=1\n  # indented comment\n",
		"TRAILING=space \nKEY WITH SPACE=1\n\tTAB\t=v\n",
		"A=b=c\nA==\nA=\r\nA=last",
		"\u00a0NBSP=1\n\u0085NEL=2\nK\u00a0=3\n",
		"\x00=\x00\n\xff=\xfe\n",
		"A=1\rB=2\n\r\n\r\n=\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data string) {
		env, err := ParseEnv(strings.NewReader(data))
		if err != nil {
			if errors.Is(err, bufio.ErrTooLong) && longestLine(data) >= maxLineSize/2 {
				return
			}
			t.Fatalf("ParseEnv: %v", err)
		}
		if lines := strings.Count(data, "\n") + 1; len(env) > lines {
			t.Fatalf("%d entries from %d lines", len(env), lines)
		}
		for _, e := range env {
			key, value, ok := strings.Cut(e, "=")
			switch {
			case !ok || key == "":
				t.Fatalf("entry %q is not KEY=VALUE", e)
			case key != strings.TrimSpace(key):
				t.Fatalf("key of %q has surrounding spaces", e)
			case strings.HasPrefix(key, "#"):
				t.Fatalf("key of %q starts with '#'", e)
			case strings.Contains(e, "\n") || strings.HasSuffix(value, "\r"):
				t.Fatalf("entry %q has a line ending", e)
			}
		}
		if err := Validate(env); err != nil {
			t.Fatalf("Validate(ParseEnv(...)): %v", err)
		}

		// Written one per line, the entries parse back to themselves.
		again, err := ParseEnv(strings.NewReader(strings.Join(env, "\n")))
		if err != nil || !slices.Equal(again, env) {
			t.Fatalf("entries do not parse back to themselves:\n got  %q (%v)\n want %q", again, err, env)
		}

		// The last definition of a key wins.
		last := map[string]string{}
		for _, e := range env {
			k, v, _ := strings.Cut(e, "=")
			last[k] = v
		}
		for k, want := range last {
			if v, ok := Lookup(env, k); !ok || v != want {
				t.Fatalf("Lookup(%q) = %q, %v; want %q", k, v, ok, want)
			}
		}

		// Without bare keys found in the environment, an --env-file is
		// read like a /.drootenv file.
		notFound := func(string) (string, bool) { return "", false }
		if got, err := ParseEnvFile(strings.NewReader(data), notFound); err != nil || !slices.Equal(got, env) {
			t.Fatalf("ParseEnvFile without lookup = %q, %v; want %q", got, err, env)
		}
		found := func(k string) (string, bool) { return "from-host-" + k, true }
		withBare, err := ParseEnvFile(strings.NewReader(data), found)
		if err != nil || Validate(withBare) != nil || len(withBare) < len(env) {
			t.Fatalf("ParseEnvFile with lookup = %q, %v (ParseEnv: %q)", withBare, err, env)
		}

		// Validate and ExpandBare agree on what a valid item is.
		items := strings.Split(data, "\n")
		for _, item := range items {
			if err := Validate([]string{item}); (err == nil) != (strings.Index(item, "=") > 0) {
				t.Fatalf("Validate(%q) = %v", item, err)
			}
		}
		expanded, err := ExpandBare(items, found)
		if wantErr := slices.ContainsFunc(items, func(s string) bool { k, _, _ := strings.Cut(s, "="); return k == "" }); wantErr != (err != nil) {
			t.Fatalf("ExpandBare(%q) = %v", items, err)
		}
		if err == nil && (len(expanded) != len(items) || Validate(expanded) != nil) {
			t.Fatalf("ExpandBare(%q) = %q", items, expanded)
		}
	})
}

// FuzzEnvRoundTrip checks that representable variables, written one per
// line, are parsed back exactly and in order, and that Merge and Lookup
// agree with each other. base and overrides hold KEY\x00VALUE\x00... pairs.
func FuzzEnvRoundTrip(f *testing.F) {
	f.Add("PATH\x00/usr/bin:/bin\x00HOME\x00/root", "HOME\x00/home/app\x00NEW\x00x=y", uint8(0))
	f.Add("A\x001\x00B\x00\x00A\x002", "", uint8(1))
	f.Add("JAVA_OPTS\x00-Da=b -Dc=d\x00QUOTE\x00\"$HOME\" `x`", "QUOTE\x00 spaced \x00EMPTY\x00", uint8(7))
	f.Add("K\u00a0\x00not representable\x00#C\x00comment\x00 S\x00spaced key", "OK\x00\tvalue with tab\t", uint8(15))
	f.Add("A\x00x\ry\x00B\x00=\x00\xff\x00\xfe", "B\x00==\x00A\x00", uint8(2))
	f.Fuzz(func(t *testing.T, baseSpec, overridesSpec string, style uint8) {
		basePairs, overridePairs := envPairs(baseSpec), envPairs(overridesSpec)

		base := parseLines(t, basePairs, style)
		if want := joinPairs(basePairs); !slices.Equal(base, want) {
			t.Fatalf("round trip with style %#x:\n got  %q\n want %q", style, base, want)
		}
		overrides := parseLines(t, overridePairs, style)

		baseCopy, overridesCopy := slices.Clone(base), slices.Clone(overrides)
		merged, err := Merge(base, overrides)
		if err != nil {
			t.Fatalf("Merge(%q, %q): %v", base, overrides, err)
		}
		if !slices.Equal(base, baseCopy) || !slices.Equal(overrides, overridesCopy) {
			t.Fatal("Merge modified its input")
		}
		if err := Validate(merged); err != nil {
			t.Fatalf("Validate(Merge(...)): %v", err)
		}

		// Keys keep the position of their first occurrence and appear once,
		// with the value of their last definition.
		var order []string
		want := map[string]string{}
		for _, p := range append(slices.Clone(basePairs), overridePairs...) {
			if _, seen := want[p.key]; !seen {
				order = append(order, p.key)
			}
			want[p.key] = p.value
		}
		if len(merged) != len(order) {
			t.Fatalf("Merge(%q, %q) = %q: want %d keys", base, overrides, merged, len(order))
		}
		for i, e := range merged {
			if k, v, _ := strings.Cut(e, "="); k != order[i] || v != want[k] {
				t.Fatalf("Merge(%q, %q)[%d] = %q, want %q", base, overrides, i, e, order[i]+"="+want[order[i]])
			}
		}
		for k, v := range want {
			if got, ok := Lookup(merged, k); !ok || got != v {
				t.Fatalf("Lookup(Merge(...), %q) = %q, %v; want %q", k, got, ok, v)
			}
		}
	})
}

type envPair struct{ key, value string }

// envPairs decodes KEY\x00VALUE\x00... and keeps the pairs that a /.drootenv
// file can represent.
func envPairs(spec string) []envPair {
	fields := strings.Split(spec, "\x00")
	var pairs []envPair
	for i := 0; i+1 < len(fields); i += 2 {
		k, v := fields[i], fields[i+1]
		if k == "" || strings.ContainsAny(k, "=\r\n") || strings.HasPrefix(k, "#") ||
			strings.TrimSpace(k) != k || strings.ContainsAny(v, "\r\n") {
			continue
		}
		pairs = append(pairs, envPair{k, v})
	}
	return pairs
}

func joinPairs(pairs []envPair) []string {
	var env []string
	for _, p := range pairs {
		env = append(env, p.key+"="+p.value)
	}
	return env
}

// parseLines writes pairs as a file in a format chosen by the bits of style
// and parses it with ParseEnv.
func parseLines(t *testing.T, pairs []envPair, style uint8) []string {
	t.Helper()
	eol := "\n"
	if style&1 != 0 {
		eol = "\r\n"
	}
	var b strings.Builder
	for i, p := range pairs {
		if style&4 != 0 {
			b.WriteString("# comment " + p.key + "=" + p.value + eol + eol + " \t" + eol)
		}
		if style&2 != 0 {
			b.WriteString(" \t" + p.key + " \t=" + p.value)
		} else {
			b.WriteString(p.key + "=" + p.value)
		}
		if i < len(pairs)-1 || style&8 == 0 {
			b.WriteString(eol)
		}
	}
	env, err := ParseEnv(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("ParseEnv(%q): %v", b.String(), err)
	}
	return env
}

func longestLine(s string) int {
	n := 0
	for line := range strings.SplitSeq(s, "\n") {
		n = max(n, len(line))
	}
	return n
}
