package docker

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/yuuki/droot/internal/environ"
	"github.com/yuuki/droot/internal/imageconfig"
	"github.com/yuuki/droot/internal/log"
)

// Kinds of the entries of fuzzArchive.
const (
	fuzzReg = iota
	fuzzDir
	fuzzSymlink
	fuzzLink
	fuzzFifo
	fuzzKinds
)

// fuzzNames are the names that a name of one byte stands for in
// fuzzArchive, so that the fuzzer often hits the paths droot replaces.
var fuzzNames = []string{
	".drootenv", "./.drootenv", "/.drootenv", ".drootenv/", ".drootenv/child", ".drootenv/sub/",
	"etc/../.drootenv", ".droot", ".droot/", ".droot/config.json", "./.droot/other",
	"etc/.drootenv", ".drootenv.bak", "etc", "etc/hostname",
}

type fuzzEntry struct {
	hdr  tar.Header
	body []byte
}

type fuzzSpec struct {
	kind      byte
	name, arg string
}

// encodeFuzzSpec encodes entries in the format decoded by fuzzArchive.
func encodeFuzzSpec(specs ...fuzzSpec) []byte {
	var b []byte
	for _, s := range specs {
		b = append(b, s.kind, byte(len(s.name)))
		b = append(b, s.name...)
		b = append(b, byte(len(s.arg)))
		b = append(b, s.arg...)
	}
	return b
}

// fuzzArchive decodes tar entries from spec and writes them as an archive.
// Each entry is a kind byte followed by two strings, a name and an argument
// (the content of a regular file or the target of a link), each prefixed
// with its length on one byte. The argument of a hard link, if it is one
// byte long, selects an earlier regular file, symlink or FIFO, like the
// links written by Docker, which point to the first name of a file. Entries
// that archive/tar refuses to write (e.g. with NUL in their name) are left
// out.
func fuzzArchive(spec []byte) ([]fuzzEntry, []byte) {
	next := func() string {
		if len(spec) == 0 {
			return ""
		}
		n := min(int(spec[0]), len(spec)-1)
		s := string(spec[1 : 1+n])
		spec = spec[1+n:]
		return s
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	var entries []fuzzEntry
	var linkable []string
	for len(spec) > 0 && len(entries) < 64 {
		kind := spec[0] % fuzzKinds
		spec = spec[1:]
		name, arg := next(), next()
		if len(name) == 1 && int(name[0]) < len(fuzzNames) {
			name = fuzzNames[name[0]]
		}
		if kind != fuzzDir {
			// Like Docker, only directories have a trailing slash.
			name = strings.TrimRight(name, "/")
		}
		e := fuzzEntry{hdr: tar.Header{Name: name, Mode: 0o644, ModTime: mtime}}
		switch kind {
		case fuzzReg:
			e.hdr.Typeflag = tar.TypeReg
			e.body = []byte(arg)
		case fuzzDir:
			e.hdr.Typeflag, e.hdr.Mode = tar.TypeDir, 0o755
		case fuzzSymlink:
			e.hdr.Typeflag, e.hdr.Linkname, e.hdr.Mode = tar.TypeSymlink, arg, 0o777
		case fuzzLink:
			e.hdr.Typeflag, e.hdr.Linkname = tar.TypeLink, arg
			if len(arg) == 1 && len(linkable) > 0 {
				e.hdr.Linkname = linkable[int(arg[0])%len(linkable)]
			}
		case fuzzFifo:
			e.hdr.Typeflag = tar.TypeFifo
		}
		e.hdr.Size = int64(len(e.body))
		if err := tw.WriteHeader(&e.hdr); err != nil {
			continue // not fatal: the writer stays usable
		}
		if _, err := tw.Write(e.body); err != nil {
			panic(err)
		}
		entries = append(entries, e)
		if kind == fuzzReg || kind == fuzzSymlink || kind == fuzzFifo {
			linkable = append(linkable, name)
		}
	}
	if err := tw.Close(); err != nil {
		panic(err)
	}
	return entries, buf.Bytes()
}

// readFuzzTar reads all the entries of an archive.
func readFuzzTar(b []byte) ([]fuzzEntry, error) {
	var entries []fuzzEntry
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		if err != nil {
			return entries, err
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return entries, err
		}
		entries = append(entries, fuzzEntry{hdr: *hdr, body: body})
	}
}

func cleanName(name string) string { return path.Clean("/" + name) }

// lastEntry returns the last of entries named name (cleaned), if any.
func lastEntry(entries []fuzzEntry, name string, typeflag ...byte) (fuzzEntry, bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if cleanName(e.hdr.Name) == name && (len(typeflag) == 0 || slices.Contains(typeflag, e.hdr.Typeflag)) {
			return e, true
		}
	}
	return fuzzEntry{}, false
}

// checkRewritten checks the output of fuzzRewrite for the complete input
// archive made of in: the entries of in that are not replaced, in order and
// unchanged except for hard links to dropped regular files, which become
// copies of them, then files, each after its parent directories.
func checkRewritten(t *testing.T, in []fuzzEntry, out []byte, files []extraFile) {
	t.Helper()
	got, err := readFuzzTar(out)
	if err != nil {
		t.Fatalf("the output is not a valid archive: %v", err)
	}
	if !bytes.HasSuffix(out, make([]byte, 1024)) {
		t.Fatal("the output does not end with an end-of-archive marker")
	}
	envFiles := 0
	for _, g := range got {
		name := cleanName(g.hdr.Name)
		if name == environ.EnvFilePath {
			envFiles++
		}
		if strings.HasPrefix(name, environ.EnvFilePath+"/") {
			t.Fatalf("entry %q is below %s", g.hdr.Name, environ.EnvFilePath)
		}
	}
	if envFiles != 1 {
		t.Fatalf("the output has %d %s entries", envFiles, environ.EnvFilePath)
	}

	n := 0 // entries of got checked
	for i, e := range in {
		if isReplaced(e.hdr.Name) {
			continue
		}
		if n == len(got) {
			t.Fatalf("entry %q is missing from the output", e.hdr.Name)
		}
		g := got[n]
		n++
		target := cleanName(e.hdr.Linkname)
		// The last dropped entry the hard link e points to, if any.
		var orig fuzzEntry
		linkToDropped := false
		if e.hdr.Typeflag == tar.TypeLink && isReplaced(target) {
			orig, linkToDropped = lastEntry(in[:i], target)
			linkToDropped = linkToDropped && orig.hdr.Typeflag != tar.TypeDir
		}
		if linkToDropped {
			// It becomes a copy of the dropped entry: no trailing slash.
			e.hdr.Name = strings.TrimSuffix(e.hdr.Name, "/")
		}
		if g.hdr.Name != e.hdr.Name || g.hdr.Mode != e.hdr.Mode && !linkToDropped {
			t.Fatalf("output entry %d is %q (mode %o), want %q (mode %o)", n-1, g.hdr.Name, g.hdr.Mode, e.hdr.Name, e.hdr.Mode)
		}
		switch {
		case linkToDropped && orig.hdr.Typeflag == tar.TypeReg:
			// A hard link to a dropped regular file becomes a copy of it.
			if g.hdr.Typeflag != tar.TypeReg || !bytes.Equal(g.body, orig.body) || g.hdr.Linkname != "" || g.hdr.Mode != e.hdr.Mode {
				t.Fatalf("hard link %q -> %q: got type %q with %q, want a regular file with %q", e.hdr.Name, e.hdr.Linkname, g.hdr.Typeflag, g.body, orig.body)
			}
		case linkToDropped:
			// A hard link to a dropped symlink, FIFO, device or hard link
			// becomes a copy of it.
			if g.hdr.Typeflag != orig.hdr.Typeflag || g.hdr.Linkname != orig.hdr.Linkname || g.hdr.Mode != orig.hdr.Mode || len(g.body) != 0 {
				t.Fatalf("hard link %q -> %q: got type %q -> %q, want a copy of type %q -> %q", e.hdr.Name, e.hdr.Linkname,
					g.hdr.Typeflag, g.hdr.Linkname, orig.hdr.Typeflag, orig.hdr.Linkname)
			}
		case g.hdr.Typeflag != e.hdr.Typeflag || g.hdr.Linkname != e.hdr.Linkname || !bytes.Equal(g.body, e.body):
			t.Fatalf("entry %q changed:\n got  type %q -> %q %q\n want type %q -> %q %q", e.hdr.Name,
				g.hdr.Typeflag, g.hdr.Linkname, g.body, e.hdr.Typeflag, e.hdr.Linkname, e.body)
		case g.hdr.Typeflag == tar.TypeLink:
			// Extracting a hard link needs its target: when the input
			// had it before (as the first name of a file, not as a
			// directory or another hard link), so must the output.
			prev, ok := lastEntry(in[:i], target)
			if ok && prev.hdr.Typeflag != tar.TypeDir && prev.hdr.Typeflag != tar.TypeLink {
				if _, ok := lastEntry(got[:n-1], target); !ok {
					t.Fatalf("hard link %q -> %q: its target (a %q entry in the input) is not before it in the output",
						g.hdr.Name, g.hdr.Linkname, prev.hdr.Typeflag)
				}
			}
		}
	}

	// Then the files droot adds, each after its parent directories.
	dirs := map[string]bool{}
	var added []fuzzEntry
	for _, g := range got[n:] {
		name := cleanName(g.hdr.Name)
		switch g.hdr.Typeflag {
		case tar.TypeDir:
			if dirs[name] {
				t.Fatalf("directory %q added twice", g.hdr.Name)
			}
			dirs[name] = true
		case tar.TypeReg:
			for d := path.Dir(name); d != "/"; d = path.Dir(d) {
				if !dirs[d] {
					t.Fatalf("%q added before its directory %s", g.hdr.Name, d)
				}
			}
			added = append(added, g)
		default:
			t.Fatalf("unexpected entry %q of type %q at the end of the output", g.hdr.Name, g.hdr.Typeflag)
		}
	}
	if len(added) != len(files) {
		t.Fatalf("%d files added at the end of the output, want %d", len(added), len(files))
	}
	for i, f := range files {
		if g := added[i]; cleanName(g.hdr.Name) != f.name || !bytes.Equal(g.body, f.content) || g.hdr.Mode != 0o644 {
			t.Fatalf("added file %d is %q (mode %o) with %q, want %s with %q", i, g.hdr.Name, g.hdr.Mode, g.body, f.name, f.content)
		}
	}
}

// checkFailed checks that the partial output written by fuzzRewrite
// before failing does not look complete: the files droot adds at the end are
// not there.
func checkFailed(t *testing.T, out []byte) {
	t.Helper()
	got, _ := readFuzzTar(out)
	for _, g := range got {
		if isReplaced(g.hdr.Name) {
			t.Fatalf("rewrite failed after writing %q", g.hdr.Name)
		}
	}
}

// fuzzRewrite rewrites archive like `droot export` without options.
func fuzzRewrite(archive []byte, w io.Writer, files []extraFile) error {
	rw := rewriter{files: files, modTime: mtime}
	return rw.rewrite(bytes.NewReader(archive), w)
}

func fuzzFiles(env []byte) []extraFile {
	return []extraFile{
		{name: environ.EnvFilePath, content: env},
		{name: imageconfig.Path, content: []byte(`{"Version":1}` + "\n")},
	}
}

// FuzzRewriteArchive checks the rewriting of the exported archive with
// archives made of fuzzed entries, complete or truncated by trim bytes.
func FuzzRewriteArchive(f *testing.F) {
	link := func(i byte) string { return string([]byte{i}) }
	for _, seed := range []struct {
		entries []fuzzSpec
		trim    uint16
	}{
		{entries: nil},
		{entries: []fuzzSpec{
			{fuzzDir, "etc/", ""},
			{fuzzReg, "etc/hostname", "droot\n"},
			{fuzzSymlink, "bin", "usr/bin"},
			{fuzzLink, "etc/hostname.hard", link(0)},
			{fuzzFifo, "run/fifo", ""},
			{fuzzReg, ".drootenv", "STALE=1\n"},
		}},
		{entries: []fuzzSpec{ // a hard link to the image's own .drootenv
			{fuzzDir, "etc/", ""},
			{fuzzReg, "\x00", "OLD=1\n"},
			{fuzzLink, "etc/hl-to-drootenv", link(0)},
			{fuzzLink, "etc/hl2", "./.drootenv"},
		}},
		{entries: []fuzzSpec{ // a .drootenv directory
			{fuzzDir, "\x03", ""},
			{fuzzReg, "\x04", "child"},
			{fuzzSymlink, "\x04", "elsewhere"},
			{fuzzDir, "\x05", ""},
			{fuzzLink, "kept", ".drootenv/child"},
		}},
		{entries: []fuzzSpec{ // a .droot file, then a .droot directory
			{fuzzReg, "\x07", "a file"},
			{fuzzReg, "\x09", `{"Version":99}`},
			{fuzzLink, "etc/config", link(1)},
			{fuzzReg, "\x0c", "kept"},
			{fuzzReg, "\x0b", "kept too"},
		}},
		{entries: []fuzzSpec{ // long and non-ASCII names
			{fuzzReg, strings.Repeat("long-directory/", 12) + "file", "long"},
			{fuzzSymlink, "été", strings.Repeat("target/", 20)},
			{fuzzLink, strings.Repeat("x", 200), link(0)},
		}},
		{entries: []fuzzSpec{{fuzzDir, "etc/", ""}, {fuzzReg, "etc/hostname", "droot\n"}}, trim: 512},
		{entries: []fuzzSpec{{fuzzReg, "etc/one", "\x01"}}, trim: 512}, // cut after a zero block
		{entries: []fuzzSpec{{fuzzDir, "etc/", ""}, {fuzzReg, "etc/hostname", "droot\n"}}, trim: 1024},
		{entries: []fuzzSpec{{fuzzReg, "a", strings.Repeat("x", 200)}, {fuzzDir, "b/", ""}}, trim: 1300},
		{entries: []fuzzSpec{{fuzzReg, "a", "x"}}, trim: 1},
	} {
		f.Add(encodeFuzzSpec(seed.entries...), []byte("PATH=/usr/bin:/bin\nHOME=/root\n"), seed.trim)
	}
	f.Fuzz(func(t *testing.T, spec, env []byte, trim uint16) {
		in, archive := fuzzArchive(spec)
		files := fuzzFiles(env)
		input := archive
		if n := int(trim) % len(archive); n > 0 {
			input = archive[:len(archive)-n]
		}
		var out bytes.Buffer
		err := fuzzRewrite(input, &out, files)
		if len(input) < len(archive) {
			if err == nil {
				t.Fatalf("no error for an archive truncated by %d bytes (of %d)", len(archive)-len(input), len(archive))
			}
			checkFailed(t, out.Bytes())
			return
		}
		if err != nil {
			t.Fatalf("rewrite: %v", err)
		}
		checkRewritten(t, in, out.Bytes(), files)
	})
}

// FuzzRewriteArchiveRaw checks the rewriting of the exported archive with
// arbitrary input.
func FuzzRewriteArchiveRaw(f *testing.F) {
	_, sample := fuzzArchive(encodeFuzzSpec(
		fuzzSpec{fuzzDir, "etc/", ""},
		fuzzSpec{fuzzReg, ".drootenv", "OLD=1\n"},
		fuzzSpec{fuzzLink, "etc/hl", ".drootenv"},
		fuzzSpec{fuzzSymlink, strings.Repeat("long/", 30), "target"},
	))
	var gnu bytes.Buffer
	tw := tar.NewWriter(&gnu)
	_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: strings.Repeat("g", 120), Size: 1, Format: tar.FormatGNU})
	_, _ = tw.Write([]byte("g"))
	_ = tw.Close()
	for _, seed := range [][]byte{
		nil,
		sample,
		sample[:len(sample)-512],
		sample[:len(sample)-1024],
		sample[:700],
		append(slices.Clone(sample), "trailing garbage"...),
		gnu.Bytes(),
		[]byte("this is not a tar archive, but long enough to look like a header block..." + strings.Repeat("x", 600)),
		make([]byte, 1024),
		make([]byte, 512),
	} {
		f.Add(seed)
	}
	files := fuzzFiles([]byte("A=1\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		var out bytes.Buffer
		if err := fuzzRewrite(data, &out, files); err != nil {
			checkFailed(t, out.Bytes())
			return
		}
		in, err := readFuzzTar(data)
		if err != nil {
			t.Fatalf("rewrite accepted an archive that archive/tar cannot read: %v", err)
		}
		for _, e := range in {
			switch e.hdr.Typeflag {
			case tar.TypeReg, tar.TypeDir, tar.TypeSymlink, tar.TypeLink, tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			default:
				// archive/tar cannot write some entries back as they
				// were read (sparse files, global headers...).
				// Docker does not produce them.
				if _, err := readFuzzTar(out.Bytes()); err != nil {
					t.Fatalf("the output is not a valid archive: %v", err)
				}
				return
			}
		}
		checkRewritten(t, in, out.Bytes(), files)
	})
}

// FuzzEncodeEnvFile checks that the /.drootenv file written by `droot
// export` is read back by `droot run` as the variables it can represent.
func FuzzEncodeEnvFile(f *testing.F) {
	log.SetOutput(io.Discard) // the warnings for skipped variables
	f.Cleanup(func() { log.SetOutput(os.Stderr) })
	f.Add("PATH=/usr/bin:/bin\x00JAVA_OPTS=-Da=b -Dc=d\x00QUOTE=\"$HOME\" `x`")
	f.Add("A=1\x00CERT=line1\nline2\x00#HASH=v\x00 SPACED=x\x00B=2\x00NOEQUAL\x00=x\x00C=\r\x00\u00a0D=1")
	f.Fuzz(func(t *testing.T, spec string) {
		env := strings.Split(spec, "\x00")
		var want []string
		for _, e := range env {
			k, v, ok := strings.Cut(e, "=")
			if ok && k != "" && strings.TrimSpace(k) == k && !strings.HasPrefix(k, "#") && !strings.ContainsAny(k+v, "\r\n") {
				want = append(want, e)
			}
		}
		got, err := environ.ParseEnv(bytes.NewReader(encodeEnvFile(env)))
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("ParseEnv(encodeEnvFile(%q)) = %q, %v; want %q", env, got, err, want)
		}
	})
}
