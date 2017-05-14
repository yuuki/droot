package commands

import (
	"github.com/urfave/cli"

	"github.com/yuuki/droot/log"
)

var Commands = []cli.Command{
	CommandExport,
	CommandRun,
	CommandUmount,
	CommandRm,
}

func fatalOnError(command func(context *cli.Context) error) func(context *cli.Context) {
	return func(context *cli.Context) {
		if err := command(context); err != nil {
			log.Error(err)
		}
	}
}
