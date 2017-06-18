package docker

import (
	"io"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"golang.org/x/net/context" // docker/docker don't use 'context' as standard package.
)

type fakeDocker struct {
	dockerAPI
	FakeContainerCreate func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, containerName string) (container.ContainerCreateCreatedBody, error)
	FakeContainerStart  func(ctx context.Context, containerID string, options types.ContainerStartOptions) error
	FakeContainerWait   func(ctx context.Context, containerID string) (int64, error)
	FakeContainerExport func(ctx context.Context, containerID string) (io.ReadCloser, error)
	FakeContainerRemove func(ctx context.Context, containerID string, options types.ContainerRemoveOptions) error
}

func (d *fakeDocker) ContainerRemove(ctx context.Context, containerID string, options types.ContainerRemoveOptions) error {
	return d.FakeContainerRemove(ctx, containerID, options)
}

func (d *fakeDocker) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, containerName string) (container.ContainerCreateCreatedBody, error) {
	return d.FakeContainerCreate(ctx, config, hostConfig, networkingConfig, containerName)
}

func (d *fakeDocker) ContainerStart(ctx context.Context, containerID string, options types.ContainerStartOptions) error {
	return d.FakeContainerStart(ctx, containerID, options)
}

func (d *fakeDocker) ContainerWait(ctx context.Context, containerID string) (int64, error) {
	return d.FakeContainerWait(ctx, containerID)
}

func (d *fakeDocker) ContainerExport(ctx context.Context, containerID string) (io.ReadCloser, error) {
	return d.FakeContainerExport(ctx, containerID)
}
