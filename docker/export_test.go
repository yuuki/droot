package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type fakeAPI struct {
	onCreate   func() // called during ContainerCreate
	env        []string
	archive    []byte
	createErr  error
	exportErr  error
	removeErr  error
	exportBody io.ReadCloser

	created  *ContainerConfig
	platform string
	removed  []string
}

const fakeID = "0123456789abcdef0123456789abcdef"

func (f *fakeAPI) ContainerCreate(ctx context.Context, cfg *ContainerConfig, platform string) (string, error) {
	if f.onCreate != nil {
		f.onCreate()
	}
	if err := ctx.Err(); err != nil {
		// Like an HTTP request aborted after the daemon received it: the
		// container may exist, but its ID is lost.
		return "", err
	}
	if f.createErr != nil {
		return "", f.createErr
	}
	f.created, f.platform = cfg, platform
	return fakeID, nil
}

func (f *fakeAPI) ContainerInspect(_ context.Context, id string) (*ContainerInfo, error) {
	info := &ContainerInfo{ID: id}
	info.Config.Env = f.env
	return info, nil
}

func (f *fakeAPI) ContainerExport(_ context.Context, _ string) (io.ReadCloser, error) {
	if f.exportErr != nil {
		return nil, f.exportErr
	}
	if f.exportBody != nil {
		return f.exportBody, nil
	}
	return io.NopCloser(bytes.NewReader(f.archive)), nil
}

func (f *fakeAPI) ContainerRemove(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err // cleanup must not use a cancelled context
	}
	f.removed = append(f.removed, id)
	return f.removeErr
}

type entry struct {
	hdr  tar.Header
	body string
}

var mtime = time.Date(2020, 12, 13, 1, 2, 3, 0, time.UTC)

func sampleEntries() []entry {
	long := "usr/share/" + strings.Repeat("very-long-directory-name/", 6) + "file.txt"
	return []entry{
		{hdr: tar.Header{Typeflag: tar.TypeDir, Name: "etc/", Mode: 0o755, ModTime: mtime}},
		{hdr: tar.Header{Typeflag: tar.TypeReg, Name: "etc/hostname", Mode: 0o644, ModTime: mtime}, body: "droot\n"},
		{hdr: tar.Header{Typeflag: tar.TypeSymlink, Name: "bin", Linkname: "usr/bin", Mode: 0o777, ModTime: mtime}},
		{hdr: tar.Header{Typeflag: tar.TypeLink, Name: "etc/hostname.hard", Linkname: "etc/hostname", ModTime: mtime}},
		{hdr: tar.Header{Typeflag: tar.TypeChar, Name: "dev/null", Mode: 0o666, Devmajor: 1, Devminor: 3, ModTime: mtime}},
		{hdr: tar.Header{Typeflag: tar.TypeReg, Name: long, Mode: 0o600, Uid: 1000, Gid: 1000, Uname: "app", Gname: "app", ModTime: mtime}, body: "long"},
		{hdr: tar.Header{
			Typeflag: tar.TypeReg, Name: "usr/bin/ping", Mode: 0o4755, ModTime: mtime,
			PAXRecords: map[string]string{"SCHILY.xattr.security.capability": "\x01\x00\x00\x02"},
		}, body: "ELF"},
		{hdr: tar.Header{Typeflag: tar.TypeReg, Name: ".drootenv", Mode: 0o644, ModTime: mtime}, body: "STALE=1\n"},
	}
}

func buildTar(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := e.hdr
		hdr.Size = int64(len(e.body))
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readTar(t *testing.T, b []byte) []entry {
	t.Helper()
	var out []entry
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, entry{hdr: *hdr, body: string(body)})
	}
}

func TestExportImage(t *testing.T) {
	entries := sampleEntries()
	api := &fakeAPI{
		env:     []string{"PATH=/usr/local/bin:/usr/bin:/bin", "JAVA_OPTS=-Da=b -Dc=d", "QUOTE=\"$HOME\" `x`"},
		archive: buildTar(t, entries),
	}

	var out bytes.Buffer
	if err := ExportImage(context.Background(), api, "example/app:latest", &out, ExportOptions{Platform: "linux/arm64"}); err != nil {
		t.Fatalf("ExportImage: %v", err)
	}

	if api.created == nil || api.created.Image != "example/app:latest" || len(api.created.Entrypoint) == 0 {
		t.Fatalf("unexpected container config: %+v", api.created)
	}
	if api.platform != "linux/arm64" {
		t.Errorf("platform = %q", api.platform)
	}
	if len(api.removed) != 1 || api.removed[0] != fakeID {
		t.Errorf("container should be removed exactly once, got %v", api.removed)
	}

	got := readTar(t, out.Bytes())
	want := entries[:len(entries)-1] // the stale .drootenv is replaced
	if len(got) != len(want)+1 {
		t.Fatalf("got %d entries, want %d", len(got), len(want)+1)
	}
	for i, w := range want {
		g := got[i]
		if g.hdr.Name != w.hdr.Name || g.hdr.Typeflag != w.hdr.Typeflag || g.hdr.Linkname != w.hdr.Linkname ||
			g.hdr.Mode != w.hdr.Mode || g.hdr.Uid != w.hdr.Uid || g.hdr.Gid != w.hdr.Gid ||
			g.hdr.Uname != w.hdr.Uname || g.hdr.Devmajor != w.hdr.Devmajor || g.hdr.Devminor != w.hdr.Devminor ||
			!g.hdr.ModTime.Equal(w.hdr.ModTime) || g.body != w.body {
			t.Errorf("entry %d differs:\n got  %+v %q\n want %+v %q", i, g.hdr, g.body, w.hdr, w.body)
		}
		for k, v := range w.hdr.PAXRecords {
			if g.hdr.PAXRecords[k] != v {
				t.Errorf("entry %s lost PAX record %s", w.hdr.Name, k)
			}
		}
	}

	env := got[len(got)-1]
	if env.hdr.Name != ".drootenv" || env.hdr.Typeflag != tar.TypeReg || env.hdr.Mode != 0o644 {
		t.Fatalf("unexpected env entry header: %+v", env.hdr)
	}
	wantEnv := "PATH=/usr/local/bin:/usr/bin:/bin\nJAVA_OPTS=-Da=b -Dc=d\nQUOTE=\"$HOME\" `x`\n"
	if env.body != wantEnv {
		t.Errorf(".drootenv = %q, want %q", env.body, wantEnv)
	}
}

func TestExportImageSkipsMultilineEnv(t *testing.T) {
	api := &fakeAPI{env: []string{"A=1", "CERT=line1\nline2", "B=2"}, archive: buildTar(t, nil)}
	var out bytes.Buffer
	if err := ExportImage(context.Background(), api, "img", &out, ExportOptions{}); err != nil {
		t.Fatal(err)
	}
	got := readTar(t, out.Bytes())
	if len(got) != 1 || got[0].body != "A=1\nB=2\n" {
		t.Fatalf("unexpected archive: %+v", got)
	}
}

func TestExportImageNotFound(t *testing.T) {
	api := &fakeAPI{createErr: &APIError{StatusCode: http.StatusNotFound, Message: "No such image: img:latest"}}
	err := ExportImage(context.Background(), api, "img", io.Discard, ExportOptions{})
	if err == nil || !IsNotFound(err) || !strings.Contains(err.Error(), "docker pull img") {
		t.Fatalf("want a helpful not-found error, got %v", err)
	}
	if len(api.removed) != 0 {
		t.Fatalf("nothing should be removed when creation failed")
	}
}

func TestExportImageRemovesContainerOnError(t *testing.T) {
	api := &fakeAPI{exportErr: errors.New("boom")}
	err := ExportImage(context.Background(), api, "img", io.Discard, ExportOptions{})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want export error, got %v", err)
	}
	if len(api.removed) != 1 {
		t.Fatalf("container should be removed on failure, got %v", api.removed)
	}
}

func TestExportImageCorruptArchive(t *testing.T) {
	api := &fakeAPI{archive: []byte("this is not a tar archive, but long enough to look like a header block..." + strings.Repeat("x", 600))}
	if err := ExportImage(context.Background(), api, "img", io.Discard, ExportOptions{}); err == nil {
		t.Fatal("want error for a corrupt archive")
	}
	if len(api.removed) != 1 {
		t.Fatalf("container should be removed on failure, got %v", api.removed)
	}
}

type cancelReader struct{ cancel context.CancelFunc }

func (r cancelReader) Read([]byte) (int, error) {
	r.cancel()
	return 0, errors.New("connection closed")
}
func (cancelReader) Close() error { return nil }

func TestExportImageCancelledStillCleansUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := &fakeAPI{exportBody: cancelReader{cancel: cancel}}
	err := ExportImage(ctx, api, "img", io.Discard, ExportOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if len(api.removed) != 1 {
		t.Fatalf("container should be removed after cancellation, got %v", api.removed)
	}
}

func TestExportImageCancelledDuringCreate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := &fakeAPI{archive: buildTar(t, nil), onCreate: cancel} // Ctrl-C while creating
	err := ExportImage(ctx, api, "img", io.Discard, ExportOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if len(api.removed) != 1 || api.removed[0] != fakeID {
		t.Fatalf("the container created during the cancellation must be removed, got %v", api.removed)
	}
}

func TestExportImageRemoveFailureIsNotFatal(t *testing.T) {
	api := &fakeAPI{archive: buildTar(t, nil), removeErr: errors.New("daemon busy")}
	if err := ExportImage(context.Background(), api, "img", io.Discard, ExportOptions{}); err != nil {
		t.Fatalf("a cleanup failure after a successful export should only warn, got %v", err)
	}
}

func TestExportImageDetectsTruncation(t *testing.T) {
	full := buildTar(t, sampleEntries())
	// Cut the stream cleanly after the first entry (a directory: one header
	// block), i.e. at an entry boundary, without the end-of-archive marker.
	for name, body := range map[string][]byte{
		"cut at an entry boundary": full[:512],
		"empty body":               nil,
	} {
		t.Run(name, func(t *testing.T) {
			api := &fakeAPI{archive: body}
			err := ExportImage(context.Background(), api, "img", io.Discard, ExportOptions{})
			if !errors.Is(err, errTruncated) {
				t.Fatalf("want errTruncated, got %v", err)
			}
		})
	}
}

func TestIsEnvFile(t *testing.T) {
	for name, want := range map[string]bool{
		".drootenv": true, "./.drootenv": true, "/.drootenv": true,
		"etc/.drootenv": false, ".drootenv.bak": false, ".drootenv/x": false,
	} {
		if got := isEnvFile(name); got != want {
			t.Errorf("isEnvFile(%q) = %v, want %v", name, got, want)
		}
	}
}
