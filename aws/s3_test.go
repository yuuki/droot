package aws

import (
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3iface"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
)

func NewTestS3Client(mockSvc s3iface.S3API) *S3Client {
	return &S3Client{svc: mockSvc}
}

func TestExistsBucket(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockS3 := NewMockS3API(ctrl)
	mockS3.EXPECT().ListObjects(&s3.ListObjectsInput{
		Bucket: aws.String("droot-containers"),
	}).Return(&s3.ListObjectsOutput{
	}, nil)

	svc := NewTestS3Client(mockS3)

	exists, err := svc.ExistsBucket("droot-containers")

	assert.NoError(t, err)
	assert.Equal(t, true, exists)
}

