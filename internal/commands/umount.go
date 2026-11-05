package commands

import (
	"context"

	"github.com/yuuki/droot/internal/cli"
	"github.com/yuuki/droot/internal/log"
	"github.com/yuuki/droot/internal/mounter"
	"github.com/yuuki/droot/internal/osutil"
)

func umountCommand() *cli.Command {
	return &cli.Command{
		Name:  "umount",
		Usage: "Unmount everything mounted under a root directory by 'droot run'",
		Description: "Lazily unmounts (MNT_DETACH) every mount point below ROOT_DIR, deepest\n" +
			"first, and fails if some cannot be unmounted. ROOT_DIR itself is never\n" +
			"unmounted. It is safe to run when nothing is mounted.",
		Flags: []*cli.Flag{
			{Name: "root", Aliases: []string{"r"}, Kind: cli.String, Placeholder: "ROOT_DIR", Usage: "root directory used with 'droot run' (required)", Complete: "dir"},
		},
		Action: doUmount,
	}
}

func doUmount(_ context.Context, cmd *cli.Invocation) error {
	setup(cmd)
	if cmd.String("root") == "" {
		return cmd.UsageError("--root is required")
	}
	if len(cmd.Args()) > 0 {
		return cmd.UsageError("unexpected arguments: %q", cmd.Args())
	}
	rootDir, err := mounter.ResolveRootDir(cmd.String("root"))
	if err != nil {
		return err
	}
	root, err := osutil.OpenRoot(rootDir)
	if err != nil {
		return err
	}
	defer root.Close()
	unmounted, err := mounter.NewMounter(root).UmountAll()
	log.Debugf("unmounted %d mount point(s) under %s", len(unmounted), rootDir)
	return err
}
