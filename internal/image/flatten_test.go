package image

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
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
			name: "paths through symlinks lead where they point to, as in a chroot",
			layers: [][]tEntry{
				{dir("etc"), symlink("a", "/etc"), dir("usr"), dir("usr/lib"), symlink("lib", "usr/lib"), file("usr/lib/old", "1")},
				{file("a/passwd", "p"), file("lib/libfoo", "so"), whiteout("lib/old"), hardlink("h", "lib/libfoo"), file("lib/../x", "x")},
			},
			want: "/etc 5\n/a 2 -> /etc\n/usr 5\n/usr/lib 5\n/lib 2 -> usr/lib\n/etc/passwd 0 p\n/usr/lib/libfoo 0 so\n/h 0 so\n/x 0 x\n",
		},
		{
			name: "a directory replaces a symlink",
			layers: [][]tEntry{
				{dir("etc"), symlink("a", "../etc")},
				{file("a/passwd", "p")},
				{dir("a")},
			},
			want: "/etc 5\n/etc/passwd 0 p\n/a 5\n",
		},
		{
			name: "opaque directories and whiteouts through symlinks",
			layers: [][]tEntry{
				{dir("d"), file("d/x", "1"), file("d/y", "1"), symlink("s", "d"), dir("e"), file("e/z", "1"), symlink("t", "/e")},
				{whiteout("s/x"), dir("t"), opaque("t")},
			},
			want: "/d 5\n/d/y 0 1\n/s 2 -> d\n/e 5\n/t 5\n",
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

func TestFlattenPathErrors(t *testing.T) {
	deep := strings.Repeat("d/", maxPathLen/2) + "f"
	for name, tt := range map[string]struct {
		layers [][]tEntry
		want   string
	}{
		"through a file":             {[][]tEntry{{file("f", "1"), file("f/x", "1")}}, "not a directory"},
		"through a dangling symlink": {[][]tEntry{{symlink("s", "nowhere"), file("s/x", "1")}}, "not a directory"},
		"through a symlink loop":     {[][]tEntry{{symlink("a", "b"), symlink("b", "a"), file("a/x", "1")}}, "too many levels"},
		"too long":                   {[][]tEntry{{file(deep, "1")}}, "longer than 4096"},
		"too long a component":       {[][]tEntry{{file(strings.Repeat("x", 256), "1")}}, "longer than 255"},
	} {
		t.Run(name, func(t *testing.T) {
			var layers [][]byte
			for _, l := range tt.layers {
				layers = append(layers, layerTar(t, l...))
			}
			if _, _, err := flatten(t, memImage(t, layers...)); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("got %v, want %q", err, tt.want)
			}
		})
	}
}

func TestFlattenSkipsNonFiles(t *testing.T) {
	// A PAX global header: a tar extractor would apply its records to
	// every following entry.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "etc", PAXRecords: map[string]string{"uid": "1000", "comment": "x"}})
	_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "etc/passwd", Size: 1, Mode: 0o644, ModTime: testTime})
	_, _ = tw.Write([]byte("p"))
	_ = tw.Close()
	img := memImage(t, layerTar(t, dir("etc")), buf.Bytes())
	var out bytes.Buffer
	if err := img.Flatten(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&out)
	var names []string
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader || hdr.Uid != 0 {
			t.Errorf("%s: %c uid %d", hdr.Name, hdr.Typeflag, hdr.Uid)
		}
		names = append(names, hdr.Name)
	}
	if strings.Join(names, " ") != "etc/ etc/passwd" {
		t.Errorf("got %q", names)
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

// resolveDir resolves the symlinks of the directory p in the model, as in a
// chroot: an independent implementation of flattener.resolveDir. Followed
// symlinks must lead to directories; p must be one if it exists.
func (m *model) resolveDir(p string, hops *int) (string, bool, error) {
	isDir := func(name string) bool {
		e, ok := m.entries[name]
		return name == "/" || ok && (e.typ == tar.TypeDir || e.typ == 0)
	}
	cur := "/"
	for _, c := range strings.Split(strings.Trim(p, "/"), "/") {
		if c == "" || c == "." {
			continue
		}
		if c == ".." {
			cur = path.Dir(cur)
			continue
		}
		if _, ok := m.entries[cur]; ok && !isDir(cur) {
			return "", false, errNotDir
		}
		next := path.Join(cur, c)
		if e, ok := m.entries[next]; ok && e.typ == tar.TypeSymlink {
			if *hops++; *hops > maxSymlinks {
				return "", false, errors.New("loop")
			}
			target := e.link
			if !path.IsAbs(target) {
				target = path.Join(cur, target)
			}
			tp, exists, err := m.resolveDir(target, hops)
			if err != nil {
				return "", false, err
			}
			if !exists {
				return "", false, errNotDir
			}
			cur = tp
			continue
		}
		cur = next
	}
	_, exists := m.entries[cur]
	if exists && !isDir(cur) {
		return "", false, errNotDir
	}
	return cur, exists || cur == "/", nil
}

// resolveParent resolves the parent directory of p.
func (m *model) resolveParent(p string) (string, error) {
	hops := 0
	d, _, err := m.resolveDir(path.Dir(p), &hops)
	return path.Join(d, path.Base(p)), err
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
		hops := 0
		if r, exists, err := m.resolveDir(d, &hops); err == nil && exists {
			m.deleteBelow(r)
		}
	}
	for _, p := range whiteouts {
		if r, err := m.resolveParent(p); err == nil {
			delete(m.entries, r)
			m.deleteBelow(r)
		}
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
		name, err := m.resolveParent(o.name)
		if err != nil {
			return false
		}
		if typ == tar.TypeLink {
			t, err := m.resolveParent(path.Clean("/" + e.link))
			if err != nil {
				return false
			}
			target, ok := m.entries[t]
			if !ok || target.typ == tar.TypeDir || target.typ == 0 {
				return false
			}
			oe = target
		}
		if typ != tar.TypeDir {
			m.deleteBelow(name)
		}
		m.entries[name] = oe
		for d := path.Dir(name); d != "/"; d = path.Dir(d) {
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
				l = append(l, symlink(p, []string{"", "/", "../"}[next()%3]+genPath()))
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
