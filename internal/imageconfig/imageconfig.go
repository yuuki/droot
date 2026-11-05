// Package imageconfig is the image configuration that `droot export` saves
// in /.droot/config.json and that `droot run` uses as defaults: the image's
// environment, working directory, user, entrypoint and command.
//
// The file complements /.drootenv, which is kept for older droot versions
// and cannot represent every environment variable.
package imageconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/yuuki/droot/internal/log"
	"github.com/yuuki/droot/internal/osutil"
)

// Dir is the directory of the archive reserved for droot.
const Dir = "/.droot"

// Path is the path of the configuration inside the container root.
const Path = Dir + "/config.json"

// Version is the version of the format written by this droot. Later versions
// may add fields; droot ignores the fields it does not know.
const Version = 1

// maxSize bounds the size of the file read by Read.
const maxSize = 16 << 20

// Config is the content of /.droot/config.json. Its field names follow the
// image configuration of the Docker Engine API.
type Config struct {
	Version int `json:"Version"`
	// Image is the image reference given to `droot export` and ImageID
	// the ID of the image, for information.
	Image   string `json:"Image,omitempty"`
	ImageID string `json:"ImageID,omitempty"`

	Env        []string `json:"Env,omitempty"`
	WorkingDir string   `json:"WorkingDir,omitempty"`
	User       string   `json:"User,omitempty"`
	Entrypoint []string `json:"Entrypoint,omitempty"`
	Cmd        []string `json:"Cmd,omitempty"`
	// StopSignal is the signal that should stop the command, e.g. SIGTERM.
	// droot does not supervise COMMAND: it is for the process manager
	// (e.g. KillSignal= of a systemd unit).
	StopSignal string `json:"StopSignal,omitempty"`
}

// Encode returns the configuration as indented JSON.
func (c *Config) Encode() ([]byte, error) {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Read reads the configuration of the container root. It returns nil and no
// error when the file does not exist (an archive exported by droot 0.7 or
// extracted from elsewhere).
func Read(root *osutil.Root) (*Config, error) {
	f, err := root.OpenFile(Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", f.Name(), err)
	}
	if len(b) > maxSize {
		return nil, fmt.Errorf("read %s: larger than %d bytes", f.Name(), maxSize)
	}
	return Parse(b, f.Name())
}

// Parse decodes the content of a configuration file read from name.
func Parse(b []byte, name string) (*Config, error) {
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if c.Version < 1 {
		return nil, fmt.Errorf("read %s: missing or invalid Version", name)
	}
	if c.Version > Version {
		log.Warnf("%s was written by a newer droot (format version %d): settings this version does not know are ignored", name, c.Version)
	}
	return &c, nil
}

// Command returns the command to run: args if given (they replace both the
// entrypoint and the command of the image, unlike `docker run`), otherwise
// the image's entrypoint followed by its command.
func (c *Config) Command(args []string) []string {
	if len(args) > 0 || c == nil {
		return args
	}
	return append(append([]string{}, c.Entrypoint...), c.Cmd...)
}
