package aws

import (
	"bytes"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestExistsBucket(t *testing.T) {
	mockS3 := new(S3API)

	mockS3.On("ListObjects", &s3.ListObjectsInput{
		Bucket: aws.String("droot-containers"),
	}).Return(&s3.ListObjectsOutput{}, nil)

	c := &S3Client{svc: mockS3}

	exists, err := c.ExistsBucket("droot-containers")

	assert.NoError(t, err)
	assert.Equal(t, true, exists)
}

func TestUpload(t *testing.T) {
	mockS3 := new(S3API)
	mockUploader := new(mockS3uploader)

	mockS3.On("ListObjects", &s3.ListObjectsInput{
		Bucket: aws.String("droot-containers"),
	}).Return(&s3.ListObjectsOutput{}, nil)

	in := bytes.NewReader([]byte{})

	mockUploader.On("Upload", &s3manager.UploadInput{
		Bucket: aws.String("droot-containers"),
		Key: aws.String("app.tar.gz"),
		Body: in,
	}, mock.AnythingOfType("func(*s3manager.Uploader)"),
	).Return(&s3manager.UploadOutput{
		Location: "https://droot-containers.s3-ap-northeast-1.amazonaws.com/app.tar.gz",
	}, nil)

	c := &S3Client{svc: mockS3, uploader: mockUploader}

	location, err := c.Upload("droot-containers", "app.tar.gz", in)

	assert.NoError(t, err)
	assert.Equal(t, "https://droot-containers.s3-ap-northeast-1.amazonaws.com/app.tar.gz", location)

}
