package docker

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
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
			return fmt.Errorf("image %s not found locally; pull or build it first (e.g. `docker pull %s`): %w", image, image, err)
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
	files := []extraFile{
		{name: environ.EnvFilePath, content: encodeEnvFile(cfg.Env)},
		{name: imageconfig.Path, content: cfgFile},
	}

	body, err := api.ContainerExport(ctx, id)
	if err != nil {
		return fmt.Errorf("export container %s: %w", shortID(id), err)
	}
	defer body.Close()

	if err := rewriteArchive(body, w, files); err != nil {
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

// rewriteArchive copies the tar stream r to w, dropping the entries of
// replacedPaths and appending files (and their parent directories).
//
// archive/tar reports the end of the input at an entry boundary like the
// end-of-archive marker (two zero blocks), so a stream cut cleanly between
// two entries would look complete. The marker is therefore checked
// explicitly: the stream must end with at least 1024 zero bytes.
func rewriteArchive(r io.Reader, w io.Writer, files []extraFile) error {
	zr := &zeroTail{r: r}
	tr := tar.NewReader(zr)
	tw := tar.NewWriter(w)
	// Content of the dropped regular files, for hard links pointing at them.
	dropped := map[string][]byte{}
	var droppedBytes int64
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
		if isReplaced(hdr.Name) {
			if hdr.Typeflag == tar.TypeReg {
				droppedBytes += hdr.Size
				if droppedBytes > maxDroppedBytes {
					return fmt.Errorf("the image's own %s is too large", path.Clean("/"+hdr.Name))
				}
				b, err := io.ReadAll(tr)
				if err != nil {
					return fmt.Errorf("read archive entry %s: %w", hdr.Name, err)
				}
				dropped[path.Clean("/"+hdr.Name)] = b
			}
			continue
		}
		if content, ok := dropped[path.Clean("/"+hdr.Linkname)]; ok && hdr.Typeflag == tar.TypeLink {
			// The target of this hard link was dropped: keep the file
			// as a regular file with the original content.
			reg := *hdr
			reg.Typeflag = tar.TypeReg
			reg.Linkname = ""
			reg.Size = int64(len(content))
			if err := tw.WriteHeader(&reg); err != nil {
				return fmt.Errorf("write archive entry %s: %w", hdr.Name, err)
			}
			if _, err := tw.Write(content); err != nil {
				return fmt.Errorf("write archive entry %s: %w", hdr.Name, err)
			}
			continue
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write archive entry %s: %w", hdr.Name, err)
		}
		if _, err := io.Copy(tw, tr); err != nil { //nolint:gosec // G110: an uncompressed stream copied as is, not decompressed
			return fmt.Errorf("write archive entry %s: %w", hdr.Name, err)
		}
	}

	modTime := time.Now().Truncate(time.Second)
	dirs := map[string]bool{}
	for _, f := range files {
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
