package commands

import (
	"context"

	"github.com/urfave/cli/v3"

	"github.com/yuuki/droot/log"
	"github.com/yuuki/droot/mounter"
)

func umountCommand() *cli.Command {
	return &cli.Command{
		Name:  "umount",
		Usage: "Unmount everything mounted under a root directory by 'droot run'",
		Description: "Lazily unmounts (MNT_DETACH) every mount point at or below ROOT_DIR,\n" +
			"deepest first. It is safe to run when nothing is mounted.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "root", Aliases: []string{"r"}, Usage: "root directory `ROOT_DIR` used with 'droot run' (required)"},
		},
		Action: doUmount,
	}
}

func doUmount(_ context.Context, cmd *cli.Command) error {
	setup(cmd)
	if cmd.String("root") == "" {
		return usageError(cmd, "--root is required")
	}
	if cmd.NArg() > 0 {
		return usageError(cmd, "unexpected arguments: %q", cmd.Args().Slice())
	}
	rootDir, err := mounter.ResolveRootDir(cmd.String("root"))
	if err != nil {
		return err
	}
	unmounted, err := mounter.NewMounter(rootDir).UmountAll()
	log.Debugf("unmounted %d mount point(s) under %s", len(unmounted), rootDir)
	return err
}
