package aws

import (
	"bytes"
	"net/http"
	"net/url"
	"io/ioutil"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/awstesting/unit"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/stretchr/testify/assert"
)

var buf12MB = make([]byte, 1024*1024*12)

func NewTestS3Client() *S3Client {
	var m sync.Mutex
	svc := s3.New(unit.Session)
	svc.Handlers.Unmarshal.Clear()
	svc.Handlers.UnmarshalMeta.Clear()
	svc.Handlers.UnmarshalError.Clear()
	svc.Handlers.Send.Clear()
	svc.Handlers.Send.PushBack(func(r *request.Request) {
		m.Lock()
		defer m.Unlock()

		r.HTTPResponse = &http.Response{
			StatusCode: 200,
			Body:       ioutil.NopCloser(bytes.NewReader(buf12MB)),
			Header:     http.Header{},
		}
	})

	return &S3Client{svc: svc}
}

func TestUpload(t *testing.T) {
	s3cli := NewTestS3Client()

	s3Url, _ := url.Parse("s3://droot-sandbox/images/app.tar.gz")
	location, err := s3cli.Upload(s3Url, bytes.NewReader(buf12MB))

	assert.NoError(t, err)
	assert.Equal(t, "https://droot-sandbox.s3.mock-region.amazonaws.com/images/app.tar.gz", location)
}

