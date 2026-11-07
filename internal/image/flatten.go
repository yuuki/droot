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
	raw  string // the name in the layer, absolute and clean
	name string // the path in the merged file system, symlinks resolved
	typ  byte
	link string // Linkname, as in the layer
	size int64
	skip bool // the root directory, whiteouts, entries that are not files
}

// Limits of the names in layers, those of Linux (PATH_MAX, NAME_MAX), which
// a container engine could not create either; they also bound the work of
// Flatten.
const (
	maxPathLen = 4096
	maxNameLen = 255
	// maxSymlinks is the number of symlinks followed in a path, as by
	// Linux (MAXSYMLINKS).
	maxSymlinks = 40
)

// node is a path of the merged file system.
type node struct {
	children map[string]*node
	rec      ref
	has      bool // rec is set; otherwise an implicit directory
	dir      bool // rec is a directory
	// sym is set for symlinks (and hard links to them), with their
	// target.
	sym  bool
	link string
	// mtime is the time of the entry that created an implicit
	// directory.
	mtime time.Time
}

func newNode() *node { return &node{children: map[string]*node{}} }

// isDir reports whether n is a directory, implicit or not.
func (n *node) isDir() bool { return !n.has || n.dir }

func components(name string) []string {
	name = strings.Trim(name, "/")
	if name == "" {
		return nil
	}
	return strings.Split(name, "/")
}

func (n *node) lookup(name string) *node {
	for _, c := range components(name) {
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

// fileType returns the type of a file entry, or false for the entries that
// are not files: PAX global headers (whose records a tar extractor would
// apply to every following entry), GNU volume headers, ...
func fileType(t byte) (byte, bool) {
	switch t {
	case tar.TypeReg, tar.TypeLink, tar.TypeSymlink, tar.TypeChar, tar.TypeBlock, tar.TypeDir, tar.TypeFifo:
		return t, true
	case tar.TypeCont, tar.TypeGNUSparse: // (archive/tar reads TypeRegA as TypeReg)
		return tar.TypeReg, true
	}
	return 0, false
}

// checkName refuses the names Linux refuses.
func checkName(name string) error {
	if len(name) > maxPathLen {
		return fmt.Errorf("a name of %d bytes, longer than %d", len(name), maxPathLen)
	}
	for _, c := range components(name) {
		if len(c) > maxNameLen {
			return fmt.Errorf("%s...: a name component of %d bytes, longer than %d", name[:64], len(c), maxNameLen)
		}
	}
	return nil
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
		if err := checkName(hdr.Name); err != nil {
			return err
		}
		if err := checkName(hdr.Linkname); err != nil {
			return err
		}
		name, wh, opaque, meta := parseEntry(hdr.Name)
		typ, isFile := fileType(hdr.Typeflag)
		e := entry{raw: name, name: name, typ: typ, link: hdr.Linkname, size: hdr.Size}
		switch {
		case !isFile:
			log.Debugf("leaving out %s, of type %q", hdr.Name, hdr.Typeflag)
			e.skip = true
		case meta:
			e.skip = true
		case opaque:
			e.skip = true
			opaques = append(opaques, wh)
		case wh != "":
			e.skip = true
			whiteouts = append(whiteouts, wh)
		case typ == tar.TypeDir:
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

	// Whiteouts only delete the files of the layers below. Like the other
	// paths, theirs are resolved in the merged file system: a symlink to a
	// directory in a lower layer leads to that directory.
	for _, d := range opaques {
		if _, n, err := f.resolveDir(d); err == nil && n != nil && n.isDir() {
			n.children = map[string]*node{}
		}
	}
	for _, p := range whiteouts {
		if _, n, err := f.resolveDir(path.Dir(p)); err == nil && n != nil && n.isDir() {
			delete(n.children, path.Base(p))
		}
	}
	for j := range entries {
		e := &entries[j]
		if e.skip {
			continue
		}
		r := ref{i, j}
		parent, _, err := f.resolveDir(path.Dir(e.raw))
		if err != nil {
			return fmt.Errorf("%s: %w", e.raw, err)
		}
		e.name = path.Join(parent, path.Base(e.raw))
		if len(e.name) > 2*maxPathLen {
			return fmt.Errorf("%s: the path through symlinks is too long", e.raw)
		}
		// A hard link to a symlink is a symlink: what the target is must
		// be read before insert, which resets the node of a hard link
		// that names its own target.
		hardLink, linkSym, linkTarget := false, false, ""
		if e.typ == tar.TypeLink {
			target, err := f.hardLinkTarget(e)
			if err != nil {
				return err
			}
			t := target.rec
			if s, ok := f.inode[t]; ok {
				t = s
			}
			f.inode[r] = t
			hardLink, linkSym, linkTarget = true, target.sym, target.link
		}
		n := f.insert(e.name, r, e.typ == tar.TypeDir, mtimes[j])
		switch {
		case e.typ == tar.TypeSymlink:
			n.sym, n.link = true, e.link
		case hardLink:
			n.sym, n.link = linkSym, linkTarget
		}
	}
	f.entries = append(f.entries, entries)
	return nil
}

// hardLinkTarget returns the node of the file the hard link e names.
func (f *flattener) hardLinkTarget(e *entry) (*node, error) {
	target := path.Clean("/" + e.link)
	parent, p, err := f.resolveDir(path.Dir(target))
	if err != nil {
		return nil, fmt.Errorf("%s is a hard link to %s: %w", e.raw, target, err)
	}
	var t *node
	if p != nil {
		t = p.children[path.Base(target)]
	}
	if t == nil || !t.has {
		return nil, fmt.Errorf("%s is a hard link to %s, which does not exist", e.raw, path.Join(parent, path.Base(target)))
	}
	if t.dir {
		return nil, fmt.Errorf("%s is a hard link to the directory %s", e.raw, target)
	}
	return t, nil
}

// errNotDir is a path through something else than a directory.
var errNotDir = errors.New("not a directory")

// resolveDir returns the directory name, absolute, in the merged file
// system as it is, with its symlinks resolved as a container engine resolves
// them when it extracts a layer in a chroot, and its node (nil if it does
// not exist: missing directories would be created). The symlinks on the way
// must lead to directories, and name must be one if it exists.
func (f *flattener) resolveDir(name string) (string, *node, error) {
	hops := 0
	return f.resolveFrom(name, &hops)
}

func (f *flattener) resolveFrom(name string, hops *int) (string, *node, error) {
	todo := components(name)
	var cur []string
	var nodes []*node // nodes[k] is the node of cur[:k+1], nil if missing
	last := func() *node {
		if len(nodes) == 0 {
			return f.tree
		}
		return nodes[len(nodes)-1]
	}
	for len(todo) > 0 {
		c := todo[0]
		todo = todo[1:]
		switch c {
		case ".":
			continue
		case "..":
			if len(cur) > 0 {
				cur, nodes = cur[:len(cur)-1], nodes[:len(nodes)-1]
			}
			continue
		}
		p := last()
		if p != nil && !p.isDir() {
			return "", nil, fmt.Errorf("/%s: %w", strings.Join(cur, "/"), errNotDir)
		}
		var child *node
		if p != nil {
			child = p.children[c]
		}
		if child != nil && child.sym {
			*hops++
			if *hops > maxSymlinks {
				return "", nil, errors.New("too many levels of symbolic links")
			}
			target := child.link
			if !path.IsAbs(target) {
				target = "/" + strings.Join(cur, "/") + "/" + target
			}
			tp, tn, err := f.resolveFrom(target, hops)
			if err != nil {
				return "", nil, err
			}
			if tn == nil {
				return "", nil, fmt.Errorf("/%s: a symlink to %s, which is not a directory", strings.Join(append(cur, c), "/"), tp)
			}
			cur = components(tp)
			nodes = nodes[:0]
			n := f.tree
			for _, k := range cur {
				n = n.children[k]
				nodes = append(nodes, n)
			}
			continue
		}
		cur = append(cur, c)
		nodes = append(nodes, child)
	}
	if n := last(); n != nil && !n.isDir() {
		return "", nil, fmt.Errorf("/%s: %w", strings.Join(cur, "/"), errNotDir)
	}
	return "/" + strings.Join(cur, "/"), last(), nil
}

// insert puts the entry r on the path name, whose parent directories are
// resolved, and returns its node.
func (f *flattener) insert(name string, r ref, dir bool, mtime time.Time) *node {
	n := f.tree
	for _, c := range components(name) {
		child := n.children[c]
		if child == nil {
			child = newNode()
			child.mtime = mtime
			n.children[c] = child
		}
		n = child
	}
	if !dir || !n.isDir() {
		// A file replaces a directory and its content; a directory
		// replaces a file.
		n.children = map[string]*node{}
	}
	n.rec, n.has, n.dir = r, true, dir
	n.sym, n.link = false, ""
	return n
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
		// What is written must be what the first pass saw (the digests
		// are only checked again at the end of the layer).
		if j >= len(entries) || !entries[j].same(hdr) {
			return errors.New("the layer changed while it was read")
		}
		e, r := entries[j], ref{i, j}
		if !e.skip {
			hdr.Typeflag = e.typ
		}
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

// same reports whether hdr is the entry e of the first pass.
func (e *entry) same(hdr *tar.Header) bool {
	typ, _ := fileType(hdr.Typeflag)
	return path.Clean("/"+hdr.Name) == e.raw && typ == e.typ && hdr.Linkname == e.link && hdr.Size == e.size
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
