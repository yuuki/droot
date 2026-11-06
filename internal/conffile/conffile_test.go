package conffile

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	in := `# droot run options
root = "/var/containers/app/current"   # trailing comment
user = 'app'
"quoted-key" = "a \"b\" \\ \t \u00e9 \U0001F600"
'literal' = 'C:\path\no-escape'
unshare = true
no-mask=false
bind = [
    "/var/log/app",   # comment in an array
    '/srv/data:/data:ro',
]
empty = []
one = ["x"]

tmpfs = ["/tmp", "/run"]
`
	got, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"root": "/var/containers/app/current", "user": "app", "quoted-key": "a \"b\" \\ \t é 😀",
		"literal": `C:\path\no-escape`, "unshare": true, "no-mask": false,
		"bind": []string{"/var/log/app", "/srv/data:/data:ro"}, "empty": []string{}, "one": []string{"x"},
		"tmpfs": []string{"/tmp", "/run"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
	if got, err := Parse([]byte("a = 'x'\r\nb = true")); err != nil || got["a"] != "x" || got["b"] != true {
		t.Fatalf("CRLF and no final newline: %v, %v", got, err)
	}
	if got, err := Parse(nil); err != nil || len(got) != 0 {
		t.Fatalf("empty file: %v, %v", got, err)
	}
}

func TestParseErrors(t *testing.T) {
	for in, want := range map[string]string{
		"[table]":                     "tables are not supported",
		"a.b = 1":                     "dotted keys",
		"a = 1":                       "unsupported value",
		"a = 1.5":                     "unsupported value",
		"a = 1979-05-27":              "unsupported value",
		"a = trueish":                 "unsupported value",
		`a = """x"""`:                 "multi-line strings",
		"a = '''x'''":                 "multi-line strings",
		"a = {b = 'c'}":               "inline tables",
		"a = [1, 2]":                  "only arrays of strings",
		"a = [[ 'x' ]]":               "only arrays of strings",
		"a = ['x' 'y']":               "expected ',' or ']'",
		"a = ['x'":                    "unterminated array",
		"a = 'x":                      "unterminated string",
		"a = \"x\ny\"":                "unterminated string",
		`a = "\q"`:                    "invalid escape",
		`a = "\uD800"`:                "invalid unicode escape",
		"a = 'x' b = 'y'":             "expected the end of the line",
		"a 'x'":                       "expected '='",
		"= 'x'":                       "expected a key",
		"a =":                         "expected a value",
		"a = 'x'\na = 'y'":            "defined twice",
		"a = \"\x01\"":                "control characters",
		"a = '\xff'":                  "not valid UTF-8",
		"\n\n  b = [\n 'x',\n  3 ]\n": "line 5, column 3",
	} {
		_, err := Parse([]byte(in))
		var perr *Error
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q) = %v, want an error containing %q", in, err, want)
		} else if want != "not valid UTF-8" && !errors.As(err, &perr) {
			t.Errorf("Parse(%q): %v has no position", in, err)
		}
	}
}

// encode writes values as a configuration file, for the round trip of the
// fuzz test.
func encode(values map[string]any) string {
	var b strings.Builder
	for k, v := range values {
		b.WriteString(quote(k) + " = ")
		switch v := v.(type) {
		case bool:
			b.WriteString(strconv.FormatBool(v))
		case string:
			b.WriteString(quote(v))
		case []string:
			b.WriteString("[")
			for _, s := range v {
				b.WriteString(quote(s) + ", ")
			}
			b.WriteString("]")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// quote writes a TOML basic string.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteString(`\` + string(r))
		case r < 0x20 || r == 0x7f:
			b.WriteString(`\u` + strconv.FormatInt(int64(r)+0x10000, 16)[1:])
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func FuzzParse(f *testing.F) {
	f.Add([]byte("root = '/x'\nbind = ['/a', \"/b\"]\nunshare = true\n"))
	f.Add([]byte("# c\n\"k\" = \"\\u00e9\\n\" # c\n"))
	f.Add([]byte("a = [\n# c\n'x',\n]\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		values, err := Parse(data)
		if err != nil {
			return
		}
		for k, v := range values {
			switch v.(type) {
			case bool, string, []string:
			default:
				t.Fatalf("key %q has a value of type %T", k, v)
			}
		}
		again, err := Parse([]byte(encode(values)))
		if err != nil || !reflect.DeepEqual(values, again) {
			t.Fatalf("round trip: %#v, %v; want %#v", again, err, values)
		}
	})
}
