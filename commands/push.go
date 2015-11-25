package commands

import (
	"io/ioutil"
	"net/url"
	"os"

	"github.com/codegangsta/cli"

	"github.com/yuuki1/dochroot/aws"
	"github.com/yuuki1/dochroot/docker"
	"github.com/yuuki1/dochroot/log"
)

var CommandArgPush = "--to S3_ENDPOINT DOCKER_REPOSITORY[:TAG]"
var CommandPush = cli.Command{
	Name:  "push",
	Usage: "Push an extracted docker image into s3",
	Action: doPush,
	Flags: []cli.Flag{
		cli.StringFlag{Name: "to, t", Usage: "Amazon S3 endpoint (ex. s3://example.com/containers/app.tar.gz)"},
	},
}

func doPush(c *cli.Context) {
	if len(c.Args()) < 1 {
		cli.ShowCommandHelp(c, "push")
		os.Exit(1)
	}
	repository := c.Args().Get(0)
	to := c.String("to")
	if to == "" || repository == "" {
		cli.ShowCommandHelp(c, "push")
		os.Exit(1)
	}

	s3Url, err := url.Parse(to)
	if err != nil {
		log.Error(err)
		return
	}
	if s3Url.Scheme != "s3" {
		log.Errorf("%s: Not s3 scheme\n", to)
		return
	}

	imageWriter, err := ioutil.TempFile(os.TempDir(), "dochroot")
	if err != nil {
		log.Error(err)
		return
	}
	defer os.Remove(imageWriter.Name())

	log.Info("Export docker image:", "to", imageWriter.Name())
	if err := docker.ExportImage(repository, imageWriter); err != nil {
		log.Error(err)
		return
	}

	imageReader, err := os.Open(imageWriter.Name())
	if err != nil {
		log.Error(err)
		return
	}
	defer imageReader.Close()

	location, err := aws.NewS3Client().Upload(s3Url, imageReader)
	if err != nil {
		log.Error(err)
		return
	}
	log.Info("uploaded", location)
}
