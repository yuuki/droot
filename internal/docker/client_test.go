package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeDaemon is a tiny Docker Engine API implementation for tests.
type fakeDaemon struct {
	mu       sync.Mutex
	requests []string
}

func (d *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.requests = append(d.requests, r.Method+" "+r.URL.RequestURI())
	d.mu.Unlock()

	switch {
	case r.URL.Path == "/_ping":
		w.Header().Set("Api-Version", "1.99")
		_, _ = io.WriteString(w, "OK")
	case r.Method == http.MethodPost && r.URL.Path == "/v1.99/containers/create":
		var cfg ContainerConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if cfg.Image == "missing" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"No such image: missing:latest"}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"Id":"`+fakeID+`","Warnings":["platform mismatch"]}`)
	case r.Method == http.MethodGet && r.URL.Path == "/v1.99/containers/"+fakeID+"/json":
		_, _ = io.WriteString(w, `{"Id":"`+fakeID+`","Config":{"Env":["PATH=/bin","A=b=c"],"WorkingDir":"/app"}}`)
	case r.Method == http.MethodGet && r.URL.Path == "/v1.99/images/sha256:abc/json":
		_, _ = io.WriteString(w, `{"Id":"sha256:abc","Config":{"Env":["PATH=/bin"]}}`)
	case r.Method == http.MethodGet && r.URL.Path == "/v1.99/containers/"+fakeID+"/export":
		_, _ = io.WriteString(w, "TAR")
	case r.Method == http.MethodDelete && r.URL.Path == "/v1.99/containers/"+fakeID:
		if r.URL.Query().Get("force") != "1" || r.URL.Query().Get("v") != "1" {
			http.Error(w, "force and v required", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "unexpected request")
	}
}

func exerciseClient(t *testing.T, c *Client, d *fakeDaemon) {
	t.Helper()
	ctx := context.Background()

	if err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if v, _ := c.APIVersion(ctx); v != "1.99" {
		t.Fatalf("negotiated version = %q, want 1.99", v)
	}

	id, err := c.ContainerCreate(ctx, &ContainerConfig{Image: "img"}, "linux/amd64")
	if err != nil || id != fakeID {
		t.Fatalf("ContainerCreate = %q, %v", id, err)
	}
	info, err := c.ContainerInspect(ctx, id)
	if err != nil {
		t.Fatalf("ContainerInspect: %v", err)
	}
	if strings.Join(info.Config.Env, ",") != "PATH=/bin,A=b=c" || info.Config.WorkingDir != "/app" {
		t.Fatalf("unexpected inspect result: %+v", info)
	}
	img, err := c.ImageInspect(ctx, "sha256:abc", "linux/amd64")
	if err != nil || img.Config == nil || strings.Join(img.Config.Env, ",") != "PATH=/bin" {
		t.Fatalf("ImageInspect = %+v, %v", img, err)
	}
	rc, err := c.ContainerExport(ctx, id)
	if err != nil {
		t.Fatalf("ContainerExport: %v", err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "TAR" {
		t.Fatalf("export body = %q", b)
	}
	if err := c.ContainerRemove(ctx, id); err != nil {
		t.Fatalf("ContainerRemove: %v", err)
	}

	_, err = c.ContainerCreate(ctx, &ContainerConfig{Image: "missing"}, "")
	if !IsNotFound(err) || !strings.Contains(err.Error(), "No such image: missing:latest") {
		t.Fatalf("want not-found APIError, got %v", err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.requests[1] != "POST /v1.99/containers/create?platform=linux%2Famd64" {
		t.Errorf("unexpected create request %q", d.requests[1])
	}
}

func TestClientUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	d := &fakeDaemon{}
	srv := &httptest.Server{Listener: l, Config: &http.Server{Handler: d}}
	srv.Start()
	defer srv.Close()

	t.Setenv("DOCKER_HOST", "unix://"+sock)
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")
	c, err := NewClientFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	exerciseClient(t, c, d)
}

func TestClientTCP(t *testing.T) {
	d := &fakeDaemon{}
	srv := httptest.NewServer(d)
	defer srv.Close()

	c, err := NewClient(Endpoint{Host: "tcp://" + srv.Listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	exerciseClient(t, c, d)
}

func TestClientTLSVerify(t *testing.T) {
	d := &fakeDaemon{}
	srv := httptest.NewTLSServer(d)
	defer srv.Close()

	certDir := t.TempDir()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(certDir, "ca.pem"), caPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DOCKER_HOST", "tcp://"+srv.Listener.Addr().String())
	t.Setenv("DOCKER_TLS_VERIFY", "1")
	t.Setenv("DOCKER_CERT_PATH", certDir)
	t.Setenv("DOCKER_API_VERSION", "")
	c, err := NewClientFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	exerciseClient(t, c, d)

	// Without the CA the server must not be trusted.
	c, err = NewClient(Endpoint{Host: "tcp://" + srv.Listener.Addr().String(), UseTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("want certificate verification error")
	}
}

func TestClientPinnedAPIVersion(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		_, _ = io.WriteString(w, `{"Id":"x"}`)
	}))
	defer srv.Close()

	c, err := NewClient(Endpoint{Host: "tcp://" + srv.Listener.Addr().String(), APIVersion: "1.41"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ContainerInspect(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if got != "/v1.41/containers/x/json" {
		t.Fatalf("request path = %q, want pinned version", got)
	}
}

func TestClientRefusesRedirectsAndOddStatuses(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		switch r.URL.Path {
		case "/v1.41/containers/x/json":
			http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
		case "/v1.41/containers/x/export":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s", r.URL)
		}
	}))
	defer srv.Close()
	c, err := NewClient(Endpoint{Host: "tcp://" + srv.Listener.Addr().String(), APIVersion: "1.41"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ContainerInspect(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("want a redirect error, got %v", err)
	}
	if hits != 1 {
		t.Fatalf("the redirect was followed (%d requests)", hits)
	}
	if _, err := c.ContainerExport(context.Background(), "x"); err == nil {
		t.Fatal("an export answered with 204 must fail")
	}
}

func TestAPIVersionEdgeCases(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/_ping" {
			return // no Api-Version header
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"Id":"x"}`)
	}))
	defer srv.Close()
	host := "tcp://" + srv.Listener.Addr().String()
	ctx := context.Background()

	c, _ := NewClient(Endpoint{Host: host})
	if v, err := c.APIVersion(ctx); err != nil || v != "1.41" {
		t.Errorf("fallback version = %q, %v", v, err)
	}

	c, _ = NewClient(Endpoint{Host: host, APIVersion: "v1.45"})
	if _, err := c.ContainerCreate(ctx, &ContainerConfig{Image: "i"}, ""); err != nil {
		t.Fatal(err)
	}
	if last := paths[len(paths)-1]; last != "/v1.45/containers/create" {
		t.Errorf("a leading v must be accepted, requested %s", last)
	}

	c, _ = NewClient(Endpoint{Host: host, APIVersion: "1.40"})
	if _, err := c.ContainerCreate(ctx, &ContainerConfig{Image: "i"}, "linux/arm64"); err == nil {
		t.Error("--platform with API 1.40 must fail instead of being ignored")
	}
}

func TestVersionLess(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want bool
	}{{"1.40", "1.41", true}, {"1.41", "1.41", false}, {"1.9", "1.41", true}, {"1.100", "1.41", false}, {"2.0", "1.41", false}} {
		if got := versionLess(tt.a, tt.b); got != tt.want {
			t.Errorf("versionLess(%s, %s) = %v", tt.a, tt.b, got)
		}
	}
}

func TestClientConnectionError(t *testing.T) {
	c, err := NewClient(Endpoint{Host: "unix://" + filepath.Join(t.TempDir(), "nonexistent.sock")})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Ping(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cannot connect to the Docker daemon") {
		t.Fatalf("want connection error, got %v", err)
	}
}

func TestUnixSocketPathsAreLiteral(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"pct%41.sock", "hash#x.sock", "q?x.sock", "sp ace.sock"} {
		t.Run(name, func(t *testing.T) {
			sock := filepath.Join(dir, name)
			l, err := net.Listen("unix", sock)
			if err != nil {
				t.Skipf("cannot listen on %q: %v", sock, err)
			}
			srv := &httptest.Server{Listener: l, Config: &http.Server{Handler: &fakeDaemon{}}}
			srv.Start()
			defer srv.Close()
			c, err := NewClient(Endpoint{Host: "unix://" + sock})
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Ping(context.Background()); err != nil {
				t.Fatalf("Ping through %q: %v", sock, err)
			}
		})
	}

	t.Run("abstract socket", func(t *testing.T) {
		name := "@droot-test-" + filepath.Base(dir)
		l, err := net.Listen("unix", name)
		if err != nil {
			t.Skipf("abstract sockets unavailable: %v", err)
		}
		srv := &httptest.Server{Listener: l, Config: &http.Server{Handler: &fakeDaemon{}}}
		srv.Start()
		defer srv.Close()
		c, err := NewClient(Endpoint{Host: "unix://" + name})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Ping(context.Background()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("host without scheme is tcp", func(t *testing.T) {
		srv := httptest.NewServer(&fakeDaemon{})
		defer srv.Close()
		c, err := NewClient(Endpoint{Host: srv.Listener.Addr().String()})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Ping(context.Background()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("empty unix path is the default socket", func(t *testing.T) {
		c, err := NewClient(Endpoint{Host: "unix://"})
		if err != nil {
			t.Fatal(err)
		}
		_ = c // the default socket may or may not exist here
	})
}

func TestNewClientUnsupported(t *testing.T) {
	for _, host := range []string{"ssh://user:pw@host", "ssh://host/path", "ssh://", "npipe:////./pipe/docker_engine", "fd://", "tcp://", "%zz://x"} {
		if _, err := NewClient(Endpoint{Host: host}); err == nil {
			t.Errorf("NewClient(%q) should fail", host)
		}
	}
}

func writeContext(t *testing.T, configDir, name, meta string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(name))
	id := hex.EncodeToString(sum[:])
	dir := filepath.Join(configDir, "contexts", "meta", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestEndpointFromEnv(t *testing.T) {
	configDir := t.TempDir()
	writeContext(t, configDir, "colima",
		`{"Name":"colima","Endpoints":{"docker":{"Host":"unix:///home/me/.colima/default/docker.sock","SkipTLSVerify":false}}}`)
	writeContext(t, configDir, "skipverify",
		`{"Name":"skipverify","Endpoints":{"docker":{"Host":"tcp://192.0.2.11:2376","SkipTLSVerify":true}}}`)
	id := writeContext(t, configDir, "remote",
		`{"Name":"remote","Endpoints":{"docker":{"Host":"tcp://192.0.2.10:2376","SkipTLSVerify":true}}}`)
	if err := os.MkdirAll(filepath.Join(configDir, "contexts", "tls", id, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		env     map[string]string
		config  string
		want    Endpoint
		wantErr bool
	}{
		{
			name: "default socket",
			want: Endpoint{Host: DefaultHost},
		},
		{
			name: "DOCKER_HOST",
			env:  map[string]string{"DOCKER_HOST": "tcp://192.0.2.1:2375"},
			want: Endpoint{Host: "tcp://192.0.2.1:2375"},
		},
		{
			name: "DOCKER_HOST with TLS verification",
			env:  map[string]string{"DOCKER_HOST": "tcp://192.0.2.1:2376", "DOCKER_TLS_VERIFY": "1", "DOCKER_CERT_PATH": "/certs"},
			want: Endpoint{Host: "tcp://192.0.2.1:2376", UseTLS: true, CertDir: "/certs"},
		},
		{
			name: "DOCKER_CERT_PATH alone does not enable TLS (like the docker CLI)",
			env:  map[string]string{"DOCKER_HOST": "tcp://192.0.2.1:2376", "DOCKER_CERT_PATH": "/certs"},
			want: Endpoint{Host: "tcp://192.0.2.1:2376"},
		},
		{
			name: "DOCKER_TLS enables TLS without verification",
			env:  map[string]string{"DOCKER_HOST": "tcp://192.0.2.1:2376", "DOCKER_TLS": "1", "DOCKER_CERT_PATH": "/certs"},
			want: Endpoint{Host: "tcp://192.0.2.1:2376", UseTLS: true, CertDir: "/certs", SkipTLSVerify: true},
		},
		{
			name: "DOCKER_TLS_VERIFY defaults cert path to config dir",
			env:  map[string]string{"DOCKER_HOST": "tcp://192.0.2.1:2376", "DOCKER_TLS_VERIFY": "1"},
			want: Endpoint{Host: "tcp://192.0.2.1:2376", UseTLS: true, CertDir: configDir},
		},
		{
			name: "DOCKER_HOST wins over DOCKER_CONTEXT",
			env:  map[string]string{"DOCKER_HOST": "tcp://192.0.2.1:2375", "DOCKER_CONTEXT": "colima"},
			want: Endpoint{Host: "tcp://192.0.2.1:2375"},
		},
		{
			name: "DOCKER_CONTEXT",
			env:  map[string]string{"DOCKER_CONTEXT": "colima"},
			want: Endpoint{Host: "unix:///home/me/.colima/default/docker.sock"},
		},
		{
			name:   "current context from config.json",
			config: `{"currentContext":"remote"}`,
			want: Endpoint{Host: "tcp://192.0.2.10:2376", UseTLS: true, SkipTLSVerify: true,
				CertDir: filepath.Join(configDir, "contexts", "tls", id, "docker")},
		},
		{
			name: "context skipping TLS verification without TLS files still uses TLS",
			env:  map[string]string{"DOCKER_CONTEXT": "skipverify"},
			want: Endpoint{Host: "tcp://192.0.2.11:2376", UseTLS: true, SkipTLSVerify: true},
		},
		{
			name:   "default context",
			config: `{"currentContext":"default"}`,
			want:   Endpoint{Host: DefaultHost},
		},
		{
			name:   "DOCKER_API_VERSION",
			env:    map[string]string{"DOCKER_API_VERSION": "1.44"},
			config: `{"currentContext":"colima"}`,
			want:   Endpoint{Host: "unix:///home/me/.colima/default/docker.sock", APIVersion: "1.44"},
		},
		{
			name:    "unknown context",
			env:     map[string]string{"DOCKER_CONTEXT": "nope"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range []string{"DOCKER_HOST", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH", "DOCKER_CONTEXT", "DOCKER_API_VERSION"} {
				t.Setenv(k, tt.env[k])
			}
			t.Setenv("DOCKER_CONFIG", configDir)
			cfgFile := filepath.Join(configDir, "config.json")
			_ = os.Remove(cfgFile)
			if tt.config != "" {
				if err := os.WriteFile(cfgFile, []byte(tt.config), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			got, err := EndpointFromEnv()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
