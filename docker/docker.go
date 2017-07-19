package docker

import (
	"fmt"
	"io"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/pkg/errors"
	"github.com/yuuki/droot/environ"
	"golang.org/x/net/context" // docker/docker don't use 'context' as standard package.
)

// dockerAPI is an interface for stub testing.
type dockerAPI interface {
	ImageInspectWithRaw(ctx context.Context, imageID string) (types.ImageInspect, []byte, error)
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, containerName string) (container.ContainerCreateCreatedBody, error)
	ContainerStart(ctx context.Context, containerID string, options types.ContainerStartOptions) error
	ContainerWait(ctx context.Context, containerID string) (int64, error)
	ContainerExport(ctx context.Context, containerID string) (io.ReadCloser, error)
	ContainerRemove(ctx context.Context, containerID string, options types.ContainerRemoveOptions) error
}

// Client represents a Docker API client.
type Client struct {
	docker dockerAPI
}

// New creates the Client instance.
func New() (*Client, error) {
	cli, err := client.NewEnvClient()
	if err != nil {
		return nil, err
	}
	if _, err := cli.Ping(context.Background()); err != nil {
		return nil, err
	}
	return &Client{docker: cli}, nil
}

// ExportImage exports a docker image into the archive of filesystem.
// Save an environ of the docker image into `/.drootenv` to preserve it.
func (c *Client) ExportImage(imageID string) (io.ReadCloser, error) {
	ctx := context.Background()

	image, _, err := c.docker.ImageInspectWithRaw(ctx, imageID)
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to inspect image imageID:%s", imageID)
	}

	//Put drootenv file into the filesystem.
	cmd := fmt.Sprintf("echo \"%s\" > %s", strings.Join(image.ContainerConfig.Env, "\n"), environ.DROOT_ENV_FILE_PATH)
	container, err := c.docker.ContainerCreate(ctx, &container.Config{
		Image:      imageID,
		User:       "root",       // Avoid permission denied error
		Entrypoint: []string{""}, // Clear the exising entrypoint
		Cmd:        []string{"/bin/sh", "-c", cmd},
	}, nil, nil, "")
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to create container imageID:%s", imageID)
	}

	// start container because creating container does not run above `printenv > /.drootenv`.
	if err := c.docker.ContainerStart(ctx, container.ID, types.ContainerStartOptions{}); err != nil {
		_ = c.docker.ContainerRemove(ctx, container.ID, types.ContainerRemoveOptions{
			Force: true,
		})
		return nil, errors.Wrapf(err, "Failed to remove container containerID:%s", container.ID)
	}

	code, err := c.docker.ContainerWait(ctx, container.ID)
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to wait container containerID:%s", container.ID)
	}
	if code != int64(0) {
		return nil, errors.Errorf("ContainerWait status code is not 0, but %d containerID:%s", code, container.ID)
	}

	pReader, pWriter := io.Pipe()

	go func() {
		defer func() {
			_ = c.docker.ContainerRemove(ctx, container.ID, types.ContainerRemoveOptions{
				Force: true,
			})
		}()

		body, err := c.docker.ContainerExport(ctx, container.ID)
		if err != nil {
			err = errors.Wrapf(err, "Failed to export container containerID:%s", container.ID)
			pWriter.CloseWithError(err)
		} else {
			_, err := io.Copy(pWriter, body)
			if err != nil {
				err = errors.Wrapf(err, "Failed to copy from reader of exporting container containerID:%s", container.ID)
				pWriter.CloseWithError(err)
			}
			pWriter.Close()
		}
	}()

	return pReader, nil
}
