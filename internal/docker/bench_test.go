package docker

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"testing"
)

// BenchmarkRewrite measures the rewriting of the tar stream that every
// `droot export` goes through, on the file system of a small image: many
// small files and a few large ones.
func BenchmarkRewrite(b *testing.B) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	write := func(hdr *tar.Header, body []byte) {
		hdr.Size, hdr.ModTime = int64(len(body)), mtime
		if err := tw.WriteHeader(hdr); err != nil {
			b.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			b.Fatal(err)
		}
	}
	small, large := bytes.Repeat([]byte("x"), 1<<10), bytes.Repeat([]byte("y"), 4<<20)
	for d := range 50 {
		dir := fmt.Sprintf("usr/lib/pkg%d/", d)
		write(&tar.Header{Typeflag: tar.TypeDir, Name: dir, Mode: 0o755}, nil)
		for i := range 40 {
			write(&tar.Header{Typeflag: tar.TypeReg, Name: fmt.Sprintf("%sfile%d", dir, i), Mode: 0o644}, small)
		}
		write(&tar.Header{Typeflag: tar.TypeSymlink, Name: dir + "link", Linkname: "file0", Mode: 0o777}, nil)
	}
	for i := range 4 {
		write(&tar.Header{Typeflag: tar.TypeReg, Name: fmt.Sprintf("usr/bin/large%d", i), Mode: 0o755}, large)
	}
	if err := tw.Close(); err != nil {
		b.Fatal(err)
	}
	archive := buf.Bytes()
	files := fuzzFiles([]byte("PATH=/usr/local/bin:/usr/bin:/bin\n"))

	b.SetBytes(int64(len(archive)))
	b.ReportAllocs()
	for b.Loop() {
		if err := fuzzRewrite(archive, io.Discard, files); err != nil {
			b.Fatal(err)
		}
	}
}
