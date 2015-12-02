package aws

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3iface"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"

	"github.com/yuuki1/droot/log"
)

const uploadPartSize = 64 * 1024 * 1024 // 64MB part size
const downloadPartSize = uploadPartSize

type S3Client struct {
	svc s3iface.S3API
}

func NewS3Client() *S3Client {
	var svc s3iface.S3API
	if log.IsDebug {
		svc = s3.New(session.New(), aws.NewConfig().WithLogLevel(aws.LogDebug))
	} else {
		svc = s3.New(session.New())
	}
	return &S3Client{svc: svc}
}

func (clt *S3Client) ExistsBucket(bucket string) (bool, error) {
	_, err := clt.svc.ListObjects(&s3.ListObjectsInput{
		Bucket: &bucket,
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

func (clt *S3Client) Upload(s3Url *url.URL, reader io.Reader) (string, error) {
	bucket, object := s3Url.Host, s3Url.Path

	ok, err := clt.ExistsBucket(bucket)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New(fmt.Sprintf("No such bucket: %s", bucket))
	}

	uploader := s3manager.NewUploaderWithClient(clt.svc)
	upOutput, err := uploader.Upload(&s3manager.UploadInput{
		Bucket: &bucket,
		Key:    &object,
		Body:   reader,
	}, func(u *s3manager.Uploader) {
		u.PartSize = uploadPartSize
	})
	if err != nil {
		return "", err
	}

	return upOutput.Location, nil
}

func (clt *S3Client) Download(s3Url *url.URL) (int64, *os.File, error) {
	bucket, object := s3Url.Host, s3Url.Path

	ok, err := clt.ExistsBucket(bucket)
	if err != nil {
		return -1, nil, err
	}
	if !ok {
		return -1, nil, fmt.Errorf("No such bucket: %s", bucket)
	}

	pReader, pWriter, err := os.Pipe()
	if err != nil {
		return -1, nil, err
	}
	defer pWriter.Close()

	downloader := s3manager.NewDownloaderWithClient(clt.svc)
	nBytes, err := downloader.Download(pWriter, &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &object,
	}, func(d *s3manager.Downloader) {
		d.PartSize = downloadPartSize
	})
	if err != nil {
		return -1, nil, err
	}

	return nBytes, pReader, nil
}
