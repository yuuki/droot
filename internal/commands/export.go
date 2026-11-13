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

	"golang.org/x/sys/unix"

	"github.com/yuuki/droot/internal/cli"
	"github.com/yuuki/droot/internal/docker"
	"github.com/yuuki/droot/internal/image"
	"github.com/yuuki/droot/internal/imageconfig"
	"github.com/yuuki/droot/internal/log"
	"github.com/yuuki/droot/internal/osutil"
)

func exportCommand() *cli.Command {
	return &cli.Command{
		Name:      "export",
		Usage:     "Export the file system of a container image as a tar archive",
		ArgsUsage: "IMAGE",
		Description: "Writes the file system of IMAGE as a tar archive, with the image's\n" +
			"configuration in /.droot/config.json and its environment in /.drootenv, for\n" +
			"'droot run'.\n\n" +
			"IMAGE is an image of a Docker daemon (NAME[:TAG], an ID, ...), or one of\n" +
			"these images, read without daemon:\n\n" +
			"  docker://[REGISTRY/]NAME[:TAG|@DIGEST]  an image of a registry (Docker Hub by default)\n" +
			"  docker-archive:FILE[:NAME:TAG]          an archive written by docker save\n" +
			"  oci:DIR[:NAME]                          an OCI image layout\n" +
			"  oci-archive:FILE[:NAME]                 a tar archive of an OCI image layout\n\n" +
			"From a daemon, droot creates a temporary container from IMAGE (without\n" +
			"starting it), streams its file system and removes the container. The daemon\n" +
			"is selected like the docker CLI does, from DOCKER_HOST, DOCKER_TLS_VERIFY,\n" +
			"DOCKER_CERT_PATH, DOCKER_CONTEXT or the current docker context.\n\n" +
			"Without daemon, droot applies the layers of the image itself, verifying their\n" +
			"digests. The credentials for registries are those of the docker CLI (docker\n" +
			"login); the layers are downloaded to $TMPDIR (default /tmp) for the export.",
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
				Usage:       "export the PLATFORM variant of a multi-platform image, e.g. linux/arm64 (default: the platform of the daemon, or the one droot runs on)",
			},
			{
				Name:  "pull",
				Kind:  cli.Bool,
				Usage: "pull IMAGE (for --platform) from its registry into the Docker daemon first, with the credentials of the docker CLI",
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
	if err := setup(cmd); err != nil {
		return err
	}
	if len(cmd.Args()) != 1 || cmd.Args()[0] == "" {
		return cmd.UsageError("exactly one image is required")
	}
	name := cmd.Args()[0]
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
	opts, err := exportOptions(cmd)
	if err != nil {
		return err
	}

	var exportImage func(io.Writer) error
	if image.IsReference(name) {
		if cmd.Bool("pull") {
			return cmd.UsageError("--pull is for the images of a Docker daemon: %s is read without daemon", name)
		}
		img, err := image.Open(ctx, name, image.Options{Platform: opts.Platform, Credentials: registryCredentials})
		if err != nil {
			return err
		}
		defer img.Close()
		exportImage = func(w io.Writer) error { return exportFlattened(ctx, img, w, opts) }
	} else {
		exportImage, err = daemonExport(ctx, cmd, name, opts)
		if err != nil {
			return err
		}
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
		p := newProgress(c, "exporting "+name, progressOutput())
		defer func() { p.end(err == nil) }()
		if err := exportImage(p); err != nil {
			c.abort()
			return err
		}
		return c.finish()
	}

	if toStdout {
		return writeBuffered(ctx, os.Stdout, export)
	}
	if err := writeFile(ctx, output, true, export); err != nil {
		return err
	}
	if checksum {
		return writeChecksum(ctx, output, sum)
	}
	return nil
}

// exportOptions returns the options of the archive given by --platform,
// --exclude, --drop-docker-files and --reproducible, which clamps the
// modification times to $SOURCE_DATE_EPOCH, if set.
func exportOptions(cmd *cli.Invocation) (docker.ExportOptions, error) {
	opts := docker.ExportOptions{
		Platform:        cmd.String("platform"),
		Exclude:         cmd.Strings("exclude"),
		DropDockerFiles: cmd.Bool("drop-docker-files"),
		Reproducible:    cmd.Bool("reproducible"),
	}
	if err := docker.ValidatePatterns(opts.Exclude); err != nil {
		return docker.ExportOptions{}, cmd.UsageError("%v", err)
	}
	if !opts.Reproducible {
		return opts, nil
	}
	if s, ok := os.LookupEnv("SOURCE_DATE_EPOCH"); ok && s != "" {
		epoch, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return docker.ExportOptions{}, fmt.Errorf("invalid SOURCE_DATE_EPOCH %q: want a number of seconds", s)
		}
		opts.ClampTime = time.Unix(epoch, 0)
	}
	return opts, nil
}

// daemonExport connects to the Docker daemon, pulls the image name into it
// for --pull, and returns the function that writes the file system of the
// image.
func daemonExport(ctx context.Context, cmd *cli.Invocation, name string, opts docker.ExportOptions) (func(io.Writer) error, error) {
	client, err := docker.NewClientFromEnv()
	if err != nil {
		return nil, err
	}
	if err := client.Ping(ctx); err != nil {
		return nil, err
	}
	if cmd.Bool("pull") {
		auth, err := docker.RegistryAuth(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("registry credentials: %w", err)
		}
		log.Infof("pulling %s", docker.WithDefaultTag(name))
		if err := client.ImagePull(ctx, name, opts.Platform, auth, func(s string) { log.Debugf("%s", s) }); err != nil {
			return nil, err
		}
	}
	return func(w io.Writer) error { return docker.ExportImage(ctx, client, name, w, opts) }, nil
}

// writeChecksum writes sum, the SHA-256 checksum of the archive written to
// output, to output.sha256, in the format of sha256sum.
func writeChecksum(ctx context.Context, output string, sum hash.Hash) error {
	line := fmt.Sprintf("%x  %s\n", sum.Sum(nil), filepath.Base(output))
	return writeFile(ctx, output+".sha256", false, func(w io.Writer) error {
		_, err := io.WriteString(w, line)
		return err
	})
}

// exportFlattened writes the file system of img, read without daemon, like
// docker.ExportImage.
func exportFlattened(ctx context.Context, img *image.Image, w io.Writer, opts docker.ExportOptions) error {
	c := img.Config.Config
	cfg := imageconfig.Config{
		Image: img.Name, ImageID: img.ID,
		Env: c.Env, WorkingDir: c.WorkingDir, User: c.User,
		Entrypoint: c.Entrypoint, Cmd: c.Cmd, StopSignal: c.StopSignal,
	}
	produce := func(w io.Writer) error { return img.Flatten(ctx, w) }
	return docker.WriteArchive(ctx, produce, w, cfg, img.Config.Created, opts)
}

// registryCredentials returns the credentials of the docker CLI for a
// registry.
func registryCredentials(ctx context.Context, host string) (*image.Credentials, error) {
	c, err := docker.RegistryCredentials(ctx, host)
	if err != nil || c == nil {
		return nil, err
	}
	return &image.Credentials{
		Username: c.Username, Password: c.Password,
		IdentityToken: c.IdentityToken, RegistryToken: c.RegistryToken,
	}, nil
}

// progressOutput returns where the progress of an export is shown: STDERR
// if it is a terminal, unless the messages are quiet, JSON or debug output.
func progressOutput() *os.File {
	if log.IsDebug() || log.IsQuiet() || log.IsJSON() {
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

// writeFile writes the output of droot export or droot unit to path with the
// same result as os.Create (droot <= 0.7), but atomically: the data goes to
// a temporary file in the same directory, which replaces the file only on
// success, so that a failed or interrupted export never leaves a truncated
// archive behind.
//
//   - New files get mode 0666 minus the umask; an existing file keeps its
//     mode and owner.
//   - A symlink is followed: its target is written, the link is kept.
//   - A file with several hard links is truncated and written in place (not
//     atomically) so that the links are kept.
//   - Non-regular files such as /dev/stdout or named pipes are written
//     directly; a terminal is refused if archive is set (a tar archive).
//   - A link whose target is not the path of the file it leads to, such as
//     /proc/self/fd/1 -> pipe:[1234] (/dev/stdout when STDOUT is a pipe), is
//     opened as is, the kernel resolving it; a regular file is written in
//     place.
func writeFile(ctx context.Context, path string, archive bool, write func(io.Writer) error) error {
	path, viaLink, err := followSymlinks(path)
	if err != nil {
		return err
	}
	fi, err := os.Stat(path)
	switch {
	case err != nil && !viaLink:
		// No file at path, or none that can be stat'ed: a new file.
		return writeAtomically(ctx, path, nil, write)
	case err != nil:
		return err
	case fi.IsDir():
		return errors.New(path + " is a directory")
	case !fi.Mode().IsRegular():
		return writeDirectly(ctx, path, fi, archive, write)
	case viaLink:
		// Only the link leads to the file, e.g. a deleted file: no path to
		// replace atomically.
		return writeInPlace(ctx, path, write)
	}
	if _, _, nlink, ok := osutil.FileOwner(fi); ok && nlink > 1 {
		return writeInPlace(ctx, path, write)
	}
	return writeAtomically(ctx, path, fi, write)
}

// writeDirectly writes to the file fi at path that is not a regular file,
// such as /dev/stdout or a named pipe, refusing a terminal for an archive.
func writeDirectly(ctx context.Context, path string, fi fs.FileInfo, archive bool, write func(io.Writer) error) error {
	f, err := openDirectly(ctx, path, fi)
	if err != nil {
		return err
	}
	defer f.Close()
	if archive && osutil.IsTerminal(f) {
		return errors.New("refusing to write a tar archive to a terminal")
	}
	return writeBuffered(ctx, f, write)
}

// openDirectly opens the file fi at path, which is not a regular file, for
// writing. open(2) cannot open a socket (ENXIO): a socket that is a
// descriptor of droot, such as /dev/stdout when STDOUT is a socket, is
// duplicated instead.
func openDirectly(ctx context.Context, path string, fi fs.FileInfo) (*os.File, error) {
	if fi.Mode()&fs.ModeSocket != 0 {
		if fd, ok := ownDescriptor(path); ok {
			nfd, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
			if err != nil {
				return nil, &fs.PathError{Op: "dup", Path: path, Err: err}
			}
			return os.NewFile(uintptr(nfd), path), nil
		}
	}
	return openCancelable(ctx, path)
}

// ownDescriptor returns N if path is /proc/self/fd/N, the descriptor N of
// droot, or another name of it, such as /dev/fd/N.
func ownDescriptor(path string) (int, bool) {
	fd, err := strconv.Atoi(filepath.Base(path))
	if err != nil || fd < 0 {
		return 0, false
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return 0, false
	}
	self, err := filepath.EvalSymlinks("/proc/self/fd")
	return fd, err == nil && dir == self
}

// writeInPlace truncates the regular file at path and writes it in place.
func writeInPlace(ctx context.Context, path string, write func(io.Writer) error) error {
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

// writeAtomically writes path through a temporary file in the same
// directory, which replaces it on success and is removed on failure. The
// temporary file gets the mode and the owner of fi, the file at path, if
// it is not nil.
func writeAtomically(ctx context.Context, path string, fi fs.FileInfo, write func(io.Writer) error) (err error) {
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

	if fi != nil {
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
// dangling final symlink, whose target is then created. It stops at a link
// that the kernel resolves to another file than its target read as a path,
// and reports it with viaLink: a link of /proc to a pipe or a socket
// (/proc/self/fd/1 -> pipe:[1234]) or to a deleted file
// (/proc/self/fd/3 -> "/tmp/out (deleted)").
func followSymlinks(path string) (resolved string, viaLink bool, err error) {
	for range 40 {
		fi, err := os.Lstat(path)
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			return path, false, nil
		}
		dest, err := os.Readlink(path)
		if err != nil {
			return "", false, err
		}
		if !filepath.IsAbs(dest) {
			dest = filepath.Join(filepath.Dir(path), dest)
		}
		if opensElsewhere(path, dest) {
			return path, true, nil
		}
		path = dest
	}
	return "", false, &fs.PathError{Op: "open", Path: path, Err: errors.New("too many levels of symbolic links")}
}

// opensElsewhere reports whether the kernel resolves the symlink link to an
// existing file that dest, the target of link, is not.
func opensElsewhere(link, dest string) bool {
	opened, err := os.Stat(link)
	if err != nil {
		return false // A dangling link: dest is created.
	}
	named, err := os.Stat(dest)
	return err != nil || !os.SameFile(opened, named)
}
