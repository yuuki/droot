package commands

import (
	"errors"
	"fmt"

	"github.com/codegangsta/cli"

	"github.com/yuuki1/droot/osutil"
)

var CommandArgRm = "--root DESTINATION_DIRECTORY"

var CommandRm = cli.Command{
	Name:   "rm",
	Usage:  "Remove directory mounted by 'run' command",
	Action: fatalOnError(doRm),
	Flags: []cli.Flag{
		cli.StringFlag{Name: "root, r", Usage: "Root directory path for chrooted"},
	},
}

func doRm(c *cli.Context) error {
	rootDir := c.String("root")
	if rootDir == "" {
		cli.ShowCommandHelp(c, "run")
		return errors.New("--root option required")
	}

	if !osutil.ExistsDir(rootDir) {
		return fmt.Errorf("No such directory %s", rootDir)
	}

	if err := osutil.UmountRoot(rootDir); err != nil {
		return err
	}
	return osutil.RunCmd("rm", "-fr", rootDir)
}
