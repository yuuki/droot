package aws

import "github.com/stretchr/testify/mock"

import "io"
import "github.com/aws/aws-sdk-go/service/s3"

import "github.com/aws/aws-sdk-go/service/s3/s3manager"

type mockS3downloader struct {
	mock.Mock
}

func (_m *mockS3downloader) Download(_a0 io.WriterAt, _a1 *s3.GetObjectInput, _a2 ...func(*s3manager.Downloader)) (int64, error) {
	ret := _m.Called(_a0, _a1, _a2)

	var r0 int64
	if rf, ok := ret.Get(0).(func(io.WriterAt, *s3.GetObjectInput, ...func(*s3manager.Downloader)) int64); ok {
		r0 = rf(_a0, _a1, _a2...)
	} else {
		r0 = ret.Get(0).(int64)
	}

	var r1 error
	if rf, ok := ret.Get(1).(func(io.WriterAt, *s3.GetObjectInput, ...func(*s3manager.Downloader)) error); ok {
		r1 = rf(_a0, _a1, _a2...)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}
