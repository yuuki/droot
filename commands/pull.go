package commands

import(
	"github.com/codegangsta/cli"
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
}
