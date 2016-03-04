package docker

import "github.com/stretchr/testify/mock"

import godocker "github.com/fsouza/go-dockerclient"

type mockDockerclient struct {
	mock.Mock
}

func (_m *mockDockerclient) RemoveContainer(opts godocker.RemoveContainerOptions) error {
	ret := _m.Called(opts)

	var r0 error
	if rf, ok := ret.Get(0).(func(godocker.RemoveContainerOptions) error); ok {
		r0 = rf(opts)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}
func (_m *mockDockerclient) CreateContainer(opts godocker.CreateContainerOptions) (*godocker.Container, error) {
	ret := _m.Called(opts)

	var r0 *godocker.Container
	if rf, ok := ret.Get(0).(func(godocker.CreateContainerOptions) *godocker.Container); ok {
		r0 = rf(opts)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*godocker.Container)
		}
	}

	var r1 error
	if rf, ok := ret.Get(1).(func(godocker.CreateContainerOptions) error); ok {
		r1 = rf(opts)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}
func (_m *mockDockerclient) StartContainer(id string, hostConfig *godocker.HostConfig) error {
	ret := _m.Called(id, hostConfig)

	var r0 error
	if rf, ok := ret.Get(0).(func(string, *godocker.HostConfig) error); ok {
		r0 = rf(id, hostConfig)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}
func (_m *mockDockerclient) ExportContainer(opts godocker.ExportContainerOptions) error {
	ret := _m.Called(opts)

	var r0 error
	if rf, ok := ret.Get(0).(func(godocker.ExportContainerOptions) error); ok {
		r0 = rf(opts)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}
