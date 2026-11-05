package docker

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/yuuki/droot/internal/environ"
	"github.com/yuuki/droot/internal/imageconfig"
	"github.com/yuuki/droot/internal/log"
)

// API is the subset of the Docker Engine API used by ExportImage.
// *Client implements it; tests use a fake.
type API interface {
	ContainerCreate(ctx context.Context, cfg *ContainerConfig, platform string) (string, error)
	ContainerInspect(ctx context.Context, id string) (*ContainerInfo, error)
	ImageInspect(ctx context.Context, ref, platform string) (*ImageInfo, error)
	ContainerExport(ctx context.Context, id string) (io.ReadCloser, error)
	ContainerRemove(ctx context.Context, id string) error
}

// ExportOptions customizes ExportImage.
type ExportOptions struct {
	// Platform selects the image variant, e.g. "linux/arm64". Optional.
	Platform string
	// Exclude drops the entries matching one of these patterns, and
	// everything below a matching directory (see Excluded).
	Exclude []string
	// DropDockerFiles drops the files Docker adds to every container:
	// /.dockerenv, and the empty /etc/hostname, /etc/hosts,
	// /etc/resolv.conf and /dev/console.
	DropDockerFiles bool
	// Reproducible makes the archive depend only on the image: the
	// modification times later than ClampTime (default: the image's
	// creation time) are set to it, and access and change times are
	// dropped, so that exporting the same image twice gives the same
	// archive.
	Reproducible bool
	ClampTime    time.Time
}

// exportLabel marks the temporary containers created by droot so that leftovers
// can be found with `docker ps -a --filter label=io.github.yuuki.droot`.
const exportLabel = "io.github.yuuki.droot"

// placeholderCmd is set on the temporary container only because the daemon
// refuses to create a container without a command (e.g. FROM scratch images).
// The container is never started, so the command is never executed.
var placeholderCmd = []string{"/.droot-export-placeholder"}

// removeTimeout bounds the cleanup of the temporary container, which also runs
// after the main context was cancelled (e.g. by Ctrl-C).
const removeTimeout = 30 * time.Second

// createTimeout bounds the creation of the temporary container, which is not
// interrupted by cancellation (see ExportImage).
const createTimeout = 2 * time.Minute

// ExportImage writes the root filesystem of image to w as a tar archive.
//
// The image's configuration (environment, working directory, user,
// entrypoint and command) is stored in /.droot/config.json inside the
// archive, and its environment variables in /.drootenv too, for older
// versions of droot, so that `droot run` can restore them. The image is
// never run: a
// container is created (but not started), exported and then removed. This
// works for any image, including ones without /bin/sh such as distroless or
// FROM scratch images.
func ExportImage(ctx context.Context, api API, image string, w io.Writer, opts ExportOptions) (err error) {
	// The creation is not cancelled with ctx: if the request were aborted
	// after the daemon received it, the container would be created without
	// droot ever learning its ID, and could not be removed. Cancellation is
	// checked right after.
	createCtx, cancelCreate := context.WithTimeout(context.WithoutCancel(ctx), createTimeout)
	id, err := api.ContainerCreate(createCtx, &ContainerConfig{
		Image:      image,
		Entrypoint: placeholderCmd,
		Labels:     map[string]string{exportLabel: "export"},
	}, opts.Platform)
	cancelCreate()
	if err != nil {
		if isNoSuchImage(err) {
			return fmt.Errorf("image %s not found locally; pull or build it first (`droot export --pull`, or e.g. `docker pull %s`): %w", image, image, err)
		}
		return fmt.Errorf("create container from image %s: %w", image, err)
	}
	log.Debugf("created temporary container %s", shortID(id))

	defer func() {
		rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removeTimeout)
		defer cancel()
		if rmErr := api.ContainerRemove(rmCtx, id); rmErr != nil {
			rmErr = fmt.Errorf("remove temporary container %s: %w", shortID(id), rmErr)
			if err == nil {
				log.Warnf("%v", rmErr)
			} else {
				err = errors.Join(err, rmErr)
			}
			return
		}
		log.Debugf("removed temporary container %s", shortID(id))
	}()
	if err := ctx.Err(); err != nil {
		return err
	}

	// Take the environment from the image the container was created from,
	// not from the container's config, to which some daemons add variables
	// (Podman adds container=podman and reorders them).
	info, err := api.ContainerInspect(ctx, id)
	if err != nil {
		return fmt.Errorf("inspect container %s: %w", shortID(id), err)
	}
	imageRef := info.Image
	if imageRef == "" {
		imageRef = image
	}
	img, err := api.ImageInspect(ctx, imageRef, opts.Platform)
	if err != nil {
		return fmt.Errorf("inspect image %s: %w", image, err)
	}
	cfg := imageconfig.Config{Version: imageconfig.Version, Image: image, ImageID: img.ID}
	if c := img.Config; c != nil {
		cfg.Env, cfg.WorkingDir, cfg.User = c.Env, c.WorkingDir, c.User
		cfg.Entrypoint, cfg.Cmd, cfg.StopSignal = c.Entrypoint, c.Cmd, c.StopSignal
	}
	cfgFile, err := cfg.Encode()
	if err != nil {
		return err
	}
	rw := rewriter{
		files: []extraFile{
			{name: environ.EnvFilePath, content: encodeEnvFile(cfg.Env)},
			{name: imageconfig.Path, content: cfgFile},
		},
		exclude:         opts.Exclude,
		dropDockerFiles: opts.DropDockerFiles,
		modTime:         time.Now().Truncate(time.Second),
	}
	if opts.Reproducible {
		rw.clamp = opts.ClampTime
		if rw.clamp.IsZero() {
			created, err := time.Parse(time.RFC3339Nano, img.Created)
			if err != nil {
				return fmt.Errorf("--reproducible: the image has no valid creation time (%q); set SOURCE_DATE_EPOCH", img.Created)
			}
			rw.clamp = created
		}
		rw.clamp = rw.clamp.Truncate(time.Second)
		rw.modTime = rw.clamp
	}

	body, err := api.ContainerExport(ctx, id)
	if err != nil {
		return fmt.Errorf("export container %s: %w", shortID(id), err)
	}
	defer body.Close()

	if err := rw.rewrite(body, w); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	return nil
}

// encodeEnvFile renders env in the format read by environ.ParseEnv.
// Variables that would not be read back exactly — values containing a
// newline, keys starting with '#' (read as comments) or surrounded by
// spaces — are skipped with a warning; /.droot/config.json has them.
func encodeEnvFile(env []string) []byte {
	var b strings.Builder
	for _, e := range env {
		parsed, err := environ.ParseEnv(strings.NewReader(e))
		if err != nil || len(parsed) != 1 || parsed[0] != e || strings.ContainsAny(e, "\r\n") {
			k, _, _ := strings.Cut(e, "=")
			log.Warnf("environment variable %q cannot be represented in %s: only %s has it (droot 0.7 ignores it)", k, environ.EnvFilePath, imageconfig.Path)
			continue
		}
		b.WriteString(e)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// replacedPaths are the paths of the archive that droot writes itself.
var replacedPaths = []string{environ.EnvFilePath, imageconfig.Dir}

// isReplaced reports whether a tar entry is dropped from the archive because
// droot writes its own files: /.drootenv and /.droot, or anything below them
// (if the image has a directory /.drootenv, or a file /.droot).
func isReplaced(name string) bool {
	p := path.Clean("/" + name)
	for _, r := range replacedPaths {
		if p == r || strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

// extraFile is a regular file droot adds to the archive.
type extraFile struct {
	name    string // absolute path in the container
	content []byte
}

// maxDroppedBytes bounds the content of dropped entries kept in memory to
// rewrite hard links pointing at them.
const maxDroppedBytes = 64 << 20

// zeroTail counts the consecutive zero bytes at the end of what was read.
type zeroTail struct {
	r     io.Reader
	zeros int
}

func (z *zeroTail) Read(p []byte) (int, error) {
	n, err := z.r.Read(p)
	for _, b := range p[:n] {
		if b == 0 {
			z.zeros++
		} else {
			z.zeros = 0
		}
	}
	return n, err
}

// errTruncated is returned for an archive without end-of-archive marker.
var errTruncated = errors.New("the exported archive is truncated (no end-of-archive marker): the connection to the Docker daemon was probably cut")

// rewriter rewrites the archive exported by the daemon.
type rewriter struct {
	files           []extraFile // appended, with their parent directories
	exclude         []string
	dropDockerFiles bool
	clamp           time.Time // if not zero, the latest modification time
	modTime         time.Time // of the appended files
}

// dockerFiles are the files Docker adds to every container. They are only
// dropped if they are empty regular files, except /.dockerenv.
var dockerFiles = []string{"/.dockerenv", "/etc/hostname", "/etc/hosts", "/etc/resolv.conf", "/dev/console"}

// Excluded reports whether the entry name (relative or absolute) is
// excluded by one of the patterns: a pattern containing "/" is matched
// with path.Match against the absolute path of the entry and of its parent
// directories, a pattern without "/" against their base names, so that
// "/usr/share/doc" excludes that directory and everything below it, and
// "*.pyc" every .pyc file.
func Excluded(name string, patterns []string) bool {
	p := path.Clean("/" + name)
	for ; p != "/"; p = path.Dir(p) {
		for _, pat := range patterns {
			subject := path.Base(p)
			if strings.Contains(pat, "/") {
				subject, pat = p, path.Clean("/"+pat)
			}
			if ok, _ := path.Match(pat, subject); ok {
				return true
			}
		}
	}
	return false
}

// ValidatePatterns checks the syntax of exclusion patterns.
func ValidatePatterns(patterns []string) error {
	for _, pat := range patterns {
		if _, err := path.Match(pat, ""); err != nil || pat == "" {
			return fmt.Errorf("invalid exclusion pattern %q", pat)
		}
	}
	return nil
}

func (rw *rewriter) dropped(hdr *tar.Header) bool {
	if len(rw.exclude) > 0 && Excluded(hdr.Name, rw.exclude) {
		return true
	}
	if rw.dropDockerFiles {
		p := path.Clean("/" + hdr.Name)
		if p == "/.dockerenv" {
			return true
		}
		if slices.Contains(dockerFiles, p) && hdr.Typeflag == tar.TypeReg && hdr.Size == 0 {
			return true
		}
	}
	return false
}

// normalize applies --reproducible to a header.
func (rw *rewriter) normalize(hdr *tar.Header) {
	if rw.clamp.IsZero() {
		return
	}
	if hdr.ModTime.After(rw.clamp) {
		hdr.ModTime = rw.clamp
	}
	hdr.AccessTime, hdr.ChangeTime = time.Time{}, time.Time{}
	for _, k := range []string{"atime", "ctime", "mtime"} {
		delete(hdr.PAXRecords, k)
	}
	// Sub-second precision would need PAX records whose presence depends
	// on the daemon.
	hdr.ModTime = hdr.ModTime.Truncate(time.Second)
}

// rewrite copies the tar stream r to w, dropping the entries of
// replacedPaths and the excluded ones, and appending rw.files (and their
// parent directories).
//
// archive/tar reports the end of the input at an entry boundary like the
// end-of-archive marker (two zero blocks), so a stream cut cleanly between
// two entries would look complete. The marker is therefore checked
// explicitly: the stream must end with at least 1024 zero bytes.
func (rw *rewriter) rewrite(r io.Reader, w io.Writer) error {
	zr := &zeroTail{r: r}
	tr := tar.NewReader(zr)
	tw := tar.NewWriter(w)
	st := &rewriteState{
		dropped:    map[string][]byte{},
		droppedHdr: map[string]*tar.Header{},
		excluded:   map[string]bool{},
	}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			if zr.zeros < 2*512 {
				return errTruncated
			}
			break
		}
		if err != nil {
			return fmt.Errorf("read exported archive: %w", err)
		}
		if err := rw.entry(hdr, tr, tw, st); err != nil {
			return err
		}
		// Consume the rest of the entry's data, so that only its padding
		// and the end-of-archive marker count as trailing zeros: data
		// ending with zeros must not hide a truncation.
		if _, err := io.Copy(io.Discard, tr); err != nil { //nolint:gosec // G110: an uncompressed stream, not decompressed
			return fmt.Errorf("read archive entry %s: %w", hdr.Name, err)
		}
		zr.zeros = 0
	}

	modTime := rw.modTime
	dirs := map[string]bool{}
	for _, f := range rw.files {
		// Parent directories first, e.g. .droot/ for .droot/config.json.
		var parents []string
		for d := path.Dir(f.name); d != "/" && !dirs[d]; d = path.Dir(d) {
			parents = append([]string{d}, parents...)
			dirs[d] = true
		}
		for _, d := range parents {
			hdr := &tar.Header{Typeflag: tar.TypeDir, Name: strings.TrimPrefix(d, "/") + "/", Mode: 0o755, ModTime: modTime}
			if err := tw.WriteHeader(hdr); err != nil {
				return fmt.Errorf("write %s: %w", d, err)
			}
		}
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     strings.TrimPrefix(f.name, "/"),
			Mode:     0o644,
			Size:     int64(len(f.content)),
			ModTime:  modTime,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write %s: %w", f.name, err)
		}
		if _, err := tw.Write(f.content); err != nil {
			return fmt.Errorf("write %s: %w", f.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("finish archive: %w", err)
	}
	return nil
}

// rewriteState is what rewrite remembers about the entries it dropped.
type rewriteState struct {
	// Content of the dropped regular files, and headers of the other
	// dropped entries, for hard links pointing at them.
	dropped      map[string][]byte
	droppedHdr   map[string]*tar.Header
	droppedBytes int64
	// Entries dropped by rw.dropped, for hard links pointing at them.
	excluded map[string]bool
}

// entry copies one entry of the archive, or drops it.
func (rw *rewriter) entry(hdr *tar.Header, tr *tar.Reader, tw *tar.Writer, st *rewriteState) error {
	name := path.Clean("/" + hdr.Name)
	if isReplaced(hdr.Name) {
		switch hdr.Typeflag {
		case tar.TypeReg:
			st.droppedBytes += hdr.Size
			if st.droppedBytes > maxDroppedBytes {
				return fmt.Errorf("the image's own %s is too large", name)
			}
			b, err := io.ReadAll(tr)
			if err != nil {
				return fmt.Errorf("read archive entry %s: %w", hdr.Name, err)
			}
			st.dropped[name] = b
			delete(st.droppedHdr, name)
		case tar.TypeDir:
			delete(st.dropped, name)
			delete(st.droppedHdr, name)
		default: // symlink, hard link, FIFO, device
			st.droppedHdr[name] = hdr
			delete(st.dropped, name)
		}
		return nil
	}
	if rw.dropped(hdr) {
		st.excluded[name] = true
		return nil
	}
	target := path.Clean("/" + hdr.Linkname)
	if hdr.Typeflag == tar.TypeLink && st.excluded[target] {
		log.Warnf("dropping %s too: it is a hard link to the excluded %s", name, target)
		st.excluded[name] = true
		return nil
	}
	rw.normalize(hdr)
	if hdr.Typeflag == tar.TypeLink {
		if content, ok := st.dropped[target]; ok {
			// The target of this hard link was dropped: keep the file
			// as a regular file with the original content.
			reg := *hdr
			reg.Name = strings.TrimSuffix(reg.Name, "/")
			reg.Typeflag = tar.TypeReg
			reg.Linkname = ""
			reg.Size = int64(len(content))
			reg.Format = tar.FormatUnknown // the writer picks one that fits
			if err := tw.WriteHeader(&reg); err != nil {
				return fmt.Errorf("write archive entry %s: %w", hdr.Name, err)
			}
			if _, err := tw.Write(content); err != nil {
				return fmt.Errorf("write archive entry %s: %w", hdr.Name, err)
			}
			return nil
		}
		if orig, ok := st.droppedHdr[target]; ok {
			// Another name of a dropped symlink, FIFO or device: write
			// a copy of it under this name.
			cp := *orig
			cp.Name = strings.TrimSuffix(hdr.Name, "/")
			// The name may need another format than the original's.
			cp.Format = tar.FormatUnknown
			rw.normalize(&cp)
			if err := tw.WriteHeader(&cp); err != nil {
				return fmt.Errorf("write archive entry %s: %w", hdr.Name, err)
			}
			return nil
		}
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write archive entry %s: %w", hdr.Name, err)
	}
	if _, err := io.Copy(tw, tr); err != nil {
		return fmt.Errorf("write archive entry %s: %w", hdr.Name, err)
	}
	return nil
}

// isNoSuchImage reports whether err says that the image does not exist,
// as opposed to other 404s (an image without the requested platform, a
// proxy answering for an unknown API version, ...).
func isNoSuchImage(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound &&
		strings.Contains(strings.ToLower(apiErr.Message), "no such image")
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
