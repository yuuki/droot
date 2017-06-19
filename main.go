package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/yuuki/albio/pkg/command"
)

// CLI is the command line object.
type CLI struct {
	// outStream and errStream are the stdout and stderr
	// to write message from the CLI.
	outStream, errStream io.Writer
}

func main() {
	cli := &CLI{outStream: os.Stdout, errStream: os.Stderr}
	os.Exit(cli.Run(os.Args))
}

// Run invokes the CLI with the given arguments.
func (cli *CLI) Run(args []string) int {
	if len(args) <= 1 {
		fmt.Fprint(cli.errStream, helpText)
		return 2
	}

	var err error

	switch args[1] {
	case "export":
		err = cli.doExport(args[2:])
	case "run":
		err = cli.doRun(args[2:])
	case "umount":
		err = cli.doUmount(args[2:])
	case "-v", "--version":
		fmt.Fprintf(cli.errStream, "%s version %s, build %s \n", Name, Version, GitCommit)
		return 0
	case "-h", "--help":
		fmt.Fprint(cli.errStream, helpText)
	default:
		fmt.Fprint(cli.errStream, helpText)
		return 1
	}

	if err != nil {
		fmt.Fprintln(cli.errStream, err)
		return 2
	}

	return 0
}

var helpText = `
Usage: droot [subcommands]

  A super-lightweight application container engine with chroot without docker

Commands:
  export	export a container's filesystem as a tar archive
  run		run command into an extracted container directory
  umount        unmount directory mounted by 'run' command

Options:
  --version, -v		print version
  --help, -h            print help
`

func (cli *CLI) prepareFlags(help string) *flag.FlagSet {
	flags := flag.NewFlagSet(Name, flag.ContinueOnError)
	flags.SetOutput(cli.errStream)
	flags.Usage = func() {
		fmt.Fprint(cli.errStream, help)
	}
	return flags
}

var statusExportText = `
Usage: droot run [options]

export a container's filesystem as a tar archive

Options:
  --instance-id, -i	specify EC2 instance id
`

func (cli *CLI) doExport(args []string) error {
	var param command.StatusParam
	flags := cli.prepareFlags(statusHelpText)
	flags.StringVar(&param.InstanceID, "i", "", "")
	flags.StringVar(&param.InstanceID, "instance-id", "", "")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return commands.Export(&param)
}

var attachHelpText = `
Usage: albio attach [options]

attach loadbalancers to the EC2 instance.

Options:
  --instance-id, -i	specify EC2 instance id
`

func (cli *CLI) doAttach(args []string) error {
	var param command.AttachParam
	flags := cli.prepareFlags(attachHelpText)
	flags.StringVar(&param.InstanceID, "i", "", "")
	flags.StringVar(&param.InstanceID, "instance-id", "", "")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return command.Attach(&param)
}

var detachHelpText = `
Usage: albio detach [options]

detach loadbalancers from the EC2 instance.

Options:
  --instance-id, -i	specify EC2 instance id
`

func (cli *CLI) doDetach(args []string) error {
	var param command.DetachParam
	flags := cli.prepareFlags(detachHelpText)
	flags.StringVar(&param.InstanceID, "i", "", "")
	flags.StringVar(&param.InstanceID, "instance-id", "", "")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return command.Detach(&param)
}
