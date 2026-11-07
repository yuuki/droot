package image

import (
	"maps"
	"strings"
	"testing"
)

// FuzzParseChallenge checks the parsing of the WWW-Authenticate header of a
// registry, which names the server droot sends the credentials to: any
// header is parsed, and parameters written as the distribution
// specification writes them are read back exactly. pairs holds
// KEY\x00VALUE\x00... pairs.
func FuzzParseChallenge(f *testing.F) {
	f.Add(`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/alpine:pull"`,
		"Bearer", "realm\x00https://auth.docker.io/token\x00service\x00registry.docker.io", uint8(0))
	f.Add(`Basic realm=Registry`, "Basic", "realm\x00Registry\x00error\x00a \"quoted\" \\ value", uint8(1))
	f.Add(`Bearer realm="unterminated\`, "Bearer", "Realm\x00a\x00REALM\x00b\x00k\x00", uint8(2))
	f.Add(` , = ="" ,,`, "x", "a-b_c\x00,=\"\x00", uint8(3))
	f.Fuzz(func(t *testing.T, header, scheme, pairs string, style uint8) {
		// Any header: no panic, and the keys are normalized.
		_, params := parseChallenge(header)
		for k := range params {
			if k != strings.ToLower(strings.TrimSpace(k)) {
				t.Fatalf("parseChallenge(%q): key %q is not normalized", header, k)
			}
		}

		if scheme == "" || strings.Trim(scheme, tokenChars) != "" {
			return
		}
		var b strings.Builder
		b.WriteString(scheme)
		want := map[string]string{}
		fields := strings.Split(pairs, "\x00")
		for i := 0; i+1 < len(fields); i += 2 {
			k, v := fields[i], fields[i+1]
			if k == "" || strings.Trim(k, tokenChars) != "" {
				continue
			}
			if b.Len() == len(scheme) {
				b.WriteByte(' ')
			} else {
				b.WriteString([]string{",", ", ", " ,\t"}[int(style)%3])
			}
			// A token value may be written without quotes.
			if style&4 != 0 && v != "" && strings.Trim(v, tokenChars) == "" {
				b.WriteString(k + "=" + v)
			} else {
				b.WriteString(k + `="` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`)
			}
			want[strings.ToLower(k)] = v // the last one wins
		}
		gotScheme, got := parseChallenge(b.String())
		if gotScheme != scheme || !maps.Equal(got, want) {
			t.Fatalf("parseChallenge(%q) = %q, %q; want %q, %q", b.String(), gotScheme, got, scheme, want)
		}
	})
}

const tokenChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.:/"

// FuzzParseReference checks that an accepted image reference is valid and
// that its canonical form, which droot prints and looks credentials up
// with, is a fixed point.
func FuzzParseReference(f *testing.F) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, s := range []string{
		"alpine", "alpine:3.22", "docker.io/alpine", "index.docker.io/library/a:1", "user/app",
		"ghcr.io/o/r/app:v1", "localhost/app", "localhost:5000/app:1", "127.0.0.1:5000/a-b_c.d/e__f",
		"app@" + digest, "app:1@" + digest, "localhost:5000", "[::1]:5000/app", "Alpine", "app:", "a/-b",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		r, err := parseReference(s)
		if err != nil {
			return
		}
		if r.host == "" || r.apiHost() == "" || !repoPattern.MatchString(r.repo) || len(r.repo) > 255 ||
			(r.tag == "" && r.digest == "") || (r.tag != "" && !tagPattern.MatchString(r.tag)) ||
			(r.digest != "" && validDigest(r.digest) != nil) {
			t.Fatalf("parseReference(%q) = %+v: invalid", s, r)
		}
		again, err := parseReference(r.String())
		if err != nil || again != r {
			t.Fatalf("parseReference(%q) = %+v, %v; want %+v (from %q)", r.String(), again, err, r, s)
		}
	})
}
