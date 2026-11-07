// Package image reads container images without a container engine: from a
// registry, a `docker save` archive or an OCI image layout, and flattens
// their layers into the file system of the image.
//
// References are written like skopeo's:
//
//	docker://[REGISTRY/]REPOSITORY[:TAG|@DIGEST]   a registry (Docker Hub by default)
//	docker-archive:FILE[:NAME:TAG]                  a `docker save` archive
//	oci:DIR[:NAME]                                  an OCI image layout directory
//	oci-archive:FILE[:NAME]                         a tar archive of an OCI image layout
package image

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/yuuki/droot/internal/log"
)

// Media types of manifests, indexes and configurations.
const (
	MediaTypeOCIIndex       = "application/vnd.oci.image.index.v1+json"
	MediaTypeOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeOCIConfig      = "application/vnd.oci.image.config.v1+json"
	MediaTypeDockerList     = "application/vnd.docker.distribution.manifest.list.v2+json"
	MediaTypeDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	MediaTypeDockerConfig   = "application/vnd.docker.container.image.v1+json"
)

// maxManifestSize bounds the size of the manifests, indexes and
// configurations read into memory.
const maxManifestSize = 8 << 20

// maxNesting bounds the depth of nested indexes.
const maxNesting = 4

// Transports lists the reference prefixes handled by this package.
var Transports = []string{"docker://", "docker-archive:", "oci:", "oci-archive:"}

// IsReference reports whether ref names an image of one of the transports
// of this package rather than an image of a container engine.
func IsReference(ref string) bool {
	for _, t := range Transports {
		if strings.HasPrefix(ref, t) {
			return true
		}
	}
	return false
}

// Descriptor points to a blob, as in the OCI image specification.
type Descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Platform    *Platform         `json:"platform,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// manifest is an image manifest or an index, OCI or Docker.
type manifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Config        Descriptor   `json:"config"`
	Layers        []Descriptor `json:"layers"`
	Manifests     []Descriptor `json:"manifests"` // indexes
}

// Config is the part of the image configuration droot uses.
type Config struct {
	Created string `json:"created"`
	Config  struct {
		Env        []string `json:"Env"`
		WorkingDir string   `json:"WorkingDir"`
		User       string   `json:"User"`
		Entrypoint []string `json:"Entrypoint"`
		Cmd        []string `json:"Cmd"`
		StopSignal string   `json:"StopSignal"`
	} `json:"config"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant"`
	RootFS       struct {
		DiffIDs []string `json:"diff_ids"`
	} `json:"rootfs"`
}

func (c *Config) platform() Platform {
	return Platform{OS: c.OS, Architecture: c.Architecture, Variant: c.Variant}
}

// Image is an image opened from one of the transports.
type Image struct {
	// Name is the reference the image was opened with.
	Name string
	// ID is the digest of the configuration, like the image IDs of Docker.
	ID     string
	Config Config
	// Layers are the layers, the lowest first.
	Layers []*Layer
	// TempDir holds the temporary files of Flatten.
	TempDir string
	closers []func() error
}

// Close releases the resources of the image: open files and the layers
// downloaded from a registry.
func (img *Image) Close() error {
	var errs []error
	for i := len(img.closers) - 1; i >= 0; i-- {
		errs = append(errs, img.closers[i]())
	}
	img.closers = nil
	return errors.Join(errs...)
}

// Options are the options of Open.
type Options struct {
	// Platform selects the image of a multi-platform image, as
	// OS/ARCH[/VARIANT]; default linux and the architecture droot runs on.
	Platform string
	// Credentials returns the credentials for a registry host
	// ("docker.io" for Docker Hub), or nil for anonymous access. Used by
	// docker:// references.
	Credentials func(ctx context.Context, host string) (*Credentials, error)
	// TempDir is where the layers downloaded from a registry, and the
	// temporary files of Flatten, are stored; default os.TempDir().
	TempDir string
	// Transport, if not nil, is used for the requests to registries
	// (tests).
	Transport http.RoundTripper
}

// Credentials authenticate to a registry.
type Credentials struct {
	Username, Password string
	// IdentityToken is an OAuth2 refresh token; RegistryToken a bearer
	// token sent as is.
	IdentityToken, RegistryToken string
}

// Open opens the image referenced by ref, which must start with one of
// Transports. The caller must Close the image.
func Open(ctx context.Context, ref string, opts Options) (*Image, error) {
	want, err := ParsePlatform(opts.Platform)
	if err != nil {
		return nil, err
	}
	sel := selector{want: want, explicit: opts.Platform != ""}
	if opts.TempDir == "" {
		opts.TempDir = os.TempDir()
	}
	var img *Image
	switch {
	case strings.HasPrefix(ref, "docker://"):
		img, err = openRegistry(ctx, strings.TrimPrefix(ref, "docker://"), sel, opts)
	case strings.HasPrefix(ref, "docker-archive:"):
		img, err = openDockerArchive(ctx, strings.TrimPrefix(ref, "docker-archive:"), sel)
	case strings.HasPrefix(ref, "oci:"):
		img, err = openLayoutDir(ctx, strings.TrimPrefix(ref, "oci:"), sel)
	case strings.HasPrefix(ref, "oci-archive:"):
		img, err = openLayoutArchive(ctx, strings.TrimPrefix(ref, "oci-archive:"), sel)
	default:
		return nil, fmt.Errorf("unknown image reference %q: want one of the prefixes %s", ref, strings.Join(Transports, ", "))
	}
	if err != nil {
		return nil, err
	}
	img.Name = ref
	img.TempDir = opts.TempDir
	return img, nil
}

// store gives access to the manifests and blobs of an image source.
type store interface {
	// manifest returns the manifest or index d and its media type, if
	// known. For registries, d.Digest may be empty for the tag of the
	// reference.
	manifest(ctx context.Context, d Descriptor) ([]byte, string, error)
	// blob opens the blob d; reading it to the end verifies its digest.
	blob(ctx context.Context, d Descriptor) (io.ReadCloser, error)
}

// readAllLimited reads r to the end, failing if it is larger than max.
func readAllLimited(r io.Reader, max int64, what string) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", what, max)
	}
	return b, nil
}

// readConfig reads the configuration blob d.
func readConfig(ctx context.Context, s store, d Descriptor) (*Config, error) {
	rc, err := s.blob(ctx, d)
	if err != nil {
		return nil, fmt.Errorf("read the image configuration: %w", err)
	}
	defer rc.Close()
	b, err := readAllLimited(rc, maxManifestSize, "the image configuration")
	if err != nil {
		return nil, fmt.Errorf("read the image configuration: %w", err)
	}
	return parseConfig(b)
}

func parseConfig(b []byte) (*Config, error) {
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse the image configuration: %w", err)
	}
	return &c, nil
}

// resolve follows d, a manifest or an index, down to the image manifest
// selected by sel, and returns the image.
func resolve(ctx context.Context, s store, d Descriptor, sel selector) (*Image, error) {
	fromIndex := false
	for depth := 0; ; depth++ {
		if depth > maxNesting {
			return nil, errors.New("too many nested indexes")
		}
		m, mt, err := readManifest(ctx, s, d)
		if err != nil {
			return nil, err
		}
		switch mt {
		case MediaTypeOCIIndex, MediaTypeDockerList:
			if d, err = sel.choose(m.Manifests); err != nil {
				return nil, err
			}
			fromIndex = true
			continue
		case MediaTypeOCIManifest, MediaTypeDockerManifest:
		case "application/vnd.docker.distribution.manifest.v1+json", "application/vnd.docker.distribution.manifest.v1+prettyjws":
			return nil, errors.New("the image has a Docker schema 1 manifest, which is not supported: push it again with a recent Docker")
		default:
			return nil, fmt.Errorf("unsupported manifest media type %q", mt)
		}
		switch m.Config.MediaType {
		case MediaTypeOCIConfig, MediaTypeDockerConfig, "":
		default:
			return nil, fmt.Errorf("not a container image: its configuration is of type %q", m.Config.MediaType)
		}
		cfg, err := readConfig(ctx, s, m.Config)
		if err != nil {
			return nil, err
		}
		if !fromIndex {
			if err := sel.check(cfg); err != nil {
				return nil, err
			}
		}
		return newImage(s, m, cfg)
	}
}

// readManifest reads the manifest or index d, and returns it with its media
// type: the one it gives, or else the one of the store, guessed from its
// content when it is missing or application/json.
func readManifest(ctx context.Context, s store, d Descriptor) (*manifest, string, error) {
	b, mt, err := s.manifest(ctx, d)
	if err != nil {
		return nil, "", err
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, "", fmt.Errorf("parse manifest %s: %w", d.Digest, err)
	}
	if m.MediaType != "" {
		mt = m.MediaType
	}
	if mt == "" || mt == "application/json" {
		mt = MediaTypeOCIManifest
		if m.Manifests != nil {
			mt = MediaTypeOCIIndex
		}
	}
	return &m, mt, nil
}

// newImage returns the image of the image manifest m, whose configuration
// is cfg, with its layers read from s.
func newImage(s store, m *manifest, cfg *Config) (*Image, error) {
	img := &Image{ID: m.Config.Digest, Config: *cfg}
	for _, l := range m.Layers {
		if strings.Contains(l.MediaType, "nondistributable") || strings.Contains(l.MediaType, "foreign") {
			return nil, fmt.Errorf("layer %s is not distributable (%s): not supported", l.Digest, l.MediaType)
		}
		img.Layers = append(img.Layers, &Layer{Digest: l.Digest, Size: l.Size, open: blobOpener(s, l)})
	}
	if err := setDiffIDs(img); err != nil {
		return nil, err
	}
	return img, nil
}

func blobOpener(s store, d Descriptor) func(context.Context) (io.ReadCloser, error) {
	return func(ctx context.Context) (io.ReadCloser, error) { return s.blob(ctx, d) }
}

// setDiffIDs gives the layers the digests of their uncompressed content
// listed in the configuration.
func setDiffIDs(img *Image) error {
	ids := img.Config.RootFS.DiffIDs
	if len(ids) != len(img.Layers) {
		return fmt.Errorf("the image configuration lists %d layers, the manifest %d", len(ids), len(img.Layers))
	}
	for i, l := range img.Layers {
		l.DiffID = ids[i]
	}
	return nil
}

// selector selects the image for a platform.
type selector struct {
	want Platform
	// explicit is set when the platform was given, not the default.
	explicit bool
}

// choose chooses the manifest of an index for the platform.
func (s selector) choose(manifests []Descriptor) (Descriptor, error) {
	best, bestScore := -1, 0
	var available []string
	for i, m := range manifests {
		if m.Platform == nil || m.Platform.OS == "unknown" { // e.g. attestations
			continue
		}
		available = append(available, m.Platform.String())
		if score := s.want.score(*m.Platform); score > bestScore {
			best, bestScore = i, score
		}
	}
	if best < 0 {
		if len(available) == 0 {
			return Descriptor{}, errors.New("the index lists no image for any platform")
		}
		err := fmt.Errorf("the image is not available for %s (only for %s)", s.want, strings.Join(available, ", "))
		if !s.explicit {
			err = fmt.Errorf("%w; choose one with --platform", err)
		}
		return Descriptor{}, err
	}
	return manifests[best], nil
}

// check checks the platform of a single-platform image: an error if the
// platform was given, a warning otherwise, like docker pull.
func (s selector) check(cfg *Config) error {
	have := cfg.platform()
	if have.OS == "" || have.Architecture == "" || s.want.score(have) > 0 {
		return nil
	}
	if s.explicit {
		return fmt.Errorf("the image is for %s, not %s", have, s.want)
	}
	log.Warnf("the image is for %s, not %s: its programs may not run here", have, s.want)
	return nil
}
