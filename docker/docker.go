package docker

import (
	"io"

	godocker "github.com/fsouza/go-dockerclient"

	"github.com/yuuki/droot/environ"
	"github.com/yuuki/droot/errwrap"
)

type Client struct {
	docker dockerclient
}

func NewClient() (*Client, error) {
	client, err := newDockerClient()
	if err != nil {
		return nil, err
	}
	return &Client{
		docker: client,
	}, nil
}

// Export a docker image into the archive of filesystem.
// Save an environ of the docker image into `/.drootenv` to preserve it.
func (c *Client) ExportImage(imageID string) (io.ReadCloser, error) {
	container, err := c.docker.CreateContainer(godocker.CreateContainerOptions{
		Config: &godocker.Config{
			Image:      imageID,
			Entrypoint: []string{"/bin/sh"}, // Clear the exising entrypoint
			Cmd:        []string{"-c", "printenv", ">", environ.DROOT_ENV_FILE_PATH},
		},
	})
	if err != nil {
		return nil, errwrap.Wrapff(err, "Failed to create container (imageID:%s): {{err}}", imageID)
	}

	// start container because creating container does not run above `printenv > /.drootenv`.
	if err := c.docker.StartContainer(container.ID, &godocker.HostConfig{}); err != nil {
		c.docker.RemoveContainer(godocker.RemoveContainerOptions{
			ID:    container.ID,
			Force: true,
		})
		return nil, errwrap.Wrapff(err, "Failed to remove container (containerID:%s): {{err}}", container.ID)
	}

	if _, err := c.docker.WaitContainer(container.ID); err != nil {
		return nil, errwrap.Wrapff(err, "Failed to wait container (containerID:%s): {{err}}", container.ID)
	}

	pReader, pWriter := io.Pipe()

	go func() {
		defer func() {
			c.docker.RemoveContainer(godocker.RemoveContainerOptions{
				ID:    container.ID,
				Force: true,
			})
		}()

		err := c.docker.ExportContainer(godocker.ExportContainerOptions{
			ID:           container.ID,
			OutputStream: pWriter,
		})
		if err != nil {
			err = errwrap.Wrapff(err, "Failed to export container %s: {{err}}", container.ID)
			pWriter.CloseWithError(err)
		} else {
			pWriter.Close()
		}
	}()

	return pReader, nil
}
