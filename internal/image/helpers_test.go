package image

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// tEntry is an entry of a test layer.
type tEntry struct {
	name string
	typ  byte // tar.TypeReg if 0
	body string
	link string
	mode int64
}

func file(name, body string) tEntry   { return tEntry{name: name, body: body} }
func dir(name string) tEntry          { return tEntry{name: name, typ: tar.TypeDir} }
func symlink(name, to string) tEntry  { return tEntry{name: name, typ: tar.TypeSymlink, link: to} }
func hardlink(name, to string) tEntry { return tEntry{name: name, typ: tar.TypeLink, link: to} }
func whiteout(name string) tEntry {
	return file(path.Join(path.Dir(name), whiteoutPrefix+path.Base(name)), "")
}
func opaque(d string) tEntry { return file(path.Join(d, opaqueWhiteout), "") }

var testTime = time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)

func layerTar(t testing.TB, entries ...tEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typ, Linkname: e.link, Mode: e.mode, ModTime: testTime}
		if hdr.Typeflag == 0 {
			hdr.Typeflag = tar.TypeReg
		}
		if hdr.Mode == 0 {
			hdr.Mode = 0o644
			if hdr.Typeflag == tar.TypeDir {
				hdr.Mode = 0o755
			}
		}
		if hdr.Typeflag == tar.TypeDir && !strings.HasSuffix(hdr.Name, "/") {
			hdr.Name += "/"
		}
		if hdr.Typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gzipBytes(t testing.TB, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// memImage is an image of in-memory layers.
func memImage(t testing.TB, layers ...[]byte) *Image {
	img := &Image{TempDir: t.TempDir()}
	for _, l := range layers {
		img.Layers = append(img.Layers, &Layer{
			Digest: digestOf(l),
			open: func(context.Context) (io.ReadCloser, error) {
				return newVerifier(io.NopCloser(bytes.NewReader(l)), digestOf(l), "layer")
			},
		})
	}
	return img
}

// outEntry is an entry of a flattened file system.
type outEntry struct {
	typ  byte
	body string
	link string
	mode int64
}

// flatten flattens img and returns its entries, in order, checking that the
// stream is consistent: no duplicates, directories before their content,
// hard links after a regular file they name.
func flatten(t testing.TB, img *Image) (map[string]outEntry, []string, error) {
	t.Helper()
	var buf bytes.Buffer
	if err := img.Flatten(context.Background(), &buf); err != nil {
		return nil, nil, err
	}
	out := map[string]outEntry{}
	var order []string
	tr := tar.NewReader(&buf)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		name := "/" + strings.TrimSuffix(hdr.Name, "/")
		if hdr.Name == "" || strings.HasPrefix(hdr.Name, "/") || path.Clean(name) != name || name == "/" {
			t.Fatalf("bad name %q", hdr.Name)
		}
		if (hdr.Typeflag == tar.TypeDir) != strings.HasSuffix(hdr.Name, "/") {
			t.Fatalf("%q: directories, and only them, end with /", hdr.Name)
		}
		if _, dup := out[name]; dup {
			t.Fatalf("%s is in the stream twice", name)
		}
		if p := path.Dir(name); p != "/" {
			if parent, ok := out[p]; !ok || parent.typ != tar.TypeDir {
				t.Fatalf("%s before its directory %s, or below a non-directory", name, p)
			}
		}
		body, _ := io.ReadAll(tr)
		e := outEntry{typ: hdr.Typeflag, body: string(body), link: hdr.Linkname, mode: hdr.Mode}
		if hdr.Typeflag == tar.TypeLink {
			target, ok := out["/"+hdr.Linkname]
			if !ok {
				t.Fatalf("hard link %s before its target %s", name, hdr.Linkname)
			}
			if target.typ == tar.TypeLink || target.typ == tar.TypeDir {
				t.Fatalf("hard link %s to %s, of type %c", name, hdr.Linkname, target.typ)
			}
			e = target
		}
		out[name] = e
		order = append(order, name)
	}
	return out, order, nil
}

// testImage is an image built for tests, for one or several platforms.
type testImage struct {
	blobs map[string][]byte
	root  Descriptor // the index, or the manifest of a single platform
}

func (ti *testImage) add(mediaType string, b []byte) Descriptor {
	d := Descriptor{MediaType: mediaType, Digest: digestOf(b), Size: int64(len(b))}
	ti.blobs[d.Digest] = b
	return d
}

func mustJSON(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// buildImage builds an image with the given layers (uncompressed tars,
// stored gzipped) for each platform; the configuration of each sets
// PLATFORM=OS/ARCH[/VARIANT].
func buildImage(t testing.TB, platforms []Platform, layers ...[]byte) *testImage {
	t.Helper()
	ti := &testImage{blobs: map[string][]byte{}}
	var manifests []Descriptor
	for _, p := range platforms {
		var diffIDs []string
		var layerDescs []Descriptor
		for _, l := range layers {
			diffIDs = append(diffIDs, digestOf(l))
			layerDescs = append(layerDescs, ti.add("application/vnd.oci.image.layer.v1.tar+gzip", gzipBytes(t, l)))
		}
		cfg := map[string]any{
			"created":      "2024-01-02T03:04:05Z",
			"os":           p.OS,
			"architecture": p.Architecture,
			"variant":      p.Variant,
			"config":       map[string]any{"Env": []string{"PLATFORM=" + p.String()}, "Cmd": []string{"/bin/sh"}},
			"rootfs":       map[string]any{"type": "layers", "diff_ids": diffIDs},
		}
		cd := ti.add(MediaTypeOCIConfig, mustJSON(t, cfg))
		m := manifest{SchemaVersion: 2, MediaType: MediaTypeOCIManifest, Config: cd, Layers: layerDescs}
		md := ti.add(MediaTypeOCIManifest, mustJSON(t, m))
		md.Platform = &Platform{OS: p.OS, Architecture: p.Architecture, Variant: p.Variant}
		manifests = append(manifests, md)
	}
	if len(manifests) == 1 {
		ti.root = manifests[0]
		ti.root.Platform = nil
		return ti
	}
	// An attestation, as BuildKit adds, to be ignored.
	att := ti.add(MediaTypeOCIManifest, mustJSON(t, manifest{SchemaVersion: 2, MediaType: MediaTypeOCIManifest}))
	att.Platform = &Platform{OS: "unknown", Architecture: "unknown"}
	manifests = append(manifests, att)
	idx := manifest{SchemaVersion: 2, MediaType: MediaTypeOCIIndex, Manifests: manifests}
	ti.root = ti.add(MediaTypeOCIIndex, mustJSON(t, idx))
	return ti
}

// writeLayout writes the image as an OCI image layout in dir, under the
// given names (annotations of index.json).
func (ti *testImage) writeLayout(t testing.TB, dir string, names ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "sha256"), 0o755); err != nil {
		t.Fatal(err)
	}
	for d, b := range ti.blobs {
		if err := os.WriteFile(filepath.Join(dir, "blobs", "sha256", strings.TrimPrefix(d, "sha256:")), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	idx := manifest{SchemaVersion: 2, MediaType: MediaTypeOCIIndex}
	for _, n := range names {
		d := ti.root
		d.Annotations = map[string]string{annotationRefName: n}
		idx.Manifests = append(idx.Manifests, d)
	}
	if len(names) == 0 {
		idx.Manifests = []Descriptor{ti.root}
	}
	writeJSON(t, filepath.Join(dir, "index.json"), idx)
	writeJSON(t, filepath.Join(dir, "oci-layout"), map[string]string{"imageLayoutVersion": "1.0.0"})
}

func writeJSON(t testing.TB, name string, v any) {
	t.Helper()
	if err := os.WriteFile(name, mustJSON(t, v), 0o644); err != nil {
		t.Fatal(err)
	}
}

// tarFile writes a tar archive of the files (name → content; a content
// starting with "->" makes a symlink) at name.
func tarFile(t testing.TB, name string, files map[string][]byte) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, n := range slices.Sorted(func(yield func(string) bool) {
		for k := range files {
			if !yield(k) {
				return
			}
		}
	}) {
		b := files[n]
		hdr := &tar.Header{Name: n, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(b)), ModTime: testTime}
		if link, ok := strings.CutPrefix(string(b), "->"); ok {
			hdr.Typeflag, hdr.Linkname, hdr.Size = tar.TypeSymlink, link, 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write(b); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// layoutFiles returns the files of an OCI layout directory, for tarFile.
func layoutFiles(t testing.TB, dir string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		files[filepath.ToSlash(rel)] = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func envOf(img *Image) string { return fmt.Sprint(img.Config.Config.Env) }
