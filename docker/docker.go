package docker

import (
	"io"

	godocker "github.com/fsouza/go-dockerclient"

	"github.com/yuuki1/droot/errwrap"
)

const exportBufSize = 32768

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

func (c *Client) ExportImage(imageID string) (io.ReadCloser, error) {
	container, err := c.docker.CreateContainer(godocker.CreateContainerOptions{
		Config: &godocker.Config{
			Image: imageID,
		},
	})
	if err != nil {
		return nil, errwrap.Wrapff(err, "Failed to create container (imageID:%s): {{err}}", imageID)
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
