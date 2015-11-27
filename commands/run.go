package commands

import(
	"errors"
	"fmt"
	"os"
	fp "path/filepath"
	"syscall"

	"github.com/codegangsta/cli"

	"github.com/yuuki1/dochroot/log"
	"github.com/yuuki1/dochroot/osutil"
)

var CommandArgRun = "--root rootDir [--bind BIND_MOUNT_DIR] COMMAND"
var CommandRun = cli.Command{
	Name:  "run",
	Usage: "Run an extracted docker image from s3",
	Action: fatalOnError(doRun),
	Flags: []cli.Flag{
		cli.StringFlag{Name: "root, r", Usage: "Root directory path for chrooting"},
		cli.StringSliceFlag{
			Name: "bind, b",
			Value: &cli.StringSlice{},
			Usage: "Bind mount directory (can be specifies multiple times)",
		},
		cli.BoolFlag{
			Name: "copy-files, cp",
			Usage: "copy host from containersuch as /etc/hosts, /etc/group, /etc/passwd, /etc/hosts",
		},
	},
}

var copyFiles = []string{
	"etc/group",
	"etc/passwd",
	"etc/resolv.conf",
	"etc/hosts",
}

var keepCaps = map[uint]bool{
	2:	true,	// CAP_DAC_READ_SEARCH
	6:	true,	// CAP_SETGID
	7:	true,	// CAP_SETUID
	10:	true,	// CAP_NET_BIND_SERVICE
}

func doRun(c *cli.Context) error {
	command := c.Args()
	if len(command) < 1 {
		cli.ShowCommandHelp(c, "run")
		return errors.New("command required")
	}

	rootDir := c.String("root")
	bindDirs := c.StringSlice("bind")

	if rootDir == "" {
		cli.ShowCommandHelp(c, "run")
		return errors.New("--root option required")
	}

	for _, dir := range append(bindDirs, rootDir) {
		if !osutil.ExistsDir(dir) {
			return fmt.Errorf("No such directory %s", dir)
		}
	}

	// copy files
	if c.Bool("copy-files") {
		for _, f := range copyFiles {
			if err := osutil.Cp(fp.Join("/", f), fp.Join(rootDir, f)); err != nil {
				return err
			}
		}
	}

	// bind the directories
	for _, dir := range bindDirs {
		ok, err := osutil.IsDirEmpty(dir)
		if err != nil {
			return err
		}
		if ok {
			if _, err := os.Create(fp.Join(dir, ".dochroot.keep")); err != nil {
				return err
			}
		}
		containerDir := fp.Join(rootDir, dir)
		if err := os.MkdirAll(containerDir, os.FileMode(0755)); err != nil {
			return err
		}
		ok, err = osutil.IsDirEmpty(containerDir)
		if err != nil {
			return err
		}
		if ok {
			osutil.BindMount(dir, containerDir)
		}
	}

	// create symlinks
	if err := osutil.Symlink("../run/lock", fp.Join(rootDir, "/var/lock")); err != nil {
		return err
	}

	// create devices
	if err := osutil.Mknod(fp.Join(rootDir, os.DevNull), syscall.S_IFCHR | uint32(os.FileMode(0666)), 1*256+3); err != nil {
		return err
	}
	if err := osutil.Mknod(fp.Join(rootDir, "/dev/zero"), syscall.S_IFCHR | uint32(os.FileMode(0666)), 1*256+3); err != nil {
		return err
	}
	for _, f := range []string{"/dev/random", "/dev/urandom"} {
		if err := osutil.Mknod(fp.Join(rootDir, f), syscall.S_IFCHR | uint32(os.FileMode(0666)), 1*256+9); err != nil {
			return err
		}
	}

	log.Debug("chroot", rootDir, command)
	if err := osutil.ChrootAndExec(keepCaps, rootDir, command...); err != nil {
		return err
	}

	return nil
}
