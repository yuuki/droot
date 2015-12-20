package aws

import (
	"io"

	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3iface"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
)

type s3uploader interface {
	Upload(*s3manager.UploadInput, func(*s3manager.Uploader)) (*s3manager.UploadOutput, error)
}

type _s3uploader struct {
	uploader *s3manager.Uploader
}

func newS3Uploader(svc s3iface.S3API) *_s3uploader {
	return &_s3uploader{uploader: s3manager.NewUploaderWithClient(svc)}
}

func (c *_s3uploader) Upload(input *s3manager.UploadInput, option func(*s3manager.Uploader)) (*s3manager.UploadOutput, error) {
	return c.uploader.Upload(input, option)
}


type s3downloader interface {
	Download(io.WriterAt, *s3.GetObjectInput, ...func(*s3manager.Downloader)) (int64, error)
}

type _s3downloader struct {
	downloader *s3manager.Downloader
}

func newS3Downloader(svc s3iface.S3API) *_s3downloader {
	return &_s3downloader{downloader: s3manager.NewDownloaderWithClient(svc)}
}

func (c *_s3downloader) Download(w io.WriterAt, input *s3.GetObjectInput, options ...func(*s3manager.Downloader)) (int64, error) {
	return c.downloader.Download(w, input, options...)
}
