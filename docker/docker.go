package docker

import (
	"io"

	"github.com/fsouza/go-dockerclient"
)

const exportBufSize = 32768

func ExportImage(imageID string) (io.ReadCloser, error) {
	client, err := docker.NewClientFromEnv()
	if err != nil {
		return nil, err
	}

	container, err := client.CreateContainer(docker.CreateContainerOptions{
		Config: &docker.Config{
			Image: imageID,
		},
	})
	if err != nil {
		return nil, err
	}
	defer func(containerID string) error {
		return client.RemoveContainer(docker.RemoveContainerOptions{
			ID:    containerID,
			Force: true,
		})
	}(container.ID)

	pReader, pWriter := io.Pipe()

	go func() {
		err := client.ExportContainer(docker.ExportContainerOptions{
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
