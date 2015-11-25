package commands

import (
	"io/ioutil"
	"net/url"
	"os"

	"github.com/codegangsta/cli"

	"github.com/yuuki1/dochroot/aws"
	"github.com/yuuki1/dochroot/docker"
	"github.com/yuuki1/dochroot/log"
	"github.com/yuuki1/dochroot/osutil"
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

	tmp, err := ioutil.TempFile(os.TempDir(), "dochroot")
	if err != nil {
		log.Error(err)
		return
	}
	defer tmp.Close()
	defer os.Remove(tmp.Name())

	log.Info("Export docker image", "to", tmp.Name())
	if err := docker.ExportImage(repository, tmp); err != nil {
		log.Error(err)
		return
	}

	// reopen for reading
	tmp, err = os.Open(tmp.Name())
	if err != nil {
		log.Error(err)
		return
	}

	tmpGzip, err := ioutil.TempFile(os.TempDir(), "dochroot_gzip")
	if err != nil {
		log.Error(err)
		return
	}
	defer tmpGzip.Close()
	defer os.Remove(tmpGzip.Name())

	log.Info("gzip", "from", tmp.Name(), "to", tmpGzip.Name())
	if err := osutil.Gzip(tmpGzip, tmp); err != nil {
		log.Error(err)
		return
	}

	location, err := aws.NewS3Client().Upload(s3Url, tmpGzip)
	if err != nil {
		log.Error(err)
		return
	}
	log.Info("uploaded", location)
}
