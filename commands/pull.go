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
		cli.StringFlag{Name: "src, s", Usage: "Amazon S3 endpoint (ex. s3://drootexample/app.tar.gz)"},
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

	uid, gid := -1, -1
	if user := c.String("user"); user != "" {
		uid, err = osutil.LookupUser(user)
		if err != nil {
			return fmt.Errorf("Failed to lookup user (%s): %s", user, err)
		}
	}
	if group := c.String("group"); group != "" {
		gid, err = osutil.LookupGroup(group)
		if err != nil {
			return fmt.Errorf("Failed to lookup group (%s): %s", group, err)
		}
	}

	tmp, err := ioutil.TempFile(os.TempDir(), "droot_gzip")
	if err != nil {
		return fmt.Errorf("Failed to create temporary file: %s", err)
	}
	defer func(f *os.File){
		f.Close()
		os.Remove(f.Name())
	}(tmp)

	nBytes, err := aws.NewS3Client().Download(s3URL, tmp)
	if err != nil {
		return fmt.Errorf("Failed to download file(%s) from s3: %s", srcURL, err)
	}
	log.Info("downloaded", "from", s3URL, "to", tmp.Name(), nBytes, "bytes")

	rawDir, err := ioutil.TempDir(os.TempDir(), "droot_raw")
	if err != nil {
		return fmt.Errorf("Failed to create temporary dir: %s", err)
	}
	defer os.RemoveAll(rawDir)

	log.Info("Extract archive:", tmp.Name(), "to", rawDir)
	if err := archive.ExtractTarGz(tmp, rawDir, uid, gid); err != nil {
		return fmt.Errorf("Failed to extract archive: %s", err)
	}

	log.Info("Sync:", "from", rawDir, "to", destDir)
	if err := archive.Rsync(rawDir, destDir); err != nil {
		return fmt.Errorf("Failed to rsync: %s", err)
	}

	return nil
}
