package docker

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/yuuki/droot/environ"
	"github.com/yuuki/droot/log"
)

// API is the subset of the Docker Engine API used by ExportImage.
// *Client implements it; tests use a fake.
type API interface {
	ContainerCreate(ctx context.Context, cfg *ContainerConfig, platform string) (string, error)
	ContainerInspect(ctx context.Context, id string) (*ContainerInfo, error)
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

// ExportImage writes the root filesystem of image to w as a tar archive.
//
// The image's environment variables are stored in /.drootenv inside the
// archive so that `droot run` can restore them. The image is never run: a
// container is created (but not started), exported and then removed. This
// works for any image, including ones without /bin/sh such as distroless or
// FROM scratch images.
func ExportImage(ctx context.Context, api API, image string, w io.Writer, opts ExportOptions) (err error) {
	id, err := api.ContainerCreate(ctx, &ContainerConfig{
		Image:      image,
		Entrypoint: placeholderCmd,
		Labels:     map[string]string{exportLabel: "export"},
	}, opts.Platform)
	if err != nil {
		if IsNotFound(err) {
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

	info, err := api.ContainerInspect(ctx, id)
	if err != nil {
		return fmt.Errorf("inspect container %s: %w", shortID(id), err)
	}
	envFile := encodeEnvFile(info.Config.Env)

	body, err := api.ContainerExport(ctx, id)
	if err != nil {
		return fmt.Errorf("export container %s: %w", shortID(id), err)
	}
	defer body.Close()

	if err := rewriteArchive(body, w, envFile); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	return nil
}

// encodeEnvFile renders env in the format read by environ.ParseEnv.
// Variables whose value contains a newline cannot be represented and are
// skipped with a warning.
func encodeEnvFile(env []string) []byte {
	var b strings.Builder
	for _, e := range env {
		if strings.ContainsAny(e, "\r\n") {
			k, _, _ := strings.Cut(e, "=")
			log.Warnf("skipping environment variable %s: multi-line values are not supported in %s", k, environ.EnvFilePath)
			continue
		}
		b.WriteString(e)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// isEnvFile reports whether a tar entry name refers to /.drootenv.
func isEnvFile(name string) bool {
	return path.Clean("/"+name) == environ.EnvFilePath
}

// rewriteArchive copies the tar stream r to w, replacing any existing
// /.drootenv entry with one containing env.
func rewriteArchive(r io.Reader, w io.Writer, env []byte) error {
	tr := tar.NewReader(r)
	tw := tar.NewWriter(w)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read exported archive: %w", err)
		}
		if isEnvFile(hdr.Name) {
			continue
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write archive entry %s: %w", hdr.Name, err)
		}
		if _, err := io.Copy(tw, tr); err != nil { //nolint:gosec // G110: an uncompressed stream copied as is, not decompressed
			return fmt.Errorf("write archive entry %s: %w", hdr.Name, err)
		}
	}

	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     strings.TrimPrefix(environ.EnvFilePath, "/"),
		Mode:     0o644,
		Size:     int64(len(env)),
		ModTime:  time.Now().Truncate(time.Second),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write %s: %w", environ.EnvFilePath, err)
	}
	if _, err := tw.Write(env); err != nil {
		return fmt.Errorf("write %s: %w", environ.EnvFilePath, err)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("finish archive: %w", err)
	}
	return nil
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
