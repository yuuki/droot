package docker

import (
	"testing"
	"time"

	godocker "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestExportImage(t *testing.T) {
	mockDocker := new(mockDockerclient)

	containerID := "container ID"

	mockDocker.On("CreateContainer", mock.Anything).Return(&godocker.Container{
		ID: containerID,
	}, nil)
	mockDocker.On("StartContainer", containerID, mock.Anything).Return(nil)
	mockDocker.On("WaitContainer", containerID).Return(0, nil)
	mockDocker.On("ExportContainer", mock.Anything).Return(nil).Run(func(args mock.Arguments) {
		id := args.Get(0).(godocker.ExportContainerOptions).ID
		assert.Equal(t, id, containerID)
	})
	mockDocker.On("RemoveContainer", mock.Anything).Return(nil).Run(func(args mock.Arguments) {
		id := args.Get(0).(godocker.RemoveContainerOptions).ID
		force := args.Get(0).(godocker.RemoveContainerOptions).Force
		assert.Equal(t, id, containerID)
		assert.Equal(t, force, true)
	})

	client := &Client{docker: mockDocker}
	r, err := client.ExportImage("aaaaaaaaaaaa")
	defer r.Close()

	time.Sleep(10 * time.Millisecond) // wait for finishing goroutine

	assert.NoError(t, err)
}
