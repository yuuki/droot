package commands

import(
	"github.com/codegangsta/cli"
)

var CommandArgPush = "--to S3_ENDPOINT DOCKER_REPOSITORY[:TAG]"
var CommandPush = cli.Command{
	Name:  "push",
	Usage: "Push an extracted docker image into s3",
	Action: doPush,
	Flags: []cli.Flag{
		cli.StringFlag{Name: "to, t", Usage: "Amazon S3 endpoint (ex. s3://example.com/containers/app.tar.gz)"},
	},
}

func doPush(c *cli.Context) {
}
