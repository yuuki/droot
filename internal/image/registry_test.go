package image

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRegistry serves a test image, with token or basic authentication.
type fakeRegistry struct {
	t    *testing.T
	ti   *testImage
	repo string
	tags map[string]string
	// auth is "bearer", "basic" or "" (anonymous).
	auth       string
	user, pass string

	mu       sync.Mutex
	failures map[string]int  // path → 500 answers to give first
	tamper   map[string]bool // digests served corrupted
	status   map[string]int  // path → status to answer
	tokens   []string        // Authorization headers of the token requests
	srv      *httptest.Server
}

const testToken = "secret-token"

func newFakeRegistry(t *testing.T, ti *testImage, auth string) *fakeRegistry {
	r := &fakeRegistry{
		t: t, ti: ti, repo: "test/app", tags: map[string]string{"v1": ti.root.Digest}, auth: auth,
		user: "alice", pass: "pw", failures: map[string]int{}, tamper: map[string]bool{}, status: map[string]int{},
	}
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *fakeRegistry) host() string { return strings.TrimPrefix(r.srv.URL, "http://") }

func (r *fakeRegistry) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if code := r.status[req.URL.Path]; code != 0 {
		w.WriteHeader(code)
		fmt.Fprint(w, `{"errors":[{"code":"TOOMANYREQUESTS","message":"slow down"}]}`)
		return
	}
	if n := r.failures[req.URL.Path]; n > 0 {
		r.failures[req.URL.Path] = n - 1
		http.Error(w, "oops", http.StatusInternalServerError)
		return
	}
	if req.URL.Path == "/token" {
		r.tokens = append(r.tokens, req.Header.Get("Authorization"))
		if req.URL.Query().Get("scope") != "repository:"+r.repo+":pull" || req.URL.Query().Get("service") != "fake" {
			http.Error(w, "bad scope", http.StatusForbidden)
			return
		}
		if r.user != "" {
			if u, p, ok := req.BasicAuth(); ok && (u != r.user || p != r.pass) {
				http.Error(w, "bad credentials", http.StatusUnauthorized)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": testToken})
		return
	}
	switch r.auth {
	case "bearer":
		if req.Header.Get("Authorization") != "Bearer "+testToken {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake",scope="repository:%s:pull"`, r.srv.URL, r.repo))
			http.Error(w, `{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`, http.StatusUnauthorized)
			return
		}
	case "basic":
		if u, p, ok := req.BasicAuth(); !ok || u != r.user || p != r.pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="fake"`)
			http.Error(w, "", http.StatusUnauthorized)
			return
		}
	}
	prefix := "/v2/" + r.repo + "/"
	rest, ok := strings.CutPrefix(req.URL.Path, prefix)
	if !ok {
		http.NotFound(w, req)
		return
	}
	kind, ref, _ := strings.Cut(rest, "/")
	if d, ok := r.tags[ref]; ok && kind == "manifests" {
		ref = d
	}
	b, ok := r.ti.blobs[ref]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}`)
		return
	}
	if r.tamper[ref] {
		b = append([]byte{}, b...)
		b[len(b)/2] ^= 1
	}
	if kind == "manifests" {
		var m manifest
		_ = json.Unmarshal(b, &m)
		w.Header().Set("Content-Type", m.MediaType)
	}
	_, _ = w.Write(b)
}

func (r *fakeRegistry) open(t *testing.T, ref, platform string, creds *Credentials) (*Image, error) {
	t.Helper()
	opts := Options{Platform: platform, TempDir: t.TempDir()}
	opts.Credentials = func(_ context.Context, host string) (*Credentials, error) {
		if host != r.host() {
			t.Errorf("credentials asked for %q, want %q", host, r.host())
		}
		return creds, nil
	}
	img, err := Open(context.Background(), "docker://"+r.host()+"/"+ref, opts)
	if err == nil {
		t.Cleanup(func() { _ = img.Close() })
	}
	return img, err
}

func testRegistryImage(t *testing.T) *testImage {
	return buildImage(t, []Platform{amd64, arm64},
		layerTar(t, dir("etc"), file("etc/a", "1")), layerTar(t, file("etc/b", "2"), whiteout("etc/a")))
}

func TestRegistry(t *testing.T) {
	retryDelay = time.Millisecond
	alice := &Credentials{Username: "alice", Password: "pw"}
	for _, tt := range []struct {
		name  string
		auth  string
		creds *Credentials
	}{
		{"token, anonymous", "bearer", nil},
		{"token, with credentials", "bearer", alice},
		{"token, with a registry token", "bearer", &Credentials{RegistryToken: testToken}},
		{"basic", "basic", alice},
		{"no authentication", "", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newFakeRegistry(t, testRegistryImage(t), tt.auth)
			img, err := r.open(t, "test/app:v1", "linux/arm64", tt.creds)
			if err != nil {
				t.Fatal(err)
			}
			if got := envOf(img); got != "[PLATFORM=linux/arm64/v8]" {
				t.Errorf("got %s", got)
			}
			out, order, err := flatten(t, img)
			if err != nil {
				t.Fatal(err)
			}
			if got := summary(out, order); got != "/etc 5\n/etc/b 0 2\n" {
				t.Errorf("got %q", got)
			}
			if tt.auth == "bearer" && tt.creds == alice {
				want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:pw"))
				if len(r.tokens) == 0 || r.tokens[0] != want {
					t.Errorf("token requests: %q, want %q", r.tokens, want)
				}
			}
		})
	}
}

func TestRegistryByDigest(t *testing.T) {
	ti := testRegistryImage(t)
	r := newFakeRegistry(t, ti, "bearer")
	img, err := r.open(t, "test/app@"+ti.root.Digest, "linux/amd64", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := envOf(img); got != "[PLATFORM=linux/amd64]" {
		t.Errorf("got %s", got)
	}
	r.tamper[ti.root.Digest] = true
	if _, err := r.open(t, "test/app@"+ti.root.Digest, "", nil); err == nil || !strings.Contains(err.Error(), "has the digest") {
		t.Errorf("got %v, want a digest error", err)
	}
}

func TestRegistryErrors(t *testing.T) {
	retryDelay = time.Millisecond
	ti := testRegistryImage(t)
	var idx, m manifest
	if err := json.Unmarshal(ti.blobs[ti.root.Digest], &idx); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(ti.blobs[idx.Manifests[0].Digest], &m); err != nil {
		t.Fatal(err)
	}
	layer := m.Layers[0].Digest // of linux/amd64

	t.Run("missing tag", func(t *testing.T) {
		r := newFakeRegistry(t, ti, "bearer")
		if _, err := r.open(t, "test/app:v2", "", nil); err == nil || !strings.Contains(err.Error(), "MANIFEST_UNKNOWN") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("basic without credentials", func(t *testing.T) {
		r := newFakeRegistry(t, ti, "basic")
		if _, err := r.open(t, "test/app:v1", "", nil); err == nil || !strings.Contains(err.Error(), "docker login") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("wrong credentials", func(t *testing.T) {
		r := newFakeRegistry(t, ti, "bearer")
		if _, err := r.open(t, "test/app:v1", "", &Credentials{Username: "alice", Password: "wrong"}); err == nil || !strings.Contains(err.Error(), "check the credentials") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("rate limit", func(t *testing.T) {
		r := newFakeRegistry(t, ti, "")
		r.status["/v2/test/app/manifests/v1"] = http.StatusTooManyRequests
		if _, err := r.open(t, "test/app:v1", "", nil); err == nil || !strings.Contains(err.Error(), "rate limit") || !strings.Contains(err.Error(), "slow down") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("transient errors are retried", func(t *testing.T) {
		r := newFakeRegistry(t, ti, "")
		r.failures["/v2/test/app/blobs/"+layer] = 2
		if _, err := r.open(t, "test/app:v1", "linux/amd64", nil); err != nil {
			t.Errorf("got %v", err)
		}
		r.failures["/v2/test/app/blobs/"+layer] = 3
		if _, err := r.open(t, "test/app:v1", "linux/amd64", nil); err == nil || !strings.Contains(err.Error(), "500") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("corrupted layer", func(t *testing.T) {
		r := newFakeRegistry(t, ti, "")
		r.tamper[layer] = true
		if _, err := r.open(t, "test/app:v1", "linux/amd64", nil); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("invalid references", func(t *testing.T) {
		r := newFakeRegistry(t, ti, "")
		for _, ref := range []string{"Test/App", "test/app:bad tag", "test/app@sha256:abc", "test//app"} {
			if _, err := r.open(t, ref, "", nil); err == nil {
				t.Errorf("%s: accepted", ref)
			}
		}
	})
}

func TestParseReference(t *testing.T) {
	d := "sha256:" + strings.Repeat("a", 64)
	for in, want := range map[string]string{
		"alpine":                      "docker.io/library/alpine:latest",
		"alpine:3.22":                 "docker.io/library/alpine:3.22",
		"docker.io/alpine":            "docker.io/library/alpine:latest",
		"index.docker.io/library/a:1": "docker.io/library/a:1",
		"user/app":                    "docker.io/user/app:latest",
		"ghcr.io/o/r/app:v1":          "ghcr.io/o/r/app:v1",
		"localhost/app":               "localhost/app:latest",
		"localhost:5000/app:1":        "localhost:5000/app:1",
		"127.0.0.1:5000/a-b_c.d/e__f": "127.0.0.1:5000/a-b_c.d/e__f:latest",
		"app@" + d:                    "docker.io/library/app@" + d,
		"app:1@" + d:                  "docker.io/library/app:1@" + d,
		"localhost:5000/app@" + d:     "localhost:5000/app@" + d,
		"localhost:5000":              "docker.io/library/localhost:5000",
	} {
		r, err := parseReference(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if r.String() != want {
			t.Errorf("%s: got %s, want %s", in, r, want)
		}
	}
	if r, _ := parseReference("alpine"); r.apiHost() != "registry-1.docker.io" {
		t.Errorf("api host %s", r.apiHost())
	}
	for _, in := range []string{"", "Alpine", "app:", "app@sha512:" + strings.Repeat("a", 128), "a/-b", "app:" + strings.Repeat("x", 129)} {
		if _, err := parseReference(in); err == nil {
			t.Errorf("%q: accepted", in)
		}
	}
}

func TestParseChallenge(t *testing.T) {
	scheme, params := parseChallenge(`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/alpine:pull,push", error="a \"quoted\" value"`)
	if scheme != "Bearer" || params["realm"] != "https://auth.docker.io/token" || params["service"] != "registry.docker.io" ||
		params["scope"] != "repository:library/alpine:pull,push" || params["error"] != `a "quoted" value` {
		t.Errorf("got %s %q", scheme, params)
	}
	scheme, params = parseChallenge(`Basic realm=Registry`)
	if scheme != "Basic" || params["realm"] != "Registry" {
		t.Errorf("got %s %q", scheme, params)
	}
}

func TestIsLoopback(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "localhost:5000": true, "127.0.0.1:5000": true, "[::1]:5000": true, "::1": true,
		"example.com": false, "10.0.0.1:5000": false, "localhost.example.com": false,
	} {
		if got := isLoopback(host); got != want {
			t.Errorf("%s: got %v", host, got)
		}
	}
}

func TestCheckRedirect(t *testing.T) {
	req := func(method, rawURL string, auth bool) *http.Request {
		r, _ := http.NewRequest(method, rawURL, nil)
		if auth {
			r.Header.Set("Authorization", "Basic secret")
		}
		return r
	}
	for _, tt := range []struct {
		from, to string
		method   string
		ok       bool
		auth     bool // Authorization kept
	}{
		{"https://reg.example/v2/a/blobs/x", "https://reg.example/v2/a/blobs/y", "GET", true, true},
		{"https://reg.example/v2/a/blobs/x", "https://cdn.example/blob?sig=1", "GET", true, false},
		{"https://reg.example/v2/a/blobs/x", "https://reg.example:8443/x", "GET", true, false},
		{"https://reg.example/v2/a/blobs/x", "http://reg.example/x", "GET", false, false},
		{"https://reg.example/v2/a/blobs/x", "http://127.0.0.1:5000/x", "GET", false, false},
		{"http://127.0.0.1:5000/v2/a", "http://127.0.0.1:5001/v2/a", "GET", true, false},
		{"http://localhost:5000/v2/a", "http://localhost:5000/v2/b", "GET", true, true},
		{"https://auth.example/token", "https://evil.example/token", "POST", false, false},
		{"https://auth.example/token", "https://auth.example/token2", "POST", true, true},
	} {
		r := req(tt.method, tt.to, true)
		err := checkRedirect(r, []*http.Request{req(tt.method, tt.from, true)})
		if (err == nil) != tt.ok {
			t.Errorf("%s -> %s: %v", tt.from, tt.to, err)
			continue
		}
		if err == nil && (r.Header.Get("Authorization") != "") != tt.auth {
			t.Errorf("%s -> %s: Authorization kept: %v", tt.from, tt.to, !tt.auth)
		}
	}
}
