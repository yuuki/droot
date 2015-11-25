package commands

import(
	"github.com/codegangsta/cli"
)

var CommandArgRun = "--root ROOT_DIR [--bind BIND_MOUNT_DIR] COMMAND"
var CommandRun = cli.Command{
	Name:  "run",
	Usage: "Run an extracted docker image from s3",
	Action: doRun,
	Flags: []cli.Flag{
		cli.StringSliceFlag{
			Name: "bind, b",
			Value: &cli.StringSlice{},
			Usage: "Bind mount directory (can be specifies multiple times)",
		},
		cli.StringFlag{Name: "root, r", Usage: "Root directory path for chrooting"},
	},
}

func doRun(c *cli.Context) {
}
