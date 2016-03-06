package main

import (
	"fmt"
	"os"
	"path"
	"strconv"

	"github.com/codegangsta/cli"

	"github.com/yuuki1/droot/commands"
	"github.com/yuuki1/droot/log"
)

var AppHelpTemplate = `Usage: {{.Name}} {{if .Flags}}[OPTIONS] {{end}}COMMAND [arg...]

{{.Usage}}

Version: {{.Version}}{{if or .Author .Email}}

Author:{{if .Author}}
  {{.Author}}{{if .Email}} - <{{.Email}}>{{end}}{{else}}
  {{.Email}}{{end}}{{end}}
{{if .Flags}}
Options:
  {{range .Flags}}{{.}}
  {{end}}{{end}}
Commands:
  {{range .Commands}}{{.Name}}{{with .ShortName}}, {{.}}{{end}}{{ "\t" }}{{.Usage}}
  {{end}}
Run '{{.Name}} COMMAND --help' for more information on a command.
`

var commandArgs = map[string]string{
	"push":   commands.CommandArgPush,
	"pull":   commands.CommandArgPull,
	"run":    commands.CommandArgRun,
	"umount": commands.CommandArgUmount,
	"rm":     commands.CommandArgRm,
}

func setDebugOutputLevel() {
	for _, f := range os.Args {
		if f == "-D" || f == "--debug" || f == "-debug" {
			log.IsDebug = true
		}
	}

	debugEnv := os.Getenv("DROOT_DEBUG")
	if debugEnv != "" {
		showDebug, err := strconv.ParseBool(debugEnv)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing boolean value from DROOT_DEBUG: %s\n", err)
			os.Exit(1)
		}
		log.IsDebug = showDebug
	}
}

func init() {
	setDebugOutputLevel()
	argsTemplate := "{{if false}}"
	for _, command := range append(commands.Commands) {
		argsTemplate = argsTemplate + fmt.Sprintf("{{else if (eq .Name %q)}}%s %s", command.Name, command.Name, commandArgs[command.Name])
	}
	argsTemplate = argsTemplate + "{{end}}"

	cli.CommandHelpTemplate = `Usage: droot ` + argsTemplate + `

{{.Usage}}{{if .Description}}

Description:
   {{.Description}}{{end}}{{if .Flags}}

Options:
   {{range .Flags}}
   {{.}}{{end}}{{ end }}
`

	cli.AppHelpTemplate = AppHelpTemplate
}

func main() {
	app := cli.NewApp()
	app.Name = path.Base(os.Args[0])
	app.Author = "y_uuki"
	app.Email = "https://github.com/yuuki1/droot"
	app.Commands = commands.Commands
	app.CommandNotFound = cmdNotFound
	app.Usage = "droot is a super-easy container with chroot without docker."
	app.Version = Version

	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:  "debug, D",
			Usage: "Enable debug mode",
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Error(err)
	}
}

func cmdNotFound(c *cli.Context, command string) {
	log.Errorf(
		"%s: '%s' is not a %s command. See '%s --help'.",
		c.App.Name,
		command,
		c.App.Name,
		os.Args[0],
	)
}
