package commands

import (
	"errors"
	"fmt"

	"github.com/codegangsta/cli"

	"github.com/yuuki1/droot/osutil"
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
	rootDir := c.String("root")
	if rootDir == "" {
		cli.ShowCommandHelp(c, "umount")
		return errors.New("--root option required")
	}

	if !osutil.ExistsDir(rootDir) {
		return fmt.Errorf("No such directory %s", rootDir)
	}

	return osutil.UmountRoot(rootDir)
}
