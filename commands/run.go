package commands

import (
	"errors"
	"fmt"
	"os"
	fp "path/filepath"
	"strings"
	"syscall"

	"github.com/codegangsta/cli"
	"github.com/hashicorp/errwrap"

	"github.com/yuuki1/droot/log"
	"github.com/yuuki1/droot/osutil"
)

var CommandArgRun = "--root ROOT_DIR [--user USER] [--group GROUP] [--bind SRC-PATH[:DEST-PATH]] [--robind SRC-PATH[:DEST-PATH]] [--no-dropcaps] -- COMMAND"
var CommandRun = cli.Command{
	Name:   "run",
	Usage:  "Run an extracted docker image from s3",
	Action: fatalOnError(doRun),
	Flags: []cli.Flag{
		cli.StringFlag{Name: "root, r", Usage: "Root directory path for chrooting"},
		cli.StringFlag{Name: "user, u", Usage: "User (ID or name) to switch before running the program"},
		cli.StringFlag{Name: "group, g", Usage: "Group (ID or name) to switch to"},
		cli.StringSliceFlag{
			Name:  "bind, b",
			Value: &cli.StringSlice{},
			Usage: "Bind mount directory (can be specifies multiple times)",
		},
		cli.StringSliceFlag{
			Name:  "robind",
			Value: &cli.StringSlice{},
			Usage: "Readonly bind mount directory (can be specifies multiple times)",
		},
		cli.BoolFlag{
			Name:  "copy-files, cp",
			Usage: "Copy host from containersuch as /etc/hosts, /etc/group, /etc/passwd, /etc/hosts",
		},
		cli.BoolFlag{Name: "no-dropcaps", Usage: "Provide COMMAND's process in chroot with root permission (dangerous)"},
	},
}

var copyFiles = []string{
	"etc/group",
	"etc/passwd",
	"etc/resolv.conf",
	"etc/hosts",
}

var keepCaps = map[uint]bool{
	0:  true, // CAP_CHOWN
	1:  true, // CAP_DAC_OVERRIDE
	2:  true, // CAP_DAC_READ_SEARCH
	3:  true, // CAP_FOWNER
	6:  true, // CAP_SETGID
	7:  true, // CAP_SETUID
	10: true, // CAP_NET_BIND_SERVICE
}

func doRun(c *cli.Context) error {
	command := c.Args()
	if len(command) < 1 {
		cli.ShowCommandHelp(c, "run")
		return errors.New("command required")
	}

	rootDir := c.String("root")
	if rootDir == "" {
		cli.ShowCommandHelp(c, "run")
		return errors.New("--root option required")
	}

	if !osutil.ExistsDir(rootDir) {
		return fmt.Errorf("No such directory %s:", rootDir)
	}

	// copy files
	if c.Bool("copy-files") {
		for _, f := range copyFiles {
			if err := osutil.Cp(fp.Join("/", f), fp.Join(rootDir, f)); err != nil {
				return fmt.Errorf("Failed to copy %s:", f, err)
			}
		}
	}

	// bind the directories
	if err := bindSystemMount(rootDir); err != nil {
		return fmt.Errorf("Failed to bind system mount:", err)
	}

	for _, dir := range c.StringSlice("bind") {
		if err := bindMount(dir, rootDir, false); err != nil {
			return fmt.Errorf("Failed to bind mount %s:", dir, err)
		}
	}
	for _, dir := range c.StringSlice("robind") {
		if err := bindMount(dir, rootDir, true); err != nil {
			return fmt.Errorf("Failed to robind mount %s:", dir, err)
		}
	}

	// create symlinks
	if err := osutil.Symlink("../run/lock", fp.Join(rootDir, "/var/lock")); err != nil {
		return fmt.Errorf("Failed to symlink lock file:", err)
	}

	if err := createDevices(rootDir); err != nil {
		return fmt.Errorf("Failed to create devices:", err)
	}

	log.Debug("chroot", rootDir, command)

	if err := syscall.Chroot(rootDir); err != nil {
		return fmt.Errorf("Failed to chroot:", err)
	}
	if err := syscall.Chdir("/"); err != nil {
		return fmt.Errorf("Failed to chdir /:", err)
	}

	if !c.Bool("no-dropcaps") {
		log.Debug("drop capabilities")
		if err := osutil.DropCapabilities(keepCaps); err != nil {
			return fmt.Errorf("Failed to drop capabilities:", err)
		}
	}

	if group := c.String("group"); group != "" {
		log.Debug("setgid", group)
		if err := osutil.SetGroup(group); err != nil {
			return fmt.Errorf("Failed to set group:", err)
		}
	}
	if user := c.String("user"); user != "" {
		log.Debug("setuid", user)
		if err := osutil.SetUser(user); err != nil {
			return fmt.Errorf("Failed to set user:", err)
		}
	}

	return osutil.Execv(command[0], command[0:], os.Environ())
}

func bindMount(bindDir string, rootDir string, readonly bool) error {
	var srcDir, destDir string

	d := strings.SplitN(bindDir, ":", 2)
	if len(d) < 2 {
		srcDir = d[0]
	} else {
		srcDir, destDir = d[0], d[1]
	}
	if destDir == "" {
		destDir = srcDir
	}

	ok, err := osutil.IsDirEmpty(srcDir)
	if err != nil {
		return err
	}
	if ok {
		if _, err := os.Create(fp.Join(srcDir, ".droot.keep")); err != nil {
			return errwrap.Wrapf("Failed to create .droot.keep: {{err}}", err)
		}
	}

	containerDir := fp.Join(rootDir, destDir)

	if err := os.MkdirAll(containerDir, os.FileMode(0755)); err != nil {
		return errwrap.Wrapf(fmt.Sprintf("Failed to mkdir %s: {{err}}", containerDir), err)
	}

	ok, err = osutil.IsDirEmpty(containerDir)
	if err != nil {
		return err
	}
	if ok {
		log.Debug("bind mount", bindDir, "to", containerDir)
		if err := osutil.BindMount(srcDir, containerDir); err != nil {
			return errwrap.Wrapf(fmt.Sprintf("Failed to bind mount %s: {{err}}", containerDir), err)
		}

		if readonly {
			log.Debug("robind mount", bindDir, "to", containerDir)
			if err := osutil.RObindMount(srcDir, containerDir); err != nil {
				return errwrap.Wrapf(fmt.Sprintf("Failed to robind mount %s: {{err}}", containerDir), err)
			}
		}
	}

	return nil
}

func bindSystemMount(rootDir string) error {
	procDir := fp.Join(rootDir, "/proc")
	if ok, err := osutil.Mounted(procDir); !ok && err == nil {
		if err := osutil.RunCmd("mount", "-t", "proc", "none", procDir); err != nil {
			return errwrap.Wrapf("Failed to mount /proc: {{err}}", err)
		}
	}

	sysDir := fp.Join(rootDir, "/sys")
	if ok, err := osutil.Mounted(sysDir); !ok && err == nil {
		if err := osutil.RunCmd("mount", "--rbind", "/sys", fp.Join(rootDir, "/sys")); err != nil {
			return errwrap.Wrapf("Failed to mount /sys: {{err}}", err)
		}
	}

	return nil
}

func createDevices(rootDir string) error {
	if err := osutil.Mknod(fp.Join(rootDir, os.DevNull), syscall.S_IFCHR|uint32(os.FileMode(0666)), 1*256+3); err != nil {
		return err
	}

	if err := osutil.Mknod(fp.Join(rootDir, "/dev/zero"), syscall.S_IFCHR|uint32(os.FileMode(0666)), 1*256+3); err != nil {
		return err
	}

	for _, f := range []string{"/dev/random", "/dev/urandom"} {
		if err := osutil.Mknod(fp.Join(rootDir, f), syscall.S_IFCHR|uint32(os.FileMode(0666)), 1*256+9); err != nil {
			return err
		}
	}

	return nil
}
