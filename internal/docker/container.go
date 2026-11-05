package docker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/yuuki/droot/internal/log"
)

// ContainerConfig is the subset of the container configuration that droot sets
// when creating a container.
type ContainerConfig struct {
	Image      string            `json:"Image"`
	Entrypoint []string          `json:"Entrypoint,omitempty"`
	Cmd        []string          `json:"Cmd,omitempty"`
	Labels     map[string]string `json:"Labels,omitempty"`
}

// ContainerInfo is the subset of the container inspection result that droot
// uses.
type ContainerInfo struct {
	ID     string `json:"Id"`
	Image  string `json:"Image"` // ID of the image the container was created from
	Config struct {
		Env        []string `json:"Env"`
		WorkingDir string   `json:"WorkingDir"`
		User       string   `json:"User"`
	} `json:"Config"`
}

// ContainerCreate creates (but does not start) a container and returns its ID.
// platform (e.g. "linux/amd64") is optional.
func (c *Client) ContainerCreate(ctx context.Context, cfg *ContainerConfig, platform string) (string, error) {
	query := url.Values{}
	if platform != "" {
		version, err := c.APIVersion(ctx)
		if err != nil {
			return "", err
		}
		if versionLess(version, platformAPIVersion) {
			return "", fmt.Errorf("--platform requires Docker Engine API %s or later (using %s)", platformAPIVersion, version)
		}
		query.Set("platform", platform)
	}
	resp, err := c.do(ctx, http.MethodPost, "/containers/create", query, cfg)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		ID       string   `json:"Id"`
		Warnings []string `json:"Warnings"`
	}
	if err := decodeJSON(resp.Body, &out); err != nil {
		return "", err
	}
	for _, w := range out.Warnings {
		log.Debugf("docker: %s", w)
	}
	if out.ID == "" {
		return "", fmt.Errorf("docker daemon returned an empty container ID")
	}
	return out.ID, nil
}

// ContainerInspect returns low-level information about a container.
func (c *Client) ContainerInspect(ctx context.Context, id string) (*ContainerInfo, error) {
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var info ContainerInfo
	if err := decodeJSON(resp.Body, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// ImageInfo is the subset of the image inspection result that droot uses.
type ImageInfo struct {
	ID     string       `json:"Id"`
	Config *ImageConfig `json:"Config"`
	// Created is the creation time of the image (RFC 3339).
	Created string `json:"Created"`
}

// ImageConfig is the subset of an image's configuration that droot saves.
type ImageConfig struct {
	Env        []string `json:"Env"`
	WorkingDir string   `json:"WorkingDir"`
	User       string   `json:"User"`
	Entrypoint []string `json:"Entrypoint"`
	Cmd        []string `json:"Cmd"`
	StopSignal string   `json:"StopSignal"`
}

// imagePlatformAPIVersion is the first API version accepting the platform
// parameter of image inspection.
const imagePlatformAPIVersion = "1.49"

// ImageInspect returns low-level information about an image. platform
// (e.g. "linux/arm64") selects a variant when the daemon supports it.
func (c *Client) ImageInspect(ctx context.Context, ref, platform string) (*ImageInfo, error) {
	query := url.Values{}
	if platform != "" {
		version, err := c.APIVersion(ctx)
		if err != nil {
			return nil, err
		}
		if !versionLess(version, imagePlatformAPIVersion) {
			query.Set("platform", platform)
		}
	}
	resp, err := c.do(ctx, http.MethodGet, "/images/"+url.PathEscape(ref)+"/json", query, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var info ImageInfo
	if err := decodeJSON(resp.Body, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// ContainerExport returns the container's filesystem as a tar stream. The
// caller must close it.
func (c *Client) ContainerExport(ctx context.Context, id string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/export", nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, &APIError{StatusCode: resp.StatusCode, Message: "unexpected status for an export"}
	}
	return resp.Body, nil
}

// ContainerRemove forcibly removes a container and its anonymous volumes.
func (c *Client) ContainerRemove(ctx context.Context, id string) error {
	query := url.Values{"force": {"1"}, "v": {"1"}}
	resp, err := c.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), query, nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
