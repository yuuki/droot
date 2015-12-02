package docker

import (
	"io"

	"github.com/fsouza/go-dockerclient"
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
	container, err := c.docker.CreateContainer(docker.CreateContainerOptions{
		Config: &docker.Config{
			Image: imageID,
		},
	})
	if err != nil {
		return nil, err
	}
	defer func(containerID string) error {
		return c.docker.RemoveContainer(docker.RemoveContainerOptions{
			ID:    containerID,
			Force: true,
		})
	}(container.ID)

	pReader, pWriter := io.Pipe()

	go func() {
		err := c.docker.ExportContainer(docker.ExportContainerOptions{
			ID:           container.ID,
			OutputStream: pWriter,
		})
		if err != nil {
			pWriter.CloseWithError(err)
		} else {
			pWriter.Close()
		}
	}()

	return pReader, nil
}
