package osutil

import (
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkRootOpenFile measures the resolution of a path inside a container
// root, which opens every component with openat(2) and follows symlinks
// itself: here a path through a symlink of a directory (/lib -> usr/lib),
// as the files droot reads from the container (/etc/passwd, ...) often are.
func BenchmarkRootOpenFile(b *testing.B) {
	dir := b.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "usr", "lib", "a", "b", "c"), 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "usr", "lib", "a", "b", "c", "file"), []byte("x"), 0o644); err != nil {
		b.Fatal(err)
	}
	if err := os.Symlink("usr/lib", filepath.Join(dir, "lib")); err != nil {
		b.Fatal(err)
	}
	root, err := OpenRoot(dir)
	if err != nil {
		b.Fatal(err)
	}
	defer root.Close()

	b.ReportAllocs()
	for b.Loop() {
		f, err := root.OpenFile("/lib/a/b/c/file")
		if err != nil {
			b.Fatal(err)
		}
		_ = f.Close()
	}
}
