package commands

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/url"
	"os"

	"github.com/codegangsta/cli"

	"github.com/yuuki1/droot/aws"
	"github.com/yuuki1/droot/docker"
	"github.com/yuuki1/droot/log"
	"github.com/yuuki1/droot/osutil"
)

var CommandArgPush = "--to S3_ENDPOINT DOCKER_REPOSITORY[:TAG]"
var CommandPush = cli.Command{
	Name:  "push",
	Usage: "Push an extracted docker image into s3",
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

	tmp, err := ioutil.TempFile(os.TempDir(), "droot")
	if err != nil {
		return err
	}
	defer tmp.Close()
	defer os.Remove(tmp.Name())

	log.Info("Export docker image", "to", tmp.Name())
	if err := docker.ExportImage(repository, tmp); err != nil {
		return err
	}

	// reopen for reading
	tmp, err = os.Open(tmp.Name())
	if err != nil {
		return err
	}

	tmpGzip, err := ioutil.TempFile(os.TempDir(), "droot_gzip")
	if err != nil {
		return err
	}
	defer tmpGzip.Close()
	defer os.Remove(tmpGzip.Name())

	log.Info("gzip", "from", tmp.Name(), "to", tmpGzip.Name())
	if err := osutil.Gzip(tmpGzip, tmp); err != nil {
		return err
	}

	log.Info("s3 uploading to", to)
	location, err := aws.NewS3Client().Upload(s3Url, tmpGzip)
	if err != nil {
		return err
	}
	log.Info("uploaded", location)

	return nil
}
