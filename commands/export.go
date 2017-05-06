package commands

import (
	"io"
	"os"

	"github.com/pkg/errors"
	"github.com/urfave/cli"

	"github.com/yuuki/droot/docker"
)

var CommandArgExport = "-o OUTPUT DOCKER_REPOSITORY[:TAG]"
var CommandExport = cli.Command{
	Name:   "export",
	Usage:  "Export a container's filesystem as a tar archive",
	Action: fatalOnError(doExport),
	Flags: []cli.Flag{
		cli.StringFlag{Name: "o, output", Usage: "Write to a file, instead of STDOUT"},
	},
}

func doExport(c *cli.Context) error {
	if len(c.Args()) < 1 {
		cli.ShowCommandHelp(c, "export")
		return errors.New("docker repository required")
	}
	repository := c.Args().Get(0)
	if repository == "" {
		cli.ShowCommandHelp(c, "export")
		return errors.New("docker repository required")
	}

	docker, err := docker.NewClient()
	if err != nil {
		return err
	}
	imageReader, err := docker.ExportImage(repository)
	if err != nil {
		return err
	}
	defer imageReader.Close()

	if output := c.String("output"); output != "" {
		file, err := os.Create(output)
		if err != nil {
			return err
		}
		defer file.Close()

		if _, err := io.Copy(file, imageReader); err != nil {
			return errors.Wrapf(err, "Failed to write to the file %s", output)
		}

	} else {
		if _, err := io.Copy(os.Stdout, imageReader); err != nil {
			return errors.Wrapf(err, "Failed to write into stdout")
		}
	}

	return nil
}
