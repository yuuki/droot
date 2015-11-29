package docker

import (
	"io"

	"github.com/fsouza/go-dockerclient"
)

func ExportImage(imageID string, outputStream io.Writer) error {
	client, err := docker.NewClientFromEnv()
	if err != nil {
		return err
	}

	container, err := client.CreateContainer(docker.CreateContainerOptions{
		Config: &docker.Config{
			Image: imageID,
		},
	})
	if err != nil {
		return err
	}
	defer func(containerID string) error {
		return client.RemoveContainer(docker.RemoveContainerOptions{
			ID: containerID,
			Force: true,
		})
	}(container.ID)

	return client.ExportContainer(docker.ExportContainerOptions{
		ID: container.ID,
		OutputStream: outputStream,
	})
}
