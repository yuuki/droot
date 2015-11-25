package commands

import (
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
	Action: doPull,
	Flags: []cli.Flag{
		cli.StringFlag{Name: "dest, d", Usage: "Local filesystem path (ex. /var/containers/app)"},
		cli.StringFlag{Name: "src, s", Usage: "Amazon S3 endpoint (ex. s3://example.com/containers/app.tar.gz)"},
	},
}

func doPull(c *cli.Context) {
	destDir := c.String("dest")
	srcURL := c.String("src")
	if destDir == "" || srcURL == "" {
		cli.ShowCommandHelp(c, "pull")
		os.Exit(1)
	}

	s3URL, err := url.Parse(srcURL)
	if err != nil {
		log.Error(err)
		return
	}
	if s3URL.Scheme != "s3" {
		log.Errorf("%s: Not s3 scheme\n", srcURL)
		return
	}

	if ! osutil.ExistsDir(destDir) {
		log.Errorf("%s: No such directory", destDir)
		return
	}

	tmp, err := ioutil.TempFile(os.TempDir(), "dochroot_gzip")
	if err != nil {
		log.Error(err)
		return
	}
	defer tmp.Close()
	defer os.Remove(tmp.Name())

	nBytes, err := aws.NewS3Client().Download(s3URL, tmp)
	if err != nil {
		log.Error(err)
		return
	}
	log.Info("downloaded", "from", s3URL, "to", tmp.Name(), nBytes, "bytes")

	rawDir, err := ioutil.TempDir(os.TempDir(), "dochroot_raw")
	if err != nil {
		log.Error(err)
		return
	}
	defer os.RemoveAll(rawDir)

	cwd, _ := os.Getwd()
	if err := os.Chdir(rawDir); err != nil {
		log.Error(err)
		return
	}

	log.Info("Extract archive:", tmp.Name(), "to", rawDir)
	if err := osutil.ExtractTarGz(tmp.Name()); err != nil {
		log.Error(err)
		return
	}

	log.Info("Sync:", "from", rawDir, "to", destDir)
	if err := osutil.Rsync(rawDir, destDir); err != nil {
		log.Error(err)
		return
	}

	if err = os.Chdir(cwd); err != nil {
		log.Error(err)
		return
	}

}
