package image

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	amd64 = Platform{OS: "linux", Architecture: "amd64"}
	arm64 = Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}
	armv6 = Platform{OS: "linux", Architecture: "arm", Variant: "v6"}
	armv7 = Platform{OS: "linux", Architecture: "arm", Variant: "v7"}
)

func open(t *testing.T, ref string, platform string) (*Image, error) {
	t.Helper()
	img, err := Open(context.Background(), ref, Options{Platform: platform, TempDir: t.TempDir()})
	if err == nil {
		t.Cleanup(func() { _ = img.Close() })
	}
	return img, err
}

func TestOpenLayout(t *testing.T) {
	l1 := layerTar(t, dir("etc"), file("etc/os", "test"))
	l2 := layerTar(t, file("etc/os", "test2"), file("app", "x"))
	ti := buildImage(t, []Platform{amd64, arm64, armv6, armv7}, l1, l2)
	dir := filepath.Join(t.TempDir(), "lay:out") // a colon in the path
	ti.writeLayout(t, dir, "v1")
	archive := filepath.Join(t.TempDir(), "image.tar")
	tarFile(t, archive, layoutFiles(t, dir))

	for _, ref := range []string{"oci:" + dir, "oci:" + dir + ":v1", "oci-archive:" + archive, "oci-archive:" + archive + ":v1"} {
		for platform, want := range map[string]string{
			"linux/amd64":    "[PLATFORM=linux/amd64]",
			"linux/arm64":    "[PLATFORM=linux/arm64/v8]",
			"linux/arm64/v8": "[PLATFORM=linux/arm64/v8]",
			"linux/arm":      "[PLATFORM=linux/arm/v7]",
			"linux/arm/v6":   "[PLATFORM=linux/arm/v6]",
		} {
			img, err := open(t, ref, platform)
			if err != nil {
				t.Fatalf("%s %s: %v", ref, platform, err)
			}
			if got := envOf(img); got != want {
				t.Errorf("%s %s: got %s, want %s", ref, platform, got, want)
			}
			out, order, err := flatten(t, img)
			if err != nil {
				t.Fatal(err)
			}
			if got := summary(out, order); got != "/etc 5\n/etc/os 0 test2\n/app 0 x\n" {
				t.Errorf("%s: got %q", ref, got)
			}
		}
	}

	for ref, want := range map[string]string{
		"oci:" + dir + ":v2":               `no image named "v2" (it has: v1)`,
		"oci:" + archive:                   "is not a directory",
		"oci-archive:" + dir + "/oci-none": "no such file",
		"oci:" + t.TempDir():               "no oci-layout file",
	} {
		if _, err := open(t, ref, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", ref, err, want)
		}
	}
	if _, err := open(t, "oci:"+dir, "linux/s390x"); err == nil || !strings.Contains(err.Error(), "not available for linux/s390x") {
		t.Errorf("got %v", err)
	}
	if _, err := open(t, "oci:"+dir, "linux"); err == nil || !strings.Contains(err.Error(), "invalid platform") {
		t.Errorf("got %v", err)
	}
}

func TestOpenLayoutSeveralImages(t *testing.T) {
	dir := t.TempDir()
	a := buildImage(t, []Platform{amd64}, layerTar(t, file("a", "1")))
	b := buildImage(t, []Platform{amd64}, layerTar(t, file("b", "1")))
	b.writeLayout(t, dir, "b")
	a.writeLayout(t, dir, "a")
	// Merge the indexes: a.writeLayout replaced index.json.
	idx := manifest{SchemaVersion: 2, MediaType: MediaTypeOCIIndex}
	for name, ti := range map[string]*testImage{"a": a, "docker.io/library/b:latest": b} {
		d := ti.root
		d.Annotations = map[string]string{annotationContainerName: name}
		idx.Manifests = append(idx.Manifests, d)
		for k, v := range ti.blobs {
			_ = os.WriteFile(filepath.Join(dir, "blobs", "sha256", strings.TrimPrefix(k, "sha256:")), v, 0o644)
		}
	}
	writeJSON(t, filepath.Join(dir, "index.json"), idx)

	if _, err := open(t, "oci:"+dir, ""); err == nil || !strings.Contains(err.Error(), "several images") {
		t.Errorf("got %v", err)
	}
	for ref, want := range map[string]string{"oci:" + dir + ":a": "/a 0 1\n", "oci:" + dir + ":b": "/b 0 1\n", "oci:" + dir + ":docker.io/library/b": "/b 0 1\n"} {
		img, err := open(t, ref, "")
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		out, order, err := flatten(t, img)
		if err != nil {
			t.Fatal(err)
		}
		if got := summary(out, order); got != want {
			t.Errorf("%s: got %q, want %q", ref, got, want)
		}
	}
}

func TestOpenLayoutVerifiesBlobs(t *testing.T) {
	ti := buildImage(t, []Platform{amd64}, layerTar(t, file("a", "1")))
	dir := t.TempDir()
	ti.writeLayout(t, dir)
	var m manifest
	if err := json.Unmarshal(ti.blobs[ti.root.Digest], &m); err != nil {
		t.Fatal(err)
	}
	layer := filepath.Join(dir, "blobs", "sha256", strings.TrimPrefix(m.Layers[0].Digest, "sha256:"))
	b, _ := os.ReadFile(layer)
	b[len(b)-1] ^= 1
	if err := os.WriteFile(layer, b, 0o644); err != nil {
		t.Fatal(err)
	}
	img, err := open(t, "oci:"+dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := flatten(t, img); err == nil || (!strings.Contains(err.Error(), "digest mismatch") && !strings.Contains(err.Error(), "gzip")) {
		t.Errorf("got %v, want a digest mismatch", err)
	}

	cfg := filepath.Join(dir, "blobs", "sha256", strings.TrimPrefix(m.Config.Digest, "sha256:"))
	if err := os.WriteFile(cfg, []byte(`{"rootfs":{"diff_ids":[]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := open(t, "oci:"+dir, ""); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Errorf("got %v, want a digest mismatch", err)
	}
}

func TestOpenDockerArchive(t *testing.T) {
	l1 := layerTar(t, file("a", "1"), file("b", "1"))
	l2 := layerTar(t, whiteout("a"))
	cfg := mustJSON(t, map[string]any{
		"created": "2024-01-02T03:04:05Z", "os": "linux", "architecture": "amd64",
		"config": map[string]any{"Env": []string{"X=1"}, "User": "app"},
		"rootfs": map[string]any{"diff_ids": []string{digestOf(l1), digestOf(l2), digestOf(l1)}},
	})
	id := strings.TrimPrefix(digestOf(cfg), "sha256:")
	// The format of Docker < 25: layer directories, a link for a layer
	// used twice.
	legacy := filepath.Join(t.TempDir(), "legacy.tar")
	tarFile(t, legacy, map[string][]byte{
		"manifest.json": mustJSON(t, []map[string]any{{
			"Config": id + ".json", "RepoTags": []string{"example/app:1"},
			"Layers": []string{"l1/layer.tar", "l2/layer.tar", "l3/layer.tar"},
		}}),
		id + ".json":   cfg,
		"l1/layer.tar": l1,
		"l2/layer.tar": l2,
		"l3/layer.tar": []byte("->../l1/layer.tar"),
	})
	// The format of Docker >= 25: blobs named by digest, gzipped layers
	// (with the containerd image store).
	gz1, gz2 := gzipBytes(t, l1), gzipBytes(t, l2)
	blob := func(b []byte) string { return "blobs/sha256/" + strings.TrimPrefix(digestOf(b), "sha256:") }
	modern := filepath.Join(t.TempDir(), "modern.tar")
	tarFile(t, modern, map[string][]byte{
		"manifest.json": mustJSON(t, []map[string]any{
			{"Config": blob(cfg), "RepoTags": []string{"example/app:1", "example/app:latest"}, "Layers": []string{blob(gz1), blob(gz2), blob(gz1)}},
			{"Config": blob(cfg), "RepoTags": []string{"other:2"}, "Layers": []string{blob(gz1), blob(gz2), blob(gz1)}},
		}),
		blob(cfg): cfg,
		blob(gz1): gz1,
		blob(gz2): gz2,
	})
	for _, ref := range []string{"docker-archive:" + legacy, "docker-archive:" + legacy + ":example/app:1", "docker-archive:" + modern + ":example/app", "docker-archive:" + modern + ":docker.io/example/app:latest"} {
		img, err := open(t, ref, "")
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if img.ID != "sha256:"+id || img.Config.Config.User != "app" {
			t.Errorf("%s: got %s %+v", ref, img.ID, img.Config)
		}
		out, order, err := flatten(t, img)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if got := summary(out, order); got != "/a 0 1\n/b 0 1\n" { // the third layer adds a again
			t.Errorf("%s: got %q", ref, got)
		}
	}
	for ref, want := range map[string]string{
		"docker-archive:" + modern:                         "several images",
		"docker-archive:" + modern + ":nope":               `no image named "nope"`,
		"docker-archive:" + legacy + ":example/app:2":      `no image named "example/app:2" (it has: example/app:1)`,
		"docker-archive:" + filepath.Dir(legacy):           "is a directory",
		"docker-archive:" + legacy + "-missing":            "no such file",
		"docker-archive:" + writeTemp(t, gzipBytes(t, l1)): "is compressed",
	} {
		if _, err := open(t, ref, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", ref, err, want)
		}
	}
	if _, err := open(t, "docker-archive:"+legacy, "linux/arm64"); err == nil || !strings.Contains(err.Error(), "the image is for linux/amd64, not linux/arm64") {
		t.Errorf("got %v", err)
	}
}

func writeTemp(t *testing.T, b []byte) string {
	name := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(name, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestIsReference(t *testing.T) {
	for ref, want := range map[string]bool{
		"docker://alpine": true, "docker-archive:a.tar": true, "oci:dir": true, "oci-archive:a.tar": true,
		"alpine": false, "localhost:5000/app": false, "docker.io/library/alpine": false, "sha256:abc": false,
	} {
		if got := IsReference(ref); got != want {
			t.Errorf("IsReference(%q) = %v", ref, got)
		}
	}
	if _, err := Open(context.Background(), "alpine", Options{}); err == nil {
		t.Error("Open accepted a reference without transport")
	}
}

func TestPlatformScore(t *testing.T) {
	for _, tt := range []struct {
		want, have string
		ok         bool
	}{
		{"linux/amd64", "linux/amd64", true},
		{"linux/amd64", "linux/amd64/v1", true},
		{"linux/amd64", "linux/amd64/v3", false},
		{"linux/amd64", "linux/arm64", false},
		{"linux/amd64", "windows/amd64", false},
		{"linux/arm64", "linux/arm64/v8", true},
		{"linux/arm64/v8", "linux/aarch64", true},
		{"linux/arm", "linux/arm/v7", true},
		{"linux/arm/v7", "linux/arm/v6", true},
		{"linux/arm/v6", "linux/arm/v7", false},
		{"linux/386", "linux/i386", true},
	} {
		want, _ := ParsePlatform(tt.want)
		have, _ := ParsePlatform(tt.have)
		if got := want.score(have) > 0; got != tt.ok {
			t.Errorf("%s runs %s: got %v", tt.want, tt.have, got)
		}
	}
	// The best variant wins.
	sel := selector{want: Platform{OS: "linux", Architecture: "arm", Variant: "v7"}}
	d, err := sel.choose([]Descriptor{
		{Digest: "v5", Platform: &Platform{OS: "linux", Architecture: "arm", Variant: "v5"}},
		{Digest: "v6", Platform: &Platform{OS: "linux", Architecture: "arm", Variant: "v6"}},
		{Digest: "amd64", Platform: &amd64},
	})
	if err != nil || d.Digest != "v6" {
		t.Errorf("got %v %v, want v6", d.Digest, err)
	}
}
