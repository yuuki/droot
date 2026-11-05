package image

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/yuuki/droot/internal/log"
)

// Whiteouts, as in the OCI image specification.
const (
	whiteoutPrefix = ".wh."
	whiteoutMeta   = ".wh..wh."
	opaqueWhiteout = ".wh..wh..opq"
)

// Flatten writes the file system of the image to w as a tar stream: its
// layers applied in order, the whiteouts of each layer removing files of the
// layers below it.
//
// The layers are read twice: first to find which entries survive, then to
// write them. Directories are written before their content. The content of
// a file that is deleted or replaced while a hard link to it survives is
// kept in a temporary file in img.TempDir between the two passes.
func (img *Image) Flatten(ctx context.Context, w io.Writer) error {
	f := &flattener{img: img, tree: newNode()}
	defer f.cleanup()
	for i, l := range img.Layers {
		if err := f.scan(ctx, i, l); err != nil {
			return fmt.Errorf("layer %d (%s): %w", i+1, layerName(l), err)
		}
	}
	f.survivors()
	tw := tar.NewWriter(w)
	f.emitted = map[string]bool{"/": true}
	f.materialized = map[ref]string{}
	for i, l := range img.Layers {
		if err := f.write(ctx, i, l, tw); err != nil {
			return fmt.Errorf("layer %d (%s): %w", i+1, layerName(l), err)
		}
	}
	if err := f.writeEmptyDirs(tw, "/", f.tree); err != nil {
		return err
	}
	return tw.Close()
}

func layerName(l *Layer) string {
	if l.Digest != "" {
		return l.Digest
	}
	return l.DiffID
}

// ref identifies an entry: its layer and its index in the layer.
type ref struct{ layer, index int }

// entry is what the first pass remembers of an entry of a layer.
type entry struct {
	name string // absolute and clean
	typ  byte
	link string // hard links: the absolute and clean target
	skip bool   // the root directory and whiteouts
}

// node is a path of the merged file system.
type node struct {
	children map[string]*node
	rec      ref
	has      bool // rec is set; otherwise an implicit directory
	dir      bool // rec is a directory
	// mtime is the time of the entry that created an implicit
	// directory.
	mtime time.Time
}

func newNode() *node { return &node{children: map[string]*node{}} }

func (n *node) lookup(name string) *node {
	if name == "/" {
		return n
	}
	for _, c := range strings.Split(strings.TrimPrefix(name, "/"), "/") {
		if n = n.children[c]; n == nil {
			return nil
		}
	}
	return n
}

type flattener struct {
	img  *Image
	tree *node
	// entries of each layer, in the order of the layer.
	entries [][]entry
	// dirs are the headers of the directories.
	dirs map[ref]*tar.Header
	// inode maps a hard link to the entry of the file it is a name of.
	inode map[ref]ref
	// survive is the set of the entries in the merged file system.
	survive map[ref]bool
	// spool is the set of the files to keep for the hard links to them
	// that survive them; spooled their copies.
	spool   map[ref]bool
	spooled map[ref]*spooledFile
	// emitted is the set of the written directories; materialized maps
	// a file kept for hard links to the first link written.
	emitted      map[string]bool
	materialized map[ref]string
}

type spooledFile struct {
	hdr  *tar.Header
	path string // content, if any
}

func (f *flattener) cleanup() {
	for _, s := range f.spooled {
		if s.path != "" {
			_ = os.Remove(s.path)
		}
	}
}

// parseEntry interprets the name of an entry: whiteouts are returned as
// the path they delete, with opaque set for opaque whiteouts; meta is set
// for the entries to leave out: the root directory and AUFS metadata.
func parseEntry(name string) (clean string, whiteout string, opaque, meta bool) {
	clean = path.Clean("/" + name)
	if clean == "/" {
		return clean, "", false, true
	}
	dir, base := path.Split(clean)
	switch {
	case base == opaqueWhiteout:
		return clean, path.Clean(dir), true, false
	case strings.HasPrefix(base, whiteoutMeta), strings.HasPrefix(clean, "/"+whiteoutMeta):
		return clean, "", false, true
	case strings.HasPrefix(base, whiteoutPrefix) && len(base) > len(whiteoutPrefix):
		return clean, path.Join(dir, base[len(whiteoutPrefix):]), false, false
	}
	return clean, "", false, false
}

// scan reads the headers of layer i and applies it to the tree.
func (f *flattener) scan(ctx context.Context, i int, l *Layer) error {
	if f.dirs == nil {
		f.dirs, f.inode = map[ref]*tar.Header{}, map[ref]ref{}
	}
	rc, err := l.Open(ctx)
	if err != nil {
		return err
	}
	defer rc.Close()
	tr := tar.NewReader(rc)
	var entries []entry
	var mtimes []time.Time
	var whiteouts, opaques []string
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		name, wh, opaque, meta := parseEntry(hdr.Name)
		e := entry{name: name, typ: hdr.Typeflag}
		switch {
		case meta:
			e.skip = true
		case opaque:
			e.skip = true
			opaques = append(opaques, wh)
		case wh != "":
			e.skip = true
			whiteouts = append(whiteouts, wh)
		case hdr.Typeflag == tar.TypeLink:
			e.link = path.Clean("/" + hdr.Linkname)
		case hdr.Typeflag == tar.TypeDir:
			h := *hdr
			f.dirs[ref{i, len(entries)}] = &h
		}
		entries = append(entries, e)
		mtimes = append(mtimes, hdr.ModTime)
	}
	// The end of the stream verifies the digests.
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return err
	}
	f.entries = append(f.entries, entries)

	// Whiteouts only delete the files of the layers below.
	for _, d := range opaques {
		if n := f.tree.lookup(d); n != nil && (n.dir || !n.has) {
			n.children = map[string]*node{}
		}
	}
	for _, p := range whiteouts {
		if n := f.tree.lookup(path.Dir(p)); n != nil {
			delete(n.children, path.Base(p))
		}
	}
	for j, e := range entries {
		if e.skip {
			continue
		}
		r := ref{i, j}
		if e.typ == tar.TypeLink {
			t := f.tree.lookup(e.link)
			if t == nil || !t.has {
				return fmt.Errorf("%s is a hard link to %s, which does not exist", e.name, e.link)
			}
			if t.dir {
				return fmt.Errorf("%s is a hard link to the directory %s", e.name, e.link)
			}
			target := t.rec
			if s, ok := f.inode[target]; ok {
				target = s
			}
			f.inode[r] = target
		}
		f.insert(e.name, r, e.typ == tar.TypeDir, mtimes[j])
	}
	return nil
}

func (f *flattener) insert(name string, r ref, dir bool, mtime time.Time) {
	n := f.tree
	for _, c := range strings.Split(strings.TrimPrefix(name, "/"), "/") {
		child := n.children[c]
		if child == nil {
			child = newNode()
			child.mtime = mtime
			n.children[c] = child
		}
		n = child
	}
	if !dir {
		// A file replaces a directory and its content.
		n.children = map[string]*node{}
	}
	n.rec, n.has, n.dir = r, true, dir
}

// survivors computes the entries of the merged file system, and the files
// to keep for the hard links that survive them.
func (f *flattener) survivors() {
	f.survive = map[ref]bool{}
	var walk func(name string, n *node)
	walk = func(name string, n *node) {
		if n.has {
			f.survive[n.rec] = true
		}
		if n.has && !n.dir && len(n.children) > 0 {
			// A later layer put files below a symlink (or another
			// file): a file system cannot have both, and extracting
			// them could write outside the root directory.
			log.Warnf("leaving out the files of the image below %s, which is not a directory", name)
			n.children = map[string]*node{}
			return
		}
		for c, child := range n.children {
			walk(path.Join(name, c), child)
		}
	}
	walk("/", f.tree)
	f.spool, f.spooled = map[ref]bool{}, map[ref]*spooledFile{}
	for l, s := range f.inode {
		if f.survive[l] && !f.survive[s] {
			f.spool[s] = true
		}
	}
}

// write writes the surviving entries of layer i.
func (f *flattener) write(ctx context.Context, i int, l *Layer, tw *tar.Writer) error {
	rc, err := l.Open(ctx)
	if err != nil {
		return err
	}
	defer rc.Close()
	tr := tar.NewReader(rc)
	entries := f.entries[i]
	for j := 0; ; j++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			if j != len(entries) {
				return errors.New("the layer changed while it was read")
			}
			break
		}
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		if j >= len(entries) || path.Clean("/"+hdr.Name) != entries[j].name {
			return errors.New("the layer changed while it was read")
		}
		e, r := entries[j], ref{i, j}
		if f.spool[r] {
			if err := f.keep(r, hdr, tr); err != nil {
				return err
			}
		}
		if e.skip || !f.survive[r] {
			continue
		}
		if e.typ == tar.TypeDir {
			if f.emitted[e.name] {
				continue // written before its content
			}
			f.emitted[e.name] = true
		}
		if err := f.writeParents(tw, e.name); err != nil {
			return err
		}
		if e.typ == tar.TypeLink {
			if err := f.writeLink(tw, e.name, hdr, r); err != nil {
				return err
			}
			continue
		}
		if err := writeEntry(tw, e.name, hdr, tr); err != nil {
			return err
		}
	}
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return err
	}
	return nil
}

// keep copies an entry that hard links need after it is deleted.
func (f *flattener) keep(r ref, hdr *tar.Header, content io.Reader) error {
	h := *hdr
	s := &spooledFile{hdr: &h}
	f.spooled[r] = s
	if hdr.Typeflag != tar.TypeReg || hdr.Size == 0 {
		return nil
	}
	tmp, err := os.CreateTemp(f.img.TempDir, "droot-link-")
	if err != nil {
		return err
	}
	s.path = tmp.Name()
	if _, err := io.Copy(tmp, content); err != nil {
		_ = tmp.Close()
		return err
	}
	return tmp.Close()
}

// writeLink writes the hard link r: a link to the file if it survives, or
// to the first link written for it, or else a copy of the file.
func (f *flattener) writeLink(tw *tar.Writer, name string, hdr *tar.Header, r ref) error {
	s := f.inode[r]
	target := ""
	if f.survive[s] {
		target = f.entries[s.layer][s.index].name
	} else if m, ok := f.materialized[s]; ok {
		target = m
	}
	if target != "" {
		h := *hdr
		h.Linkname = strings.TrimPrefix(target, "/")
		return writeEntry(tw, name, &h, nil)
	}
	sp := f.spooled[s]
	if sp == nil {
		return fmt.Errorf("internal error: no copy of the target of the hard link %s", name)
	}
	f.materialized[s] = name
	var content io.Reader = strings.NewReader("")
	if sp.path != "" {
		file, err := os.Open(sp.path)
		if err != nil {
			return err
		}
		defer file.Close()
		content = file
	}
	return writeEntry(tw, name, sp.hdr, content)
}

// writeParents writes the directories above name not written yet: their
// headers from the layer that last created them, or a default directory.
func (f *flattener) writeParents(tw *tar.Writer, name string) error {
	var missing []string
	for d := path.Dir(name); d != "/" && !f.emitted[d]; d = path.Dir(d) {
		missing = append(missing, d)
	}
	for k := len(missing) - 1; k >= 0; k-- {
		d := missing[k]
		f.emitted[d] = true
		n := f.tree.lookup(d)
		if n == nil {
			return fmt.Errorf("internal error: no directory %s", d)
		}
		if n.has && !n.dir {
			continue // e.g. a symlink: written at its place
		}
		if err := writeEntry(tw, d, f.dirHeader(n), nil); err != nil {
			return err
		}
	}
	return nil
}

// dirHeader returns the header of the directory n: the one of the layer
// that last created it, or a default one for an implicit directory.
func (f *flattener) dirHeader(n *node) *tar.Header {
	if n.has {
		return f.dirs[n.rec]
	}
	return &tar.Header{Typeflag: tar.TypeDir, Mode: 0o755, ModTime: n.mtime}
}

// writeEmptyDirs writes the implicit directories left empty: the parent
// directories of files deleted later, which stay, as when the layers are
// extracted one after the other.
func (f *flattener) writeEmptyDirs(tw *tar.Writer, name string, n *node) error {
	if !n.has && name != "/" && !f.emitted[name] {
		if err := f.writeParents(tw, name); err != nil {
			return err
		}
		f.emitted[name] = true
		if err := writeEntry(tw, name, f.dirHeader(n), nil); err != nil {
			return err
		}
	}
	for _, c := range slices.Sorted(maps.Keys(n.children)) {
		if err := f.writeEmptyDirs(tw, path.Join(name, c), n.children[c]); err != nil {
			return err
		}
	}
	return nil
}

// writeEntry writes hdr under name, with content.
func writeEntry(tw *tar.Writer, name string, hdr *tar.Header, content io.Reader) error {
	h := *hdr
	h.Name = strings.TrimPrefix(name, "/")
	if h.Typeflag == tar.TypeDir {
		h.Name += "/"
	}
	h.Format = tar.FormatUnknown // the name may need another format
	if err := tw.WriteHeader(&h); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	if content != nil && h.Size > 0 {
		if _, err := io.Copy(tw, content); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}
