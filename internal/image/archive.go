package image

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// files gives access to the files of a directory or of a tar archive.
type files interface {
	open(name string) (io.ReadCloser, error)
}

// dirFiles are the files of a directory.
type dirFiles string

func (d dirFiles) open(name string) (io.ReadCloser, error) {
	if !fs.ValidPath(name) {
		return nil, fmt.Errorf("invalid path %q", name)
	}
	return os.Open(filepath.Join(string(d), filepath.FromSlash(name)))
}

// tarFiles are the files of an uncompressed tar archive, read in place.
type tarFiles struct {
	f       *os.File
	entries map[string]tarEntry
}

type tarEntry struct {
	off, size int64
	typ       byte
	link      string
}

// counter counts the bytes read or skipped from a file.
type counter struct {
	f   *os.File
	pos int64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.f.Read(p)
	c.pos += int64(n)
	return n, err
}

// Seek lets archive/tar skip the content of the entries.
func (c *counter) Seek(off int64, whence int) (int64, error) {
	pos, err := c.f.Seek(off, whence)
	if err == nil {
		c.pos = pos
	}
	return pos, err
}

// openTar indexes the entries of the tar archive at name.
func openTar(name string) (*tarFiles, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	var magic [4]byte
	if n, _ := io.ReadFull(f, magic[:]); n >= 2 && (bytes.HasPrefix(magic[:], []byte{0x1f, 0x8b}) || bytes.Equal(magic[:], []byte{0x28, 0xb5, 0x2f, 0xfd})) {
		_ = f.Close()
		return nil, fmt.Errorf("%s is compressed: decompress it first", name)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, err
	}
	t := &tarFiles{f: f, entries: map[string]tarEntry{}}
	c := &counter{f: f}
	tr := tar.NewReader(c)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		t.entries[cleanName(hdr.Name)] = tarEntry{off: c.pos, size: hdr.Size, typ: hdr.Typeflag, link: hdr.Linkname}
	}
	if len(t.entries) == 0 {
		_ = f.Close()
		return nil, fmt.Errorf("%s is not a tar archive, or is empty", name)
	}
	return t, nil
}

// cleanName is the relative clean form of the name of an entry.
func cleanName(name string) string {
	return strings.TrimPrefix(path.Clean("/"+name), "/")
}

func (t *tarFiles) open(name string) (io.ReadCloser, error) {
	name = cleanName(name)
	for range 16 {
		e, ok := t.entries[name]
		if !ok {
			return nil, fmt.Errorf("%s: %w", name, fs.ErrNotExist)
		}
		switch e.typ {
		case tar.TypeReg:
			return io.NopCloser(io.NewSectionReader(t.f, e.off, e.size)), nil
		case tar.TypeSymlink: // docker save links the duplicated layers
			if path.IsAbs(e.link) {
				name = cleanName(e.link)
			} else {
				name = cleanName(path.Join(path.Dir(name), e.link))
			}
		case tar.TypeLink:
			name = cleanName(e.link)
		default:
			return nil, fmt.Errorf("%s is not a regular file", name)
		}
	}
	return nil, fmt.Errorf("%s: too many levels of links", name)
}

func (t *tarFiles) Close() error { return t.f.Close() }

// splitPath splits FILE[:NAME]: FILE is the longest prefix that exists, so
// that FILE may contain colons.
func splitPath(s string) (file, name string) {
	if _, err := os.Stat(s); err == nil {
		return s, ""
	}
	for i := len(s) - 1; i > 0; i-- {
		if s[i] != ':' {
			continue
		}
		if _, err := os.Stat(s[:i]); err == nil {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}

// readFile reads a small file of a source.
func readFile(fsys files, name string) ([]byte, error) {
	rc, err := fsys.open(name)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return readAllLimited(rc, maxManifestSize, name)
}

// layoutStore reads the blobs of an OCI image layout.
type layoutStore struct{ fsys files }

func blobPath(digest string) (string, error) {
	if err := validDigest(digest); err != nil {
		return "", err
	}
	alg, hexPart, _ := strings.Cut(digest, ":")
	return "blobs/" + alg + "/" + hexPart, nil
}

func (s layoutStore) blob(_ context.Context, d Descriptor) (io.ReadCloser, error) {
	p, err := blobPath(d.Digest)
	if err != nil {
		return nil, err
	}
	rc, err := s.fsys.open(p)
	if err != nil {
		return nil, fmt.Errorf("blob %s: %w", d.Digest, err)
	}
	return newVerifier(limitSize(rc, d), d.Digest, "blob "+d.Digest)
}

func (s layoutStore) manifest(ctx context.Context, d Descriptor) ([]byte, string, error) {
	rc, err := s.blob(ctx, d)
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()
	b, err := readAllLimited(rc, maxManifestSize, "manifest "+d.Digest)
	return b, d.MediaType, err
}

const (
	annotationRefName       = "org.opencontainers.image.ref.name"
	annotationContainerName = "io.containerd.image.name"
)

// openLayout opens the image name of the OCI image layout fsys.
func openLayout(ctx context.Context, fsys files, name string, sel selector) (*Image, error) {
	b, err := readFile(fsys, "oci-layout")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errors.New("not an OCI image layout: no oci-layout file")
	}
	if err != nil {
		return nil, err
	}
	var layout struct {
		Version string `json:"imageLayoutVersion"`
	}
	if err := json.Unmarshal(b, &layout); err != nil || layout.Version == "" {
		return nil, errors.New("not an OCI image layout: invalid oci-layout file")
	}
	b, err = readFile(fsys, "index.json")
	if err != nil {
		return nil, err
	}
	var index manifest
	if err := json.Unmarshal(b, &index); err != nil {
		return nil, fmt.Errorf("parse index.json: %w", err)
	}
	d, err := chooseInLayout(index.Manifests, name, sel)
	if err != nil {
		return nil, err
	}
	return resolve(ctx, layoutStore{fsys}, d, sel)
}

// chooseInLayout chooses the image name in the index of a layout.
func chooseInLayout(manifests []Descriptor, name string, sel selector) (Descriptor, error) {
	var names []string
	for _, m := range manifests {
		ref, full := m.Annotations[annotationRefName], m.Annotations[annotationContainerName]
		if name != "" && (name == ref || name == full || (full != "" && sameImage(name, full))) {
			return m, nil
		}
		if full != "" {
			names = append(names, full)
		} else if ref != "" {
			names = append(names, ref)
		}
	}
	switch {
	case len(manifests) == 0:
		return Descriptor{}, errors.New("the layout has no image")
	case name != "":
		return Descriptor{}, fmt.Errorf("the layout has no image named %q (it has: %s)", name, strings.Join(names, ", "))
	case len(manifests) == 1:
		return manifests[0], nil
	}
	// Several images: the variants of one image for several platforms, or
	// several images to choose from.
	if !slices.ContainsFunc(manifests, func(m Descriptor) bool { return m.Platform == nil }) {
		return sel.choose(manifests)
	}
	return Descriptor{}, fmt.Errorf("the layout has several images: add the name of one to the reference (it has: %s)", strings.Join(names, ", "))
}

// sameImage compares two image names, with Docker Hub's defaults:
// alpine, docker.io/library/alpine:latest, ...
func sameImage(a, b string) bool {
	return normalizeName(a) == normalizeName(b)
}

func normalizeName(s string) string {
	r, err := parseReference(s)
	if err != nil {
		return s
	}
	return r.String()
}

func openLayoutDir(ctx context.Context, spec string, sel selector) (*Image, error) {
	dir, name := splitPath(spec)
	if fi, err := os.Stat(dir); err != nil {
		return nil, err
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory (for an archive, use oci-archive:)", dir)
	}
	img, err := openLayout(ctx, dirFiles(dir), name, sel)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	return img, nil
}

func openLayoutArchive(ctx context.Context, spec string, sel selector) (*Image, error) {
	file, name := splitPath(spec)
	t, err := openTar(file)
	if err != nil {
		return nil, err
	}
	img, err := openLayout(ctx, t, name, sel)
	if err != nil {
		_ = t.Close()
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	img.closers = append(img.closers, t.Close)
	return img, nil
}

// openDockerArchive opens an image of a `docker save` archive, from its
// manifest.json.
func openDockerArchive(_ context.Context, spec string, sel selector) (*Image, error) {
	file, name := splitPath(spec)
	t, err := openTar(file)
	if err != nil {
		return nil, err
	}
	img, err := readDockerArchive(t, name, sel)
	if err != nil {
		_ = t.Close()
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	img.closers = append(img.closers, t.Close)
	return img, nil
}

func readDockerArchive(t *tarFiles, name string, sel selector) (*Image, error) {
	b, err := readFile(t, "manifest.json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errors.New("not a docker save archive: no manifest.json")
	}
	if err != nil {
		return nil, err
	}
	var images []struct {
		Config   string   `json:"Config"`
		RepoTags []string `json:"RepoTags"`
		Layers   []string `json:"Layers"`
	}
	if err := json.Unmarshal(b, &images); err != nil {
		return nil, fmt.Errorf("parse manifest.json: %w", err)
	}
	chosen := -1
	var names []string
	for i, im := range images {
		names = append(names, im.RepoTags...)
		if name != "" && slices.ContainsFunc(im.RepoTags, func(t string) bool { return sameImage(t, name) }) {
			chosen = i
		}
	}
	switch {
	case len(images) == 0:
		return nil, errors.New("the archive has no image")
	case name != "" && chosen < 0:
		return nil, fmt.Errorf("the archive has no image named %q (it has: %s)", name, strings.Join(names, ", "))
	case name == "" && len(images) > 1:
		return nil, fmt.Errorf("the archive has several images: add the name of one to the reference (it has: %s)", strings.Join(names, ", "))
	case chosen < 0:
		chosen = 0
	}
	im := images[chosen]

	// The configuration is named after its digest, the ID of the image.
	hexPart := strings.TrimSuffix(path.Base(im.Config), ".json")
	id := "sha256:" + hexPart
	if validDigest(id) != nil {
		return nil, fmt.Errorf("unexpected name of the configuration %q", im.Config)
	}
	rc, err := t.open(im.Config)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	v, err := newVerifier(rc, id, "configuration "+im.Config)
	if err != nil {
		return nil, err
	}
	b, err = readAllLimited(v, maxManifestSize, "the image configuration")
	if err != nil {
		return nil, err
	}
	cfg, err := parseConfig(b)
	if err != nil {
		return nil, err
	}
	if err := sel.check(cfg); err != nil {
		return nil, err
	}
	img := &Image{ID: id, Config: *cfg}
	for _, lp := range im.Layers {
		l := &Layer{}
		l.open = func(context.Context) (io.ReadCloser, error) { return t.open(lp) }
		// The OCI layout of Docker >= 25 names the layers by digest.
		if dir, hexPart, ok := strings.Cut(cleanName(lp), "/"); ok && dir == "blobs" {
			if alg, hexPart, ok := strings.Cut(hexPart, "/"); ok && validDigest(alg+":"+hexPart) == nil {
				l.Digest = alg + ":" + hexPart
				l.open = func(context.Context) (io.ReadCloser, error) {
					rc, err := t.open(lp)
					if err != nil {
						return nil, err
					}
					return newVerifier(rc, l.Digest, "layer "+lp)
				}
			}
		}
		img.Layers = append(img.Layers, l)
	}
	if err := setDiffIDs(img); err != nil {
		return nil, err
	}
	return img, nil
}
