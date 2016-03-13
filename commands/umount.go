package commands

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/codegangsta/cli"

	"github.com/yuuki/droot/osutil"
)

var CommandArgUmount = "--root ROOT_DIR"
var CommandUmount = cli.Command{
	Name:   "umount",
	Usage:  "Umount directory mounted by 'run' command",
	Action: fatalOnError(doUmount),
	Flags: []cli.Flag{
		cli.StringFlag{Name: "root, r", Usage: "Root directory path for chrooted"},
	},
}

func doUmount(c *cli.Context) error {
	if c.String("root") == "" {
		cli.ShowCommandHelp(c, "umount")
		return errors.New("--root option required")
	}

	rootDir, err := filepath.Abs(c.String("root"))
	if err != nil {
		return err
	}

	if !osutil.ExistsDir(rootDir) {
		return fmt.Errorf("No such directory %s", rootDir)
	}

	return osutil.UmountRoot(rootDir)
}
