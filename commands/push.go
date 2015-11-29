package commands

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/codegangsta/cli"

	"github.com/yuuki1/droot/aws"
	"github.com/yuuki1/droot/docker"
	"github.com/yuuki1/droot/log"
	"github.com/yuuki1/droot/osutil"
)

var CommandArgPush = "--to S3_ENDPOINT DOCKER_REPOSITORY[:TAG]"
var CommandPush = cli.Command{
	Name:   "push",
	Usage:  "Push an extracted docker image into s3",
	Action: fatalOnError(doPush),
	Flags: []cli.Flag{
		cli.StringFlag{Name: "to, t", Usage: "Amazon S3 endpoint (ex. s3://example.com/containers/app.tar.gz)"},
	},
}

func doPush(c *cli.Context) error {
	if len(c.Args()) < 1 {
		cli.ShowCommandHelp(c, "push")
		return errors.New("docker repository required")
	}
	repository := c.Args().Get(0)
	to := c.String("to")
	if to == "" || repository == "" {
		cli.ShowCommandHelp(c, "push")
		return errors.New("docker repository required")
	}

	s3Url, err := url.Parse(to)
	if err != nil {
		return err
	}
	if s3Url.Scheme != "s3" {
		return fmt.Errorf("Not s3 scheme %s", to)
	}

	// In the Following, pipe like  `docker export ... | gzip -c | aws s3`
	// to avoid to use a temporary file.

	log.Info("export", repository)
	imageReader, err := docker.ExportImage(repository)
	if err != nil {
		return err
	}
	defer imageReader.Close()

	gzipReader := osutil.Compress(imageReader)
	defer gzipReader.Close()

	log.Info("s3 uploading to", to)
	location, err := aws.NewS3Client().Upload(s3Url, gzipReader)
	if err != nil {
		return err
	}
	log.Info("uploaded", location)

	return nil
}
