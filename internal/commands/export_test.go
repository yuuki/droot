package commands

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuuki/droot/internal/imageconfig"
)

// writeTestLayout writes an OCI image layout of one layer with a file
// newer than the image, and returns its directory.
func writeTestLayout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	blobs := filepath.Join(dir, "blobs", "sha256")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	add := func(b []byte) (string, int) {
		sum := sha256.Sum256(b)
		h := hex.EncodeToString(sum[:])
		if err := os.WriteFile(filepath.Join(blobs, h), b, 0o644); err != nil {
			t.Fatal(err)
		}
		return "sha256:" + h, len(b)
	}
	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	for _, h := range []*tar.Header{
		{Name: "app/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: time.Unix(1700000000, 0)},
		{Name: "app/hello", Typeflag: tar.TypeReg, Mode: 0o755, Size: 2, ModTime: time.Unix(1800000000, 0)},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			_, _ = tw.Write([]byte("hi"))
		}
	}
	_ = tw.Close()
	layerDigest, layerSize := add(layer.Bytes())
	cfg, _ := json.Marshal(map[string]any{
		"created": "2023-11-14T22:13:20Z", "os": "linux", "architecture": "amd64",
		"config": map[string]any{"Env": []string{"A=1"}, "User": "nobody", "Entrypoint": []string{"/app/hello"}, "StopSignal": "SIGINT"},
		"rootfs": map[string]any{"type": "layers", "diff_ids": []string{layerDigest}},
	})
	cfgDigest, cfgSize := add(cfg)
	m, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": cfgDigest, "size": cfgSize},
		"layers": []any{map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": layerDigest, "size": layerSize}},
	})
	mDigest, mSize := add(m)
	idx, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"manifests": []any{map[string]any{
			"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": mDigest, "size": mSize,
			"annotations": map[string]string{"org.opencontainers.image.ref.name": "v1"},
		}},
	})
	if err := os.WriteFile(filepath.Join(dir, "index.json"), idx, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestExportWithoutDaemon(t *testing.T) {
	// No daemon may be contacted.
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	dir := writeTestLayout(t)
	out := filepath.Join(t.TempDir(), "out.tar")
	app := NewApp("test")
	if err := app.Run(context.Background(), []string{"droot", "export", "--quiet", "--reproducible", "-o", out, "oci:" + dir + ":v1"}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	files := map[string]string{}
	created := time.Date(2023, 11, 14, 22, 13, 20, 0, time.UTC)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		files[hdr.Name] = string(b)
		if hdr.ModTime.After(created) {
			t.Errorf("%s: modification time %v after the creation of the image", hdr.Name, hdr.ModTime)
		}
	}
	if files["app/hello"] != "hi" {
		t.Errorf("app/hello: %q", files["app/hello"])
	}
	if files[".drootenv"] != "A=1\n" {
		t.Errorf(".drootenv: %q", files[".drootenv"])
	}
	cfg, err := imageconfig.Parse([]byte(files[".droot/config.json"]), "config.json")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Image != "oci:"+dir+":v1" || !strings.HasPrefix(cfg.ImageID, "sha256:") || cfg.User != "nobody" ||
		cfg.StopSignal != "SIGINT" || len(cfg.Entrypoint) != 1 {
		t.Errorf("config: %+v", cfg)
	}
}

func TestExportWithoutDaemonErrors(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	dir := writeTestLayout(t)
	out := filepath.Join(t.TempDir(), "out.tar")
	for _, tt := range []struct {
		argv []string
		want string
	}{
		{[]string{"--pull", "oci:" + dir}, "--pull is for the images of a Docker daemon"},
		{[]string{"oci:" + dir + ":v2"}, `no image named "v2"`},
		{[]string{"--platform", "linux/arm64", "oci:" + dir}, "the image is for linux/amd64, not linux/arm64"},
		{[]string{"--platform", "linux", "oci:" + dir}, "invalid platform"},
		{[]string{"docker-archive:" + dir}, "is a directory"},
	} {
		app := NewApp("test")
		err := app.Run(context.Background(), append([]string{"droot", "export", "--quiet", "-o", out}, tt.argv...))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%q: got %v, want %q", tt.argv, err, tt.want)
		}
		if _, err := os.Stat(out); err == nil {
			t.Errorf("%q: %s was written", tt.argv, out)
		}
	}

	// An error of the layers ends the export.
	layers, _ := filepath.Glob(filepath.Join(dir, "blobs", "sha256", "*"))
	for _, l := range layers {
		b, _ := os.ReadFile(l)
		if bytes.HasPrefix(b, []byte("app/")) {
			// The tar stream stays valid: only the digest tells.
			_ = os.WriteFile(l, bytes.Replace(b, []byte("hi"), []byte("ho"), 1), 0o644)
		}
	}
	app := NewApp("test")
	err := app.Run(context.Background(), []string{"droot", "export", "--quiet", "-o", out, "oci:" + dir})
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Errorf("got %v, want a digest mismatch", err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Errorf("%s was written", out)
	}
}
