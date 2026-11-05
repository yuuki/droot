package image

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// Layer is a layer of an image.
type Layer struct {
	// Digest is the digest of the layer as stored (compressed or not), if
	// known; DiffID the digest of its uncompressed content, if known.
	Digest, DiffID string
	// Size is the stored size, if known.
	Size int64
	// open opens the stored content, verified against Digest.
	open func(ctx context.Context) (io.ReadCloser, error)
}

// Open returns the uncompressed tar stream of the layer. Reading it to the
// end verifies the digests of the layer: Read then fails on a mismatch.
func (l *Layer) Open(ctx context.Context) (io.ReadCloser, error) {
	stored, err := l.open(ctx)
	if err != nil {
		return nil, err
	}
	r, err := decompress(ctx, stored)
	if err != nil {
		_ = stored.Close()
		return nil, err
	}
	if l.DiffID != "" {
		return newVerifier(r, l.DiffID, "layer "+l.DiffID)
	}
	return r, nil
}

// decompress returns the uncompressed content of r, gzip, zstd or plain
// tar, detected from its first bytes. Reaching the end of the result reads r
// to its end.
func decompress(ctx context.Context, r io.ReadCloser) (io.ReadCloser, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	magic, err := br.Peek(4)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	switch {
	case bytes.HasPrefix(magic, []byte{0x1f, 0x8b}):
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		return &drainOnEOF{r: gz, under: br, close: r.Close}, nil
	case bytes.Equal(magic, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		return newZstdReader(ctx, br, r.Close)
	}
	return &drainOnEOF{r: br, under: br, close: r.Close}, nil
}

// drainOnEOF reads under to its end when r ends, so that the digest of what
// was stored is verified even if the decompressor did not read it all.
type drainOnEOF struct {
	r, under io.Reader
	close    func() error
	err      error
}

func (d *drainOnEOF) Read(p []byte) (int, error) {
	if d.err != nil {
		return 0, d.err
	}
	n, err := d.r.Read(p)
	if errors.Is(err, io.EOF) {
		if _, derr := io.Copy(io.Discard, d.under); derr != nil {
			err = derr
		}
	}
	if err != nil {
		d.err = err
	}
	return n, err
}

func (d *drainOnEOF) Close() error { return d.close() }

// zstdReader decompresses with the zstd command: zstd is not in the
// standard library.
type zstdReader struct {
	cmd    *exec.Cmd
	out    io.ReadCloser
	stderr *bytes.Buffer
	close  func() error
	once   sync.Once
	err    error
}

func newZstdReader(ctx context.Context, r io.Reader, closeInput func() error) (io.ReadCloser, error) {
	path, err := exec.LookPath("zstd")
	if err != nil {
		return nil, errors.New("the layer is compressed with zstd: install the zstd command")
	}
	cmd := exec.CommandContext(ctx, path, "-q", "-d", "-c")
	cmd.Stdin = r
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start zstd: %w", err)
	}
	return &zstdReader{cmd: cmd, out: out, stderr: stderr, close: closeInput}, nil
}

func (z *zstdReader) wait() error {
	z.once.Do(func() {
		if err := z.cmd.Wait(); err != nil {
			z.err = fmt.Errorf("zstd: %w: %s", err, strings.TrimSpace(z.stderr.String()))
		}
	})
	return z.err
}

func (z *zstdReader) Read(p []byte) (int, error) {
	n, err := z.out.Read(p)
	if errors.Is(err, io.EOF) {
		// The input is read to its end by zstd (several frames may
		// follow each other): its digest error, if any, comes from Wait.
		if werr := z.wait(); werr != nil {
			return n, werr
		}
	}
	return n, err
}

func (z *zstdReader) Close() error {
	_ = z.out.Close()
	if z.cmd.ProcessState == nil {
		_ = z.cmd.Process.Kill()
	}
	_ = z.wait()
	return z.close()
}

// verifier checks the digest of what is read through it, at the end.
type verifier struct {
	r      io.ReadCloser
	h      hash.Hash
	digest string
	name   string
	err    error
}

// validDigest checks the syntax of a digest. Only sha256 is supported.
func validDigest(digest string) error {
	alg, hexPart, ok := strings.Cut(digest, ":")
	if !ok || alg != "sha256" {
		return fmt.Errorf("unsupported digest %q: only sha256 digests are supported", digest)
	}
	if len(hexPart) != 64 || strings.Trim(hexPart, "0123456789abcdef") != "" {
		return fmt.Errorf("invalid digest %q", digest)
	}
	return nil
}

func newVerifier(r io.ReadCloser, digest, name string) (io.ReadCloser, error) {
	if err := validDigest(digest); err != nil {
		_ = r.Close()
		return nil, err
	}
	return &verifier{r: r, h: sha256.New(), digest: digest, name: name}, nil
}

func (v *verifier) Read(p []byte) (int, error) {
	if v.err != nil {
		return 0, v.err
	}
	n, err := v.r.Read(p)
	v.h.Write(p[:n])
	if errors.Is(err, io.EOF) {
		if got := "sha256:" + hex.EncodeToString(v.h.Sum(nil)); got != v.digest {
			err = &digestError{name: v.name, got: got}
		}
	}
	if err != nil {
		v.err = err
	}
	return n, err
}

func (v *verifier) Close() error { return v.r.Close() }

// digestError is a content that does not have its digest.
type digestError struct{ name, got string }

func (e *digestError) Error() string {
	return fmt.Sprintf("%s: digest mismatch: the content has the digest %s (corrupted or tampered with)", e.name, e.got)
}

// sizeLimit fails when more than n bytes are read: a blob larger than its
// descriptor says.
type sizeLimit struct {
	io.ReadCloser
	n    int64
	name string
}

func (s *sizeLimit) Read(p []byte) (int, error) {
	if int64(len(p)) > s.n+1 {
		p = p[:s.n+1]
	}
	n, err := s.ReadCloser.Read(p)
	s.n -= int64(n)
	if s.n < 0 {
		return n, fmt.Errorf("%s is larger than its descriptor says", s.name)
	}
	return n, err
}

// limitSize wraps r with sizeLimit if the size of d is known.
func limitSize(r io.ReadCloser, d Descriptor) io.ReadCloser {
	if d.Size <= 0 {
		return r
	}
	return &sizeLimit{ReadCloser: r, n: d.Size, name: d.Digest}
}
