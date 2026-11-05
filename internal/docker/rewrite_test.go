package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"slices"
	"testing"
	"time"
)

// rewriteArchive rewrites r with the default options and the given files.
func rewriteArchive(r io.Reader, w io.Writer, files []extraFile) error {
	return (&rewriter{files: files, modTime: time.Now().Truncate(time.Second)}).rewrite(r, w)
}

func TestExcluded(t *testing.T) {
	patterns := []string{"/usr/share/doc", "*.pyc", "var/cache/apk/*", "/etc/ssl/certs/[a-c]*"}
	for name, want := range map[string]bool{
		"usr/share/doc":             true,
		"usr/share/doc/":            true,
		"./usr/share/doc/x/y":       true,
		"usr/share/docs":            false,
		"usr/share":                 false,
		"app/x.pyc":                 true,
		"x.pyc":                     true,
		"app/x.pyc/y":               true, // below a matching directory
		"app/x.py":                  false,
		"var/cache/apk":             false,
		"var/cache/apk/APKINDEX.gz": true,
		"etc/ssl/certs/ca.pem":      true,
		"etc/ssl/certs/d.pem":       false,
		"/usr/share/doc/README":     true,
		"usr/share/doc-base/x":      false,
		"home/user/usr/share/doc/x": false,
	} {
		if got := Excluded(name, patterns); got != want {
			t.Errorf("Excluded(%q) = %v, want %v", name, got, want)
		}
	}
	if err := ValidatePatterns([]string{"[", ""}); err == nil {
		t.Error("invalid patterns must be refused")
	}
}

func TestExportImageOptions(t *testing.T) {
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	created := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 11, 5, 1, 2, 3, 456, time.UTC)
	entries := []entry{
		{hdr: tar.Header{Typeflag: tar.TypeReg, Name: ".dockerenv", Mode: 0o755, ModTime: newer}},
		{hdr: tar.Header{Typeflag: tar.TypeDir, Name: "etc/", Mode: 0o755, ModTime: newer, AccessTime: newer, ChangeTime: newer, Format: tar.FormatPAX}},
		{hdr: tar.Header{Typeflag: tar.TypeReg, Name: "etc/hosts", Mode: 0o644, ModTime: newer}},
		{hdr: tar.Header{Typeflag: tar.TypeReg, Name: "etc/hostname", Mode: 0o644, ModTime: newer}, body: "kept: not empty\n"},
		{hdr: tar.Header{Typeflag: tar.TypeReg, Name: "etc/app.conf", Mode: 0o644, ModTime: old}, body: "x=1\n"},
		{hdr: tar.Header{Typeflag: tar.TypeDir, Name: "usr/share/doc/", Mode: 0o755, ModTime: old}},
		{hdr: tar.Header{Typeflag: tar.TypeReg, Name: "usr/share/doc/README", Mode: 0o644, ModTime: old}, body: "doc"},
		{hdr: tar.Header{Typeflag: tar.TypeLink, Name: "usr/bin/readme", Linkname: "usr/share/doc/README", ModTime: old}},
		{hdr: tar.Header{Typeflag: tar.TypeReg, Name: "app/x.pyc", Mode: 0o644, ModTime: old}, body: "pyc"},
	}
	export := func(opts ExportOptions) []entry {
		t.Helper()
		api := &fakeAPI{archive: buildTar(t, entries)}
		var out bytes.Buffer
		if err := ExportImage(context.Background(), api, "img", &out, opts); err != nil {
			t.Fatal(err)
		}
		return readTar(t, out.Bytes())
	}
	names := func(es []entry) []string {
		var n []string
		for _, e := range es {
			n = append(n, e.hdr.Name)
		}
		return n
	}

	got := export(ExportOptions{Exclude: []string{"/usr/share/doc", "*.pyc"}, DropDockerFiles: true})
	want := []string{"etc/", "etc/hostname", "etc/app.conf", ".drootenv", ".droot/", ".droot/config.json"}
	if !slices.Equal(names(got), want) {
		t.Fatalf("got %q, want %q", names(got), want)
	}

	// Reproducible: times clamped to the image's creation, appended files
	// dated from it, and the same archive twice.
	reproducible := ExportOptions{Reproducible: true, ClampTime: created}
	got = export(reproducible)
	for _, e := range got {
		mt := e.hdr.ModTime
		if mt.After(created) || !e.hdr.AccessTime.IsZero() || !e.hdr.ChangeTime.IsZero() {
			t.Errorf("%s: mtime %v atime %v ctime %v", e.hdr.Name, mt, e.hdr.AccessTime, e.hdr.ChangeTime)
		}
		if e.hdr.Name == "etc/app.conf" && !mt.Equal(old) {
			t.Errorf("older modification times must be kept, got %v", mt)
		}
	}
	if !got[len(got)-1].hdr.ModTime.Equal(created) {
		t.Errorf("appended files must be dated from the clamp time, got %v", got[len(got)-1].hdr.ModTime)
	}
	api1, api2 := &fakeAPI{archive: buildTar(t, entries)}, &fakeAPI{archive: buildTar(t, entries)}
	var out1, out2 bytes.Buffer
	if err := ExportImage(context.Background(), api1, "img", &out1, reproducible); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := ExportImage(context.Background(), api2, "img", &out2, reproducible); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out1.Bytes(), out2.Bytes()) {
		t.Error("two reproducible exports of the same image differ")
	}

	// Without ClampTime, the image's creation time is used.
	api := &fakeAPI{archive: buildTar(t, entries), imageCreated: created.Format(time.RFC3339Nano)}
	var out bytes.Buffer
	if err := ExportImage(context.Background(), api, "img", &out, ExportOptions{Reproducible: true}); err != nil {
		t.Fatal(err)
	}
	for _, e := range readTar(t, out.Bytes()) {
		if e.hdr.ModTime.After(created) {
			t.Errorf("%s: %v is after the image's creation", e.hdr.Name, e.hdr.ModTime)
		}
	}
	api = &fakeAPI{archive: buildTar(t, entries)}
	if err := ExportImage(context.Background(), api, "img", io.Discard, ExportOptions{Reproducible: true}); err == nil {
		t.Error("an image without creation time needs SOURCE_DATE_EPOCH")
	}
}
