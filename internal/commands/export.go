package commands

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/yuuki/droot/internal/cli"
	"github.com/yuuki/droot/internal/docker"
	"github.com/yuuki/droot/internal/log"
	"github.com/yuuki/droot/internal/osutil"
)

func exportCommand() *cli.Command {
	return &cli.Command{
		Name:      "export",
		Usage:     "Export the file system of a Docker image as a tar archive",
		ArgsUsage: "IMAGE[:TAG]",
		Description: "Creates a temporary container from IMAGE (without starting it), streams its\n" +
			"file system as a tar archive and removes the container. The image's\n" +
			"configuration is saved as /.droot/config.json and its environment as\n" +
			"/.drootenv in the archive, for 'droot run'.\n\n" +
			"The Docker daemon is selected like the docker CLI does, from DOCKER_HOST,\n" +
			"DOCKER_TLS_VERIFY, DOCKER_CERT_PATH, DOCKER_CONTEXT or the current docker context.",
		Flags: []*cli.Flag{
			{
				Name:        "output",
				Aliases:     []string{"o"},
				Kind:        cli.String,
				Placeholder: "FILE",
				Usage:       "write the archive to FILE instead of STDOUT (written atomically)",
				Complete:    "file",
			},
			{
				Name:        "platform",
				Kind:        cli.String,
				Placeholder: "PLATFORM",
				Usage:       "export the PLATFORM variant of a multi-platform image, e.g. linux/amd64",
			},
			{
				Name:        "compress",
				Kind:        cli.String,
				Placeholder: "ALGO",
				Usage:       "compress the archive with gzip, or zstd (with the zstd command)",
			},
			{
				Name:  "checksum",
				Kind:  cli.Bool,
				Usage: "also write the SHA-256 checksum of the archive to FILE.sha256 (sha256sum format; needs -o)",
			},
			{
				Name:  "reproducible",
				Kind:  cli.Bool,
				Usage: "make the archive depend only on the image: modification times later than $SOURCE_DATE_EPOCH or the image's creation time are set to it",
			},
			{
				Name:        "exclude",
				Kind:        cli.Strings,
				Placeholder: "PATTERN",
				Usage:       "leave out the files matching PATTERN and everything below matching directories; a PATTERN with a / matches absolute paths (/usr/share/doc), one without a / base names (*.pyc)",
			},
			{
				Name:  "drop-docker-files",
				Kind:  cli.Bool,
				Usage: "leave out the files Docker adds to every container: /.dockerenv and the empty /etc/hostname, /etc/hosts, /etc/resolv.conf and /dev/console",
			},
		},
		Action: doExport,
	}
}

func doExport(ctx context.Context, cmd *cli.Invocation) error {
	setup(cmd)
	if len(cmd.Args()) != 1 || cmd.Args()[0] == "" {
		return cmd.UsageError("exactly one image is required")
	}
	image := cmd.Args()[0]
	output := cmd.String("output")
	toStdout := output == "" || output == "-"

	if toStdout && osutil.IsTerminal(os.Stdout) {
		return cmd.UsageError("refusing to write a tar archive to a terminal; use -o FILE or redirect stdout")
	}
	checksum := cmd.Bool("checksum")
	if checksum && toStdout {
		return cmd.UsageError("--checksum needs -o FILE")
	}
	algo := cmd.String("compress")
	if algo != "" && !slices.Contains(compressions, algo) {
		return cmd.UsageError("unknown compression %q: want gzip or zstd", algo)
	}
	opts := docker.ExportOptions{
		Platform:        cmd.String("platform"),
		Exclude:         cmd.Strings("exclude"),
		DropDockerFiles: cmd.Bool("drop-docker-files"),
		Reproducible:    cmd.Bool("reproducible"),
	}
	if err := docker.ValidatePatterns(opts.Exclude); err != nil {
		return cmd.UsageError("%v", err)
	}
	if opts.Reproducible {
		if s, ok := os.LookupEnv("SOURCE_DATE_EPOCH"); ok && s != "" {
			epoch, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid SOURCE_DATE_EPOCH %q: want a number of seconds", s)
			}
			opts.ClampTime = time.Unix(epoch, 0)
		}
	}

	client, err := docker.NewClientFromEnv()
	if err != nil {
		return err
	}
	if err := client.Ping(ctx); err != nil {
		return err
	}
	var sum hash.Hash
	export := func(w io.Writer) (err error) {
		if checksum {
			sum = sha256.New()
			w = io.MultiWriter(w, sum)
		}
		c, err := newCompressor(ctx, w, algo)
		if err != nil {
			return err
		}
		p := newProgress(c, "exporting "+image, progressOutput())
		defer func() { p.end(err == nil) }()
		if err := docker.ExportImage(ctx, client, image, p, opts); err != nil {
			c.abort()
			return err
		}
		return c.finish()
	}

	if toStdout {
		return writeBuffered(ctx, os.Stdout, export)
	}
	if err := writeFile(ctx, output, export); err != nil {
		return err
	}
	if checksum {
		line := fmt.Sprintf("%x  %s\n", sum.Sum(nil), filepath.Base(output))
		return writeFile(ctx, output+".sha256", func(w io.Writer) error {
			_, err := io.WriteString(w, line)
			return err
		})
	}
	return nil
}

// progressOutput returns where the progress of an export is shown: STDERR
// if it is a terminal, and the output is not in debug mode.
func progressOutput() *os.File {
	if log.IsDebug() {
		return nil
	}
	return os.Stderr
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
