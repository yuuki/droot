package image

import (
	"context"
	"fmt"
	"io"
	"testing"
)

// BenchmarkFlatten measures the flattening of the layers of an image
// exported without daemon: a base layer, and layers that replace, delete
// and hard-link files of the layers below.
func BenchmarkFlatten(b *testing.B) {
	var base, update, links []tEntry
	for d := range 50 {
		pkg := fmt.Sprintf("usr/lib/pkg%d", d)
		base = append(base, dir(pkg))
		for i := range 40 {
			base = append(base, file(fmt.Sprintf("%s/file%d", pkg, i), "content of a small file\n"))
		}
		base = append(base, symlink(pkg+"/link", "file0"))
		if d%5 == 0 {
			update = append(update, file(pkg+"/file1", "updated\n"), whiteout(pkg+"/file2"))
		}
		if d%10 == 0 {
			update = append(update, opaque(pkg), file(pkg+"/new", "after the opaque whiteout\n"))
		}
		if d%10 != 0 { // the opaque whiteout deleted file3
			links = append(links, hardlink(fmt.Sprintf("usr/bin/tool%d", d), pkg+"/file3"))
		}
	}
	img := memImage(b, layerTar(b, base...), layerTar(b, update...), layerTar(b, links...))
	ctx := context.Background()

	b.ReportAllocs()
	for b.Loop() {
		if err := img.Flatten(ctx, io.Discard); err != nil {
			b.Fatal(err)
		}
	}
}
