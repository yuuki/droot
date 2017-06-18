package docker

import (
	"io"
	"io/ioutil"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/stretchr/testify/assert"
	"golang.org/x/net/context" // docker/docker don't use 'context' as standard package.
)

func TestExportImage(t *testing.T) {
	containerID := "container ID"

	fakeClient := &fakeDocker{
		FakeContainerCreate: func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, containerName string) (container.ContainerCreateCreatedBody, error) {
			return container.ContainerCreateCreatedBody{ID: containerID}, nil
		},
		FakeContainerStart: func(ctx context.Context, containerID string, options types.ContainerStartOptions) error {
			return nil
		},
		FakeContainerWait: func(ctx context.Context, containerID string) (int64, error) {
			return int64(0), nil
		},
		FakeContainerExport: func(ctx context.Context, containerID string) (io.ReadCloser, error) {
			return ioutil.NopCloser(strings.NewReader("image body")), nil
		},
		FakeContainerRemove: func(ctx context.Context, containerID string, options types.ContainerRemoveOptions) error {
			return nil
		},
	}

	client := &Client{docker: fakeClient}
	r, err := client.ExportImage("aaaaaaaaaaaa")
	defer r.Close()

	time.Sleep(10 * time.Millisecond) // wait for finishing goroutine

	assert.NoError(t, err)
}
