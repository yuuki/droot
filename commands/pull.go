package commands

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/url"
	"os"

	"github.com/codegangsta/cli"

	"github.com/yuuki1/droot/archive"
	"github.com/yuuki1/droot/aws"
	"github.com/yuuki1/droot/log"
	"github.com/yuuki1/droot/osutil"
)

var CommandArgPull = "--dest DESTINATION_DIRECTORY --src S3_ENDPOINT [--user USER] [--grpup GROUP]"
var CommandPull = cli.Command{
	Name:   "pull",
	Usage:  "Pull an extracted docker image from s3",
	Action: fatalOnError(doPull),
	Flags: []cli.Flag{
		cli.StringFlag{Name: "dest, d", Usage: "Local filesystem path (ex. /var/containers/app)"},
		cli.StringFlag{Name: "src, s", Usage: "Amazon S3 endpoint (ex. s3://example.com/containers/app.tar.gz)"},
		cli.StringFlag{Name: "user, u", Usage: "User (ID or name) to set after extracting archive"},
		cli.StringFlag{Name: "group, g", Usage: "Group (ID or name) to set after extracting archive"},
	},
}

func doPull(c *cli.Context) error {
	destDir := c.String("dest")
	srcURL := c.String("src")
	if destDir == "" || srcURL == "" {
		cli.ShowCommandHelp(c, "pull")
		return errors.New("--src and --dest option required ")
	}

	s3URL, err := url.Parse(srcURL)
	if err != nil {
		return err
	}
	if s3URL.Scheme != "s3" {
		return fmt.Errorf("Not s3 scheme %s", srcURL)
	}

	if !osutil.ExistsDir(destDir) {
		return fmt.Errorf("No such directory %s", destDir)
	}

	uid, gid := -1, -1
	if user := c.String("user"); user != "" {
		uid, err = osutil.LookupUser(user)
		if err != nil {
			return err
		}
	}
	if group := c.String("group"); group != "" {
		gid, err = osutil.LookupGroup(group)
		if err != nil {
			return err
		}
	}

	downloadSize, imageReader, err := aws.NewS3Client().Download(s3URL)
	if err != nil {
		return err
	}
	defer imageReader.Close()
	log.Info("downloaded", "from", s3URL, downloadSize, "bytes")

	dir, err := ioutil.TempDir(os.TempDir(), "droot")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	if err := archive.ExtractTarGz(imageReader, dir, uid, gid); err != nil {
		return err
	}

	log.Info("rsync:", "from", dir, "to", destDir)
	if err := archive.Rsync(dir, destDir); err != nil {
		return err
	}

	return nil
}
