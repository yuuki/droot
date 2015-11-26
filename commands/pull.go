package commands

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"io/ioutil"

	"github.com/codegangsta/cli"

	"github.com/yuuki1/dochroot/aws"
	"github.com/yuuki1/dochroot/log"
	"github.com/yuuki1/dochroot/osutil"
)

var CommandArgPull = "--dest DESTINATION_DIRECTORY --src S3_ENDPOINT"
var CommandPull = cli.Command{
	Name:  "pull",
	Usage: "Pull an extracted docker image from s3",
	Action: fatalOnError(doPull),
	Flags: []cli.Flag{
		cli.StringFlag{Name: "dest, d", Usage: "Local filesystem path (ex. /var/containers/app)"},
		cli.StringFlag{Name: "src, s", Usage: "Amazon S3 endpoint (ex. s3://example.com/containers/app.tar.gz)"},
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

	if ! osutil.ExistsDir(destDir) {
		return fmt.Errorf("No such directory", destDir)
	}

	tmp, err := ioutil.TempFile(os.TempDir(), "dochroot_gzip")
	if err != nil {
		return err
	}
	defer tmp.Close()
	defer os.Remove(tmp.Name())

	nBytes, err := aws.NewS3Client().Download(s3URL, tmp)
	if err != nil {
		return err
	}
	log.Info("downloaded", "from", s3URL, "to", tmp.Name(), nBytes, "bytes")

	rawDir, err := ioutil.TempDir(os.TempDir(), "dochroot_raw")
	if err != nil {
		return err
	}
	defer os.RemoveAll(rawDir)

	cwd, _ := os.Getwd()
	if err := os.Chdir(rawDir); err != nil {
		return err
	}

	log.Info("Extract archive:", tmp.Name(), "to", rawDir)
	if err := osutil.ExtractTarGz(tmp.Name()); err != nil {
		return err
	}

	log.Info("Sync:", "from", rawDir, "to", destDir)
	if err := osutil.Rsync(rawDir, destDir); err != nil {
		return err
	}

	if err = os.Chdir(cwd); err != nil {
		return err
	}

	return nil
}
