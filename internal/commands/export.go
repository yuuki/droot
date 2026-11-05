package commands

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"github.com/yuuki/droot/internal/docker"
	"github.com/yuuki/droot/internal/osutil"
)

func exportCommand() *cli.Command {
	return &cli.Command{
		Name:      "export",
		Usage:     "Export the file system of a Docker image as a tar archive",
		ArgsUsage: "IMAGE[:TAG]",
		Description: "Creates a temporary container from IMAGE (without starting it), streams its\n" +
			"file system as a tar archive and removes the container. The image's\n" +
			"environment variables are saved as /.drootenv in the archive so that\n" +
			"'droot run' can restore them.\n\n" +
			"The Docker daemon is selected like the docker CLI does, from DOCKER_HOST,\n" +
			"DOCKER_TLS_VERIFY, DOCKER_CERT_PATH, DOCKER_CONTEXT or the current docker context.",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "output",
				Aliases: []string{"o"},
				Usage:   "write the archive to `FILE` instead of STDOUT (written atomically)",
			},
			&cli.StringFlag{
				Name:  "platform",
				Usage: "export the `PLATFORM` variant of a multi-platform image, e.g. linux/amd64",
			},
		},
		Action: doExport,
	}
}

func doExport(ctx context.Context, cmd *cli.Command) error {
	setup(cmd)
	if cmd.NArg() != 1 || cmd.Args().First() == "" {
		return usageError(cmd, "exactly one image is required")
	}
	image := cmd.Args().First()
	output := cmd.String("output")
	toStdout := output == "" || output == "-"

	if toStdout && osutil.IsTerminal(os.Stdout) {
		return usageError(cmd, "refusing to write a tar archive to a terminal; use -o FILE or redirect stdout")
	}

	client, err := docker.NewClientFromEnv()
	if err != nil {
		return err
	}
	if err := client.Ping(ctx); err != nil {
		return err
	}
	opts := docker.ExportOptions{Platform: cmd.String("platform")}
	export := func(w io.Writer) error {
		return docker.ExportImage(ctx, client, image, w, opts)
	}

	if toStdout {
		return writeBuffered(ctx, os.Stdout, export)
	}
	return writeFile(ctx, output, export)
}

func writeBuffered(ctx context.Context, w io.Writer, write func(io.Writer) error) error {
	bw := bufio.NewWriterSize(cancelableWriter{ctx: ctx, w: w}, 1<<20)
	if err := write(bw); err != nil {
		return err
	}
	return bw.Flush()
}

// cancelableWriter returns from Write as soon as ctx is cancelled, even when
// the underlying write blocks because the reader of a pipe stalled, so that
// droot can still remove its temporary container on Ctrl-C. A blocked write
// is abandoned (the process exits shortly after).
type cancelableWriter struct {
	ctx context.Context
	w   io.Writer
}

func (c cancelableWriter) Write(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := c.w.Write(p)
		done <- result{n, err}
	}()
	select {
	case r := <-done:
		return r.n, r.err
	case <-c.ctx.Done():
		return 0, c.ctx.Err()
	}
}

// openCancelable opens path for writing, giving up when ctx is cancelled:
// opening a FIFO blocks until a reader appears.
func openCancelable(ctx context.Context, path string) (*os.File, error) {
	type result struct {
		f   *os.File
		err error
	}
	done := make(chan result, 1)
	go func() {
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		done <- result{f, err}
	}()
	select {
	case r := <-done:
		return r.f, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// writeFile writes the archive to path with the same result as os.Create
// (droot <= 0.7), but atomically: the data goes to a temporary file in the
// same directory, which replaces the file only on success, so that a failed
// or interrupted export never leaves a truncated archive behind.
//
//   - New files get mode 0666 minus the umask; an existing file keeps its
//     mode and owner.
//   - A symlink is followed: its target is written, the link is kept.
//   - A file with several hard links is truncated and written in place (not
//     atomically) so that the links are kept.
//   - Non-regular files such as /dev/stdout or named pipes are written
//     directly.
func writeFile(ctx context.Context, path string, write func(io.Writer) error) (err error) {
	path, err = followSymlinks(path)
	if err != nil {
		return err
	}
	fi, statErr := os.Stat(path)
	if statErr == nil && !fi.Mode().IsRegular() {
		if fi.IsDir() {
			return errors.New(path + " is a directory")
		}
		f, err := openCancelable(ctx, path)
		if err != nil {
			return err
		}
		defer f.Close()
		if osutil.IsTerminal(f) {
			return errors.New("refusing to write a tar archive to a terminal")
		}
		return writeBuffered(ctx, f, write)
	}
	if statErr == nil {
		if _, _, nlink, ok := osutil.FileOwner(fi); ok && nlink > 1 {
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
			if err != nil {
				return err
			}
			if err := writeBuffered(ctx, f, write); err != nil {
				_ = f.Close()
				return err
			}
			return f.Close()
		}
	}

	tmp, err := createTemp(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()

	if statErr == nil {
		if err := tmp.Chmod(fi.Mode().Perm()); err != nil {
			return err
		}
		if uid, gid, _, ok := osutil.FileOwner(fi); ok {
			if err := tmp.Chown(uid, gid); err != nil && !errors.Is(err, fs.ErrPermission) {
				return err
			}
		}
	}
	if err := writeBuffered(ctx, tmp, write); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// createTemp creates a new file in dir with mode 0666 minus the umask, like
// os.Create (os.CreateTemp uses 0600). The name is short so that it fits
// even when the output file name is close to NAME_MAX.
func createTemp(dir string) (*os.File, error) {
	for range 100 {
		var rnd [6]byte
		_, _ = rand.Read(rnd[:])
		name := filepath.Join(dir, ".droot-"+hex.EncodeToString(rnd[:])+".tmp")
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return f, err
	}
	return nil, errors.New("cannot create a temporary file in " + dir)
}

// followSymlinks resolves path like open(2) does for os.Create, including a
// dangling final symlink, whose target is then created.
func followSymlinks(path string) (string, error) {
	for range 40 {
		fi, err := os.Lstat(path)
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			return path, nil
		}
		dest, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(dest) {
			dest = filepath.Join(filepath.Dir(path), dest)
		}
		path = dest
	}
	return "", &fs.PathError{Op: "open", Path: path, Err: errors.New("too many levels of symbolic links")}
}
