package aws

import "github.com/stretchr/testify/mock"

import "github.com/aws/aws-sdk-go/service/s3/s3manager"

type mockS3uploader struct {
	mock.Mock
}

func (_m *mockS3uploader) Upload(_a0 *s3manager.UploadInput, _a1 func(*s3manager.Uploader)) (*s3manager.UploadOutput, error) {
	ret := _m.Called(_a0, _a1)

	var r0 *s3manager.UploadOutput
	if rf, ok := ret.Get(0).(func(*s3manager.UploadInput, func(*s3manager.Uploader)) *s3manager.UploadOutput); ok {
		r0 = rf(_a0, _a1)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(*s3manager.UploadOutput)
		}
	}

	var r1 error
	if rf, ok := ret.Get(1).(func(*s3manager.UploadInput, func(*s3manager.Uploader)) error); ok {
		r1 = rf(_a0, _a1)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}
