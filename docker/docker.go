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

	pReader, pWriter := io.Pipe()

	go func() {
		defer func() {
			c.docker.RemoveContainer(docker.RemoveContainerOptions{
				ID:    container.ID,
				Force: true,
			})
		}()

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
