package commands

import(
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
	Action: doRun,
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

func doRun(c *cli.Context) {
	command := c.Args()
	if len(command) < 1 {
		cli.ShowCommandHelp(c, "run")
		os.Exit(1)
	}

	rootDir := c.String("root")
	bindDirs := c.StringSlice("bind")

	if rootDir == "" {
		log.Error("--root option required")
		cli.ShowCommandHelp(c, "run")
		os.Exit(1)
	}

	for _, dir := range append(bindDirs, rootDir) {
		if !osutil.ExistsDir(dir) {
			log.Error(dir, "No such directory")
			os.Exit(1)
		}
	}

	// copy files
	if c.Bool("copy-files") {
		for _, f := range copyFiles {
			osutil.Cp(fp.Join("/", f), fp.Join(rootDir, f))
		}
	}

	// bind the directories
	for _, dir := range bindDirs {
		ok, err := osutil.IsDirEmpty(dir)
		if err != nil {
			log.Error(err)
			return
		}
		if ok {
			os.Create(fp.Join(dir, ".dochroot.keep"))
		}
		containerDir := fp.Join(rootDir, dir)
		if err := os.MkdirAll(containerDir, os.FileMode(0755)); err != nil {
			log.Error(err)
			return
		}
		ok, err = osutil.IsDirEmpty(containerDir)
		if err != nil {
			log.Error(err)
			return
		}
		if ok {
			osutil.BindMount(dir, containerDir)
		}
	}

	// create symlinks
	if err := osutil.Symlink("../run/lock", fp.Join(rootDir, "/var/lock")); err != nil {
		log.Error(err)
		return
	}

	// create devices
	if err := osutil.Mknod(fp.Join(rootDir, os.DevNull), syscall.S_IFCHR | uint32(os.FileMode(0666)), 1*256+3); err != nil {
		log.Error(err)
		return
	}
	if err := osutil.Mknod(fp.Join(rootDir, "/dev/zero"), syscall.S_IFCHR | uint32(os.FileMode(0666)), 1*256+3); err != nil {
		log.Error(err)
		return
	}
	for _, f := range []string{"/dev/random", "/dev/urandom"} {
		if err := osutil.Mknod(fp.Join(rootDir, f), syscall.S_IFCHR | uint32(os.FileMode(0666)), 1*256+9); err != nil {
			log.Error(err)
			return
		}
	}

	log.Debug("chroot", rootDir, command)
	if err := osutil.ChrootAndExec(keepCaps, rootDir, command...); err != nil {
		log.Error(err)
		return
	}
}
