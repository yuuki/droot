package docker

import (
	godocker "github.com/fsouza/go-dockerclient"
)

type dockerclient interface {
	RemoveContainer(opts godocker.RemoveContainerOptions) error
	CreateContainer(opts godocker.CreateContainerOptions) (*godocker.Container, error)
	StartContainer(id string, hostConfig *godocker.HostConfig) error
	ExportContainer(opts godocker.ExportContainerOptions) error
}

type _dockerclient struct {
	docker *godocker.Client
}

func newDockerClient() (*_dockerclient, error) {
	client, err := godocker.NewClientFromEnv()
	if err != nil {
		return nil, err
	}
	err = client.Ping()
	return &_dockerclient{
		docker: client,
	}, err
}

func (d *_dockerclient) RemoveContainer(opts godocker.RemoveContainerOptions) error {
	return d.docker.RemoveContainer(opts)
}

func (d *_dockerclient) CreateContainer(opts godocker.CreateContainerOptions) (*godocker.Container, error) {
	return d.docker.CreateContainer(opts)
}

func (d *_dockerclient) StartContainer(id string, hostConfig *godocker.HostConfig) error {
	return d.docker.StartContainer(id, hostConfig)
}

func (d *_dockerclient) ExportContainer(opts godocker.ExportContainerOptions) error {
	return d.docker.ExportContainer(opts)
}
