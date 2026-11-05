package image

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os/exec"
	"path"
	"slices"
	"strings"
	"testing"
)

// summary describes the flattened file system: one line per entry, in
// order: NAME TYPE [BODY|-> LINK] [MODE].
func summary(out map[string]outEntry, order []string) string {
	var b strings.Builder
	for _, n := range order {
		e := out[n]
		b.WriteString(n + " " + string(e.typ))
		switch e.typ {
		case tar.TypeReg:
			b.WriteString(" " + e.body)
		case tar.TypeSymlink:
			b.WriteString(" -> " + e.link)
		case tar.TypeDir:
			if e.mode != 0o755 {
				b.WriteString(" " + string(rune('0'+e.mode>>6&7)) + string(rune('0'+e.mode>>3&7)) + string(rune('0'+e.mode&7)))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func TestFlatten(t *testing.T) {
	tests := []struct {
		name   string
		layers [][]tEntry
		want   string
	}{
		{
			name: "merge, later layers win",
			layers: [][]tEntry{
				{dir("etc"), file("etc/a", "1"), file("etc/b", "1")},
				{dir("etc"), file("etc/b", "2"), file("etc/c", "2")},
			},
			want: "/etc 5\n/etc/a 0 1\n/etc/b 0 2\n/etc/c 0 2\n",
		},
		{
			name: "whiteouts delete files and directories of the layers below",
			layers: [][]tEntry{
				{dir("d"), file("d/a", "1"), dir("d/sub"), file("d/sub/x", "1"), file("top", "1")},
				{whiteout("d/a"), whiteout("d/sub"), whiteout("top"), whiteout("missing/x")},
			},
			want: "/d 5\n",
		},
		{
			name: "a whiteout does not delete the files of its own layer",
			layers: [][]tEntry{
				{file("a", "1")},
				{file("a", "2"), whiteout("a"), file("b", "2"), whiteout("b")},
			},
			want: "/a 0 2\n/b 0 2\n",
		},
		{
			name: "opaque directories hide what is below them",
			layers: [][]tEntry{
				{dir("o"), file("o/old", "1"), dir("o/sub"), file("o/sub/x", "1"), file("other", "1")},
				{dir("o"), opaque("o"), file("o/new", "2")},
			},
			want: "/other 0 1\n/o 5\n/o/new 0 2\n",
		},
		{
			name: "a file replaces a directory and its content, and the reverse",
			layers: [][]tEntry{
				{dir("x"), file("x/in", "1"), file("y", "1")},
				{file("x", "2"), dir("y"), file("y/in", "2")},
			},
			want: "/x 0 2\n/y 5\n/y/in 0 2\n",
		},
		{
			name: "directories come before their content, with their last metadata",
			layers: [][]tEntry{
				{{name: "d", typ: tar.TypeDir, mode: 0o700}, file("d/a", "1")},
				{{name: "d", typ: tar.TypeDir, mode: 0o750}},
			},
			want: "/d 5 750\n/d/a 0 1\n",
		},
		{
			name: "missing parent directories are added",
			layers: [][]tEntry{
				{file("a/b/c", "1")},
			},
			want: "/a 5\n/a/b 5\n/a/b/c 0 1\n",
		},
		{
			name: "missing parent directories stay when their content is deleted",
			layers: [][]tEntry{
				{file("a/b/c", "1"), file("x/y", "1")},
				{whiteout("a/b/c"), whiteout("x")},
			},
			want: "/a 5\n/a/b 5\n",
		},
		{
			name: "the root and AUFS metadata are left out",
			layers: [][]tEntry{
				{dir("./"), file(".wh..wh.plnk/x", ""), file("./a", "1"), file("b/../c", "1")},
			},
			want: "/a 0 1\n/c 0 1\n",
		},
		{
			name: "hard links to surviving files",
			layers: [][]tEntry{
				{file("a", "1"), hardlink("b", "a")},
				{hardlink("c", "b"), symlink("s", "a"), hardlink("t", "s")},
			},
			want: "/a 0 1\n/b 0 1\n/c 0 1\n/s 2 -> a\n/t 2 -> a\n",
		},
		{
			name: "hard links to deleted or replaced files keep their content",
			layers: [][]tEntry{
				{file("a", "old-a"), file("x", "old-x"), hardlink("a2", "a")},
				{hardlink("b", "a"), hardlink("c", "a"), hardlink("y", "x")},
				{whiteout("a"), whiteout("a2"), file("x", "new-x")},
			},
			want: "/b 0 old-a\n/c 0 old-a\n/y 0 old-x\n/x 0 new-x\n",
		},
		{
			name: "nothing is kept below a symlink",
			layers: [][]tEntry{
				{dir("etc"), symlink("a", "/etc"), file("f", "1")},
				{file("a/passwd", "evil"), hardlink("g", "f"), symlink("f", "x"), file("f/y", "2")},
			},
			want: "/etc 5\n/a 2 -> /etc\n/g 0 1\n/f 2 -> x\n",
		},
		{
			name: "a hard link replaced in a later layer",
			layers: [][]tEntry{
				{file("a", "1"), hardlink("b", "a")},
				{file("b", "2")},
			},
			want: "/a 0 1\n/b 0 2\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var layers [][]byte
			for _, l := range tt.layers {
				layers = append(layers, layerTar(t, l...))
			}
			out, order, err := flatten(t, memImage(t, layers...))
			if err != nil {
				t.Fatal(err)
			}
			if got := summary(out, order); got != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestFlattenHardLinkErrors(t *testing.T) {
	for name, entries := range map[string][][]tEntry{
		"missing target":         {{hardlink("b", "a")}},
		"target after the link":  {{hardlink("b", "a"), file("a", "1")}},
		"directory target":       {{dir("d"), hardlink("b", "d")}},
		"target deleted earlier": {{file("a", "1")}, {whiteout("a")}, {hardlink("b", "a")}},
	} {
		t.Run(name, func(t *testing.T) {
			var layers [][]byte
			for _, l := range entries {
				layers = append(layers, layerTar(t, l...))
			}
			if _, _, err := flatten(t, memImage(t, layers...)); err == nil || !strings.Contains(err.Error(), "hard link") {
				t.Errorf("got %v, want an error about the hard link", err)
			}
		})
	}
}

func TestFlattenVerifiesDigests(t *testing.T) {
	l := layerTar(t, file("a", "1"))
	img := memImage(t, l)
	img.Layers[0].DiffID = digestOf([]byte("something else"))
	if _, _, err := flatten(t, img); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Errorf("got %v, want a digest mismatch", err)
	}

	// The digest of the stored layer is checked even if the tar stream
	// ends before the end of the data.
	tampered := append(slices.Clone(l), make([]byte, 512)...)
	img = memImage(t, tampered)
	img.Layers[0].open = func(context.Context) (io.ReadCloser, error) {
		return newVerifier(io.NopCloser(bytes.NewReader(tampered)), digestOf(l), "layer")
	}
	if _, _, err := flatten(t, img); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Errorf("got %v, want a digest mismatch", err)
	}
}

func TestFlattenCompressed(t *testing.T) {
	l := layerTar(t, file("a", "1"))
	img := memImage(t, gzipBytes(t, l))
	img.Layers[0].DiffID = digestOf(l)
	out, order, err := flatten(t, img)
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(out, order); got != "/a 0 1\n" {
		t.Errorf("gzip: got %q", got)
	}

	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("no zstd command")
	}
	cmd := exec.Command("zstd", "-q", "-c")
	cmd.Stdin = bytes.NewReader(l)
	z, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	img = memImage(t, z)
	img.Layers[0].DiffID = digestOf(l)
	out, order, err = flatten(t, img)
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(out, order); got != "/a 0 1\n" {
		t.Errorf("zstd: got %q", got)
	}
	img = memImage(t, z)
	img.Layers[0].DiffID = digestOf([]byte("x"))
	if _, _, err := flatten(t, img); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Errorf("zstd: got %v, want a digest mismatch", err)
	}
}

// model applies layers the simple way, as a reference for Flatten: a map
// of the paths, deleting by prefix.
type model struct {
	entries map[string]outEntry
}

func (m *model) deleteBelow(p string) {
	for k := range m.entries {
		if strings.HasPrefix(k, p+"/") {
			delete(m.entries, k)
		}
	}
}

func (m *model) apply(entries []tEntry) bool {
	var whiteouts, opaques []string
	type op struct {
		name string
		e    tEntry
	}
	var ops []op
	for _, e := range entries {
		name, wh, isOpaque, meta := parseEntry(e.name)
		switch {
		case meta:
		case isOpaque:
			opaques = append(opaques, wh)
		case wh != "":
			whiteouts = append(whiteouts, wh)
		default:
			ops = append(ops, op{name, e})
		}
	}
	for _, d := range opaques {
		if e, ok := m.entries[d]; !ok || e.typ == tar.TypeDir || e.typ == 0 {
			m.deleteBelow(d)
		}
	}
	for _, p := range whiteouts {
		delete(m.entries, p)
		m.deleteBelow(p)
	}
	for _, o := range ops {
		e := o.e
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		oe := outEntry{typ: typ, body: e.body, link: e.link, mode: e.mode}
		if oe.mode == 0 {
			oe.mode = 0o644
			if typ == tar.TypeDir {
				oe.mode = 0o755
			}
		}
		if typ == tar.TypeLink {
			target, ok := m.entries[path.Clean("/"+e.link)]
			if !ok || target.typ == tar.TypeDir || target.typ == 0 {
				return false
			}
			oe = target
		}
		if typ != tar.TypeDir {
			m.deleteBelow(o.name)
		}
		m.entries[o.name] = oe
		for d := path.Dir(o.name); d != "/"; d = path.Dir(d) {
			if _, ok := m.entries[d]; !ok {
				m.entries[d] = outEntry{} // implicit directory
			}
		}
	}
	return true
}

// genLayers makes layers from fuzz input: small paths and every kind of
// entry, so that they collide.
func genLayers(data []byte) [][]tEntry {
	next := func() int {
		if len(data) == 0 {
			return 0
		}
		b := data[0]
		data = data[1:]
		return int(b)
	}
	names := []string{"a", "b", "c"}
	genPath := func() string {
		n := next()%3 + 1
		var parts []string
		for range n {
			parts = append(parts, names[next()%len(names)])
		}
		return strings.Join(parts, "/")
	}
	var layers [][]tEntry
	for range next()%4 + 1 {
		var l []tEntry
		for range next() % 8 {
			p := genPath()
			switch next() % 8 {
			case 0, 1:
				l = append(l, file(p, string(rune('a'+next()%26))))
			case 2:
				l = append(l, tEntry{name: p, typ: tar.TypeDir, mode: []int64{0o755, 0o700, 0o750}[next()%3]})
			case 3:
				l = append(l, symlink(p, genPath()))
			case 4:
				l = append(l, hardlink(p, genPath()))
			case 5:
				l = append(l, whiteout(p))
			case 6:
				l = append(l, opaque(p))
			case 7:
				l = append(l, dir("./"))
			}
		}
		layers = append(layers, l)
	}
	return layers
}

func checkAgainstModel(t *testing.T, layers [][]tEntry) {
	m := &model{entries: map[string]outEntry{}}
	ok := true
	var tars [][]byte
	for _, l := range layers {
		if ok {
			ok = m.apply(l)
		}
		tars = append(tars, layerTar(t, l...))
	}
	out, _, err := flatten(t, memImage(t, tars...))
	// Nothing is kept below a non-directory.
	for name := range m.entries {
		for d := path.Dir(name); d != "/"; d = path.Dir(d) {
			if e, ok := m.entries[d]; ok && e.typ != tar.TypeDir && e.typ != 0 {
				delete(m.entries, name)
				break
			}
		}
	}
	if !ok {
		if err == nil {
			t.Fatalf("Flatten accepted an invalid hard link: %v", layers)
		}
		return
	}
	if err != nil {
		t.Fatalf("Flatten: %v (layers %v)", err, layers)
	}
	for name, want := range m.entries {
		got, found := out[name]
		if !found {
			t.Fatalf("%s is missing (layers %v)", name, layers)
		}
		if want.typ == 0 { // implicit directory
			if got.typ != tar.TypeDir && got.typ != 0 {
				t.Fatalf("%s: got %+v, want a directory (layers %v)", name, got, layers)
			}
			continue
		}
		if got != want {
			t.Fatalf("%s: got %+v, want %+v (layers %v)", name, got, want, layers)
		}
	}
	for name := range out {
		if _, ok := m.entries[name]; !ok {
			t.Fatalf("%s should not be there (layers %v)", name, layers)
		}
	}
}

func FuzzFlatten(f *testing.F) {
	f.Add([]byte{1, 3, 0, 0, 0, 1, 1, 0, 4, 1, 1, 2, 1, 5, 0, 1, 0})
	f.Add([]byte{2, 4, 0, 1, 0, 0, 4, 0, 1, 1, 0, 0, 3, 0, 1, 5, 0, 0, 0, 2, 1, 0, 0})
	f.Add([]byte{3, 5, 1, 0, 2, 0, 0, 6, 1, 0, 0, 2, 0, 0, 0, 2, 5, 0, 0, 7, 0, 1, 4})
	f.Fuzz(func(t *testing.T, data []byte) {
		checkAgainstModel(t, genLayers(data))
	})
}

func TestFlattenAgainstModel(t *testing.T) {
	// Deterministic pseudo-random inputs, for runs without -fuzz.
	state := uint32(1)
	for range 3000 {
		data := make([]byte, 64)
		for i := range data {
			state = state*1664525 + 1013904223
			data[i] = byte(state >> 24)
		}
		checkAgainstModel(t, genLayers(data))
	}
}
