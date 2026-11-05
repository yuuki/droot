package commands

import (
	"io"
	"os"
	"path/filepath"

	"github.com/yuuki/droot/internal/cli"
)

// Environment lists the environment variables droot reads, for the man page.
var Environment = [][2]string{
	{"DROOT_DEBUG, DROOT_QUIET, DROOT_LOG_FORMAT", "Defaults of --debug, --quiet and --log-format."},
	{"DOCKER_HOST", "Address of the Docker daemon used by droot export: unix:///path, tcp://host:port, https://host:port or ssh://[user@]host[:port]."},
	{"DOCKER_TLS_VERIFY, DOCKER_TLS, DOCKER_CERT_PATH", "TLS for tcp:// hosts, verifying the daemon's certificate or not, with ca.pem, cert.pem and key.pem from DOCKER_CERT_PATH (default ~/.docker)."},
	{"DOCKER_CONTEXT, DOCKER_CONFIG", "The docker context to use, and the directory of the docker CLI's configuration (default ~/.docker), which holds the current context and the registry credentials used by droot export --pull."},
	{"DOCKER_API_VERSION", "Pins the version of the Docker Engine API."},
	{"SOURCE_DATE_EPOCH", "Time (in seconds since the epoch) to which droot export --reproducible clamps modification times."},
}

// docFiles are the generated documentation files, relative to the
// documentation directory.
var docFiles = map[string]func(app *cli.App, w io.Writer) error{
	"cli.md":  func(app *cli.App, w io.Writer) error { return app.WriteMarkdown(w) },
	"droot.1": func(app *cli.App, w io.Writer) error { return app.WriteMan(w, Environment) },
}

// WriteDocs writes the Markdown reference and the man page of droot to dir.
func WriteDocs(dir string) error {
	app := NewApp("")
	for name, write := range docFiles {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if err := write(app, f); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}
