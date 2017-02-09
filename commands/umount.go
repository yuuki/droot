package commands

import (
	"errors"

	"github.com/urfave/cli"

	"github.com/yuuki/droot/mounter"
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
	optRootDir := c.String("root")
	if optRootDir == "" {
		cli.ShowCommandHelp(c, "umount")
		return errors.New("--root option required")
	}

	rootDir, err := mounter.ResolveRootDir(optRootDir)
	if err != nil {
		return err
	}

	mnt := mounter.NewMounter(rootDir)
	return mnt.UmountRoot()
}
