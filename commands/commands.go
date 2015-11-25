package commands

import (
	"github.com/codegangsta/cli"
)

var Commands = []cli.Command{
	CommandPush,
	CommandPull,
	CommandRun,
}

