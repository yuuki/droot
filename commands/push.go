package commands

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/codegangsta/cli"

	"github.com/yuuki/droot/archive"
	"github.com/yuuki/droot/aws"
	"github.com/yuuki/droot/docker"
	"github.com/yuuki/droot/log"
)

var CommandArgPush = "--to S3_ENDPOINT DOCKER_REPOSITORY[:TAG]"
var CommandPush = cli.Command{
	Name:   "push",
	Usage:  "Push an extracted docker image into s3",
	Action: fatalOnError(doPush),
	Flags: []cli.Flag{
		cli.StringFlag{Name: "to, t", Usage: "Amazon S3 endpoint (ex. s3://drootexample/app.tar.gz)"},
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
		return fmt.Errorf("Failed to parse %s: %s", to, err)
	}
	if s3Url.Scheme != "s3" {
		return fmt.Errorf("Not s3 scheme %s", to)
	}

	// In the Following, pipe like  `docker export ... | gzip -c | aws s3`
	// to avoid to use a temporary file.

	log.Info("-->", "Exporting docker image", to)

	docker, err := docker.NewClient()
	if err != nil {
		return fmt.Errorf("Failed to create docker client: %s", err)
	}
	imageReader, err := docker.ExportImage(repository)
	if err != nil {
		return fmt.Errorf("Failed to export image %s: %s", repository, err)
	}
	defer imageReader.Close()

	gzipReader := archive.Compress(imageReader)
	defer gzipReader.Close()

	log.Info("-->", "Uploading archive to", to)

	location, err := aws.NewS3Client().Upload(s3Url.Host, s3Url.Path, gzipReader)
	if err != nil {
		return fmt.Errorf("Failed to upload file: %s", err)
	}

	log.Info("-->", "Uploaded to", location)

	return nil
}
