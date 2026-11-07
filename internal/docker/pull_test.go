package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWithDefaultTag(t *testing.T) {
	for ref, want := range map[string]string{
		"alpine":                         "alpine:latest",
		"alpine:3.22":                    "alpine:3.22",
		"localhost:5000/app":             "localhost:5000/app:latest",
		"localhost:5000/app:1":           "localhost:5000/app:1",
		"ghcr.io/o/app@sha256:abc":       "ghcr.io/o/app@sha256:abc",
		"registry.example.com:443/a/b/c": "registry.example.com:443/a/b/c:latest",
	} {
		if got := WithDefaultTag(ref); got != want {
			t.Errorf("WithDefaultTag(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestRegistryHost(t *testing.T) {
	for ref, want := range map[string]string{
		"alpine": "docker.io", "library/alpine": "docker.io", "user/app:1": "docker.io",
		"docker.io/library/alpine": "docker.io", "index.docker.io/x/y": "docker.io",
		"ghcr.io/o/app": "ghcr.io", "localhost/app": "localhost", "localhost:5000/app": "localhost:5000",
		"registry.example.com:443/a": "registry.example.com:443",
	} {
		if got := registryHost(ref); got != want {
			t.Errorf("registryHost(%q) = %q, want %q", ref, got, want)
		}
	}
}

func decodeAuth(t *testing.T, header string) authConfig {
	t.Helper()
	b, err := base64.URLEncoding.DecodeString(header)
	if err != nil {
		t.Fatal(err)
	}
	var a authConfig
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRegistryAuth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	ctx := context.Background()
	write := func(cfg string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if a, err := RegistryAuth(ctx, "alpine"); a != "" || err != nil {
		t.Fatalf("no config: %q, %v", a, err)
	}

	userPass := base64.StdEncoding.EncodeToString([]byte("hub-user:hub:pass"))
	write(`{"auths": {"https://index.docker.io/v1/": {"auth": "` + userPass + `"},
		"https://ghcr.io": {"identitytoken": "tok"}, "other.example.com": {}}}`)
	a, err := RegistryAuth(ctx, "library/alpine:3")
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeAuth(t, a); got != (authConfig{Username: "hub-user", Password: "hub:pass", ServerAddress: dockerHubServer}) {
		t.Errorf("Docker Hub: %+v", got)
	}
	a, _ = RegistryAuth(ctx, "ghcr.io/o/app")
	if got := decodeAuth(t, a); got != (authConfig{IdentityToken: "tok", ServerAddress: "ghcr.io"}) {
		t.Errorf("ghcr.io: %+v", got)
	}
	if a, err := RegistryAuth(ctx, "other.example.com/app"); a != "" || err != nil {
		t.Errorf("an empty entry gives no credentials: %q, %v", a, err)
	}

	// Credential helpers, which get the server on their standard input.
	bin := t.TempDir()
	helper := `#!/bin/sh
read server
case "$server" in
https://index.docker.io/v1/) echo '{"ServerURL":"x","Username":"<token>","Secret":"hub-token"}' ;;
registry.example.com) echo '{"ServerURL":"x","Username":"u","Secret":"s"}' ;;
*) echo "credentials not found in native keychain"; exit 1 ;;
esac
`
	for _, name := range []string{"docker-credential-store", "docker-credential-ecr"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(helper), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	write(`{"credsStore": "store", "credHelpers": {"registry.example.com": "ecr", "broken.example.com": "missing"},
		"auths": {"https://index.docker.io/v1/": {"auth": "` + userPass + `"}}}`)
	a, err = RegistryAuth(ctx, "alpine")
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeAuth(t, a); got != (authConfig{IdentityToken: "hub-token", ServerAddress: dockerHubServer}) {
		t.Errorf("credsStore: %+v", got)
	}
	a, _ = RegistryAuth(ctx, "registry.example.com/app")
	if got := decodeAuth(t, a); got != (authConfig{Username: "u", Password: "s", ServerAddress: "registry.example.com"}) {
		t.Errorf("credHelpers: %+v", got)
	}
	if a, err := RegistryAuth(ctx, "quay.io/x"); a != "" || err != nil {
		t.Errorf("credentials not found: %q, %v", a, err)
	}
	if _, err := RegistryAuth(ctx, "broken.example.com/x"); err == nil || !strings.Contains(err.Error(), "docker-credential-missing") {
		t.Errorf("a missing helper must be reported, got %v", err)
	}
}

func TestImagePull(t *testing.T) {
	var gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_ping":
			w.Header().Set("Api-Version", "1.45")
		case "/v1.45/images/create":
			gotQuery, gotAuth = r.URL.RawQuery, r.Header.Get("X-Registry-Auth")
			if strings.Contains(r.URL.Query().Get("fromImage"), "denied") {
				_, _ = io.WriteString(w, `{"status":"Pulling from x"}`+"\n"+`{"errorDetail":{"message":"pull access denied"},"error":"pull access denied"}`+"\n")
				return
			}
			_, _ = io.WriteString(w, `{"status":"Pulling from library/alpine","id":"3.22"}`+"\n"+`{"status":"Download complete","id":"abc"}`+"\n")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, err := NewClient(Endpoint{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	if err := c.ImagePull(context.Background(), "alpine", "linux/arm64", "AUTH", func(s string) { lines = append(lines, s) }); err != nil {
		t.Fatal(err)
	}
	if gotQuery != "fromImage=alpine%3Alatest&platform=linux%2Farm64" || gotAuth != "AUTH" {
		t.Errorf("query %q, auth %q", gotQuery, gotAuth)
	}
	if strings.Join(lines, "|") != "3.22: Pulling from library/alpine|abc: Download complete" {
		t.Errorf("progress %q", lines)
	}
	err = c.ImagePull(context.Background(), "denied/app:1", "", "", nil)
	if err == nil || !strings.Contains(err.Error(), "pull access denied") {
		t.Errorf("an error message in the stream must fail the pull, got %v", err)
	}
}

// TestCredentialHelperChildKeepsOutputOpen checks that a credential helper
// that is a script does not hold droot beyond its timeout, or beyond its own
// exit, when a child of the script keeps its output open.
func TestCredentialHelperChildKeepsOutputOpen(t *testing.T) {
	bin := t.TempDir()
	for name, script := range map[string]string{
		// The child (sleep) hangs, as on a locked keychain, and keeps the
		// output of the killed script open.
		"docker-credential-hang": "#!/bin/sh\nread server\nsleep 10\n",
		// The script answers and exits, leaving a child with its output.
		"docker-credential-daemon": "#!/bin/sh\nread server\necho '{\"Username\":\"u\",\"Secret\":\"s\"}'\nsleep 10 &\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := credentialHelper(ctx, "hang", "registry.example.com"); err == nil {
		t.Error("a helper killed by the timeout must fail")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the timeout did not hold: the helper took %v", d)
	}

	start = time.Now()
	creds, err := credentialHelper(context.Background(), "daemon", "registry.example.com")
	if err != nil || creds == nil || *creds != (authConfig{Username: "u", Password: "s"}) {
		t.Errorf("got %+v, %v", creds, err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("droot waited for the child of the helper: %v", d)
	}
}
