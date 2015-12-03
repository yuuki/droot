package docker

import (
	"fmt"
	"io"

	"github.com/fsouza/go-dockerclient"
	"github.com/hashicorp/errwrap"
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
		return nil, errwrap.Wrapf(fmt.Sprintf("Failed to create container (imageID:%s): {{err}}", imageID), err)
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
			err = errwrap.Wrapf(fmt.Sprintf("Failed to export container %s: {{err}}", container.ID), err)
			pWriter.CloseWithError(err)
		} else {
			pWriter.Close()
		}
	}()

	return pReader, nil
}
