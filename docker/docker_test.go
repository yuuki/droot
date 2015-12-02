package docker

import (
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	godocker "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
)

func TestExportImage(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mockDocker := NewMockdockerclient(mockCtrl)

	containerID := "container ID"

	mockDocker.EXPECT().CreateContainer(gomock.Any()).Return(&godocker.Container{
		ID: containerID,
	}, nil)
	mockDocker.EXPECT().ExportContainer(gomock.Any()).Do(func(opts godocker.ExportContainerOptions) {
		assert.Equal(t, opts.ID, containerID)
	}).Return(nil)
	mockDocker.EXPECT().RemoveContainer(gomock.Any()).Do(func(opts godocker.RemoveContainerOptions) {
		assert.Equal(t, opts.ID, containerID)
		assert.Equal(t, opts.Force, true)
	}).Return(nil)

	client := &Client{docker: mockDocker}
	r, err := client.ExportImage("aaaaaaaaaaaa")
	defer r.Close()

	time.Sleep(10 * time.Millisecond) // wait for finishing goroutine

	assert.NoError(t, err)
}

