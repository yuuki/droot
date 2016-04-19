package commands

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/codegangsta/cli"

	"github.com/yuuki/droot/archive"
	"github.com/yuuki/droot/deploy"
	"github.com/yuuki/droot/log"
)

var CommandArgDeploy = "--root DESTINATION_DIRECTORY [--mode MODE]"
var CommandDeploy = cli.Command{
	Name:   "deploy",
	Usage:  "Deploy a droot archive into a directory",
	Action: fatalOnError(doDeploy),
	Flags: []cli.Flag{
		cli.StringFlag{Name: "root, r", Usage: "Destination directory"},
		cli.StringFlag{Name: "mode, m", Usage: "Mode of deployment. 'rsync' or 'symlink'. default is 'rsync'"},
		cli.BoolFlag{Name: "same-owner", Usage: "Try extracting files with the same ownership as exists in the archive (default for superuser)"},
	},
}

func doDeploy(c *cli.Context) error {
	if c.String("root") == "" {
		cli.ShowCommandHelp(c, "deploy")
		return errors.New("--root required")
	}

	rootDir, err := filepath.Abs(c.String("root"))
	if err != nil {
		return err
	}

	tmpDir, err := ioutil.TempDir(os.TempDir(), "droot")
	if err != nil {
		return fmt.Errorf("Failed to create temporary dir: %s", err)
	}
	defer os.RemoveAll(tmpDir)
	if err := os.Chmod(tmpDir, 0755); err != nil {
		return err
	}

	log.Info("-->", "Extracting", "from", "stdin", "to", tmpDir)
	if err := archive.Untar(os.Stdin, tmpDir, c.Bool("same-owner")); err != nil {
		return err
	}

	switch c.String("mode") {
	case "","rsync":
		log.Info("-->", "Syncing", "from", tmpDir, "to", rootDir)

		if err := deploy.Rsync(tmpDir, rootDir); err != nil {
			return fmt.Errorf("Failed to rsync: %s", err)
		}
	case "symlink":
		if err := deploy.DeployWithSymlink(tmpDir, rootDir); err != nil {
			return err
		}
	default:
		return fmt.Errorf("Invalid mode %s. '--mode' must be 'rsync' or 'symlink'.", c.String("mode"))
	}

	return nil
}
