package commands

import (
	"golang.org/x/sys/unix"
	"errors"
	"fmt"
	"os"
	fp "path/filepath"
	"strings"

	"github.com/codegangsta/cli"
	"github.com/docker/docker/pkg/fileutils"

	"github.com/yuuki/droot/environ"
	"github.com/yuuki/droot/errwrap"
	"github.com/yuuki/droot/log"
	"github.com/yuuki/droot/osutil"
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
			Usage: "Copy host files to container such as /etc/group, /etc/passwd, /etc/resolv.conf, /etc/hosts",
		},
		cli.BoolFlag{Name: "no-dropcaps", Usage: "Provide COMMAND's process in chroot with root permission (dangerous)"},
		cli.StringSliceFlag{
			Name: "env, e",
			Value: &cli.StringSlice{},
			Usage: "Set environment variables",
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

	var err error
	rootDir, err = fp.Abs(rootDir)
	if err != nil {
		return err
	}
	rootDir, err = os.Readlink(rootDir)
	if err != nil {
		return err
	}

	// Check env format KEY=VALUE
	env := c.StringSlice("env")
	if len(env) > 0 {
		for _, e := range env {
			if len(strings.SplitN(e, "=", 2)) != 2 {
				return fmt.Errorf("Invalid env format: %s", e)
			}
		}
	}

	uid, gid := os.Getuid(), os.Getgid()

	if group := c.String("group"); group != "" {
		if gid, err = osutil.LookupGroup(group); err != nil {
			return fmt.Errorf("Failed to lookup group: %s", err)
		}
	}
	if user := c.String("user"); user != "" {
		if uid, err = osutil.LookupUser(user); err != nil {
			return fmt.Errorf("Failed to lookup user: %s", err)
		}
	}

	// copy files
	if c.Bool("copy-files") {
		for _, f := range copyFiles {
			srcFile, destFile := fp.Join("/", f), fp.Join(rootDir, f)
			if err := osutil.Cp(srcFile, destFile); err != nil {
				return fmt.Errorf("Failed to copy %s: %s", f, err)
			}
			if err := os.Lchown(destFile, uid, gid); err != nil {
				return fmt.Errorf("Failed to lchown %s: %s", f, err)
			}
		}
	}

	// mount -t proc none {{rootDir}}/proc
	if err := osutil.MountIfNotMounted("none", fp.Join(rootDir, "/proc"), "proc", ""); err != nil {
		return fmt.Errorf("Failed to mount /proc: %s", err)
	}
	// mount --rbind /sys {{rootDir}}/sys
	if err := osutil.MountIfNotMounted("/sys", fp.Join(rootDir, "/sys"), "none", "rbind"); err != nil {
		return fmt.Errorf("Failed to mount /sys: %s", err)
	}

	for _, dir := range c.StringSlice("bind") {
		if err := bindMount(dir, rootDir, false); err != nil {
			return fmt.Errorf("Failed to bind mount %s: %s", dir, err)
		}
	}
	for _, dir := range c.StringSlice("robind") {
		if err := bindMount(dir, rootDir, true); err != nil {
			return fmt.Errorf("Failed to robind mount %s: %s", dir, err)
		}
	}

	// create symlinks
	if err := osutil.Symlink("../run/lock", fp.Join(rootDir, "/var/lock")); err != nil {
		return fmt.Errorf("Failed to symlink lock file: %s", err)
	}

	if err := createDevices(rootDir, uid, gid); err != nil {
		return fmt.Errorf("Failed to create devices: %s", err)
	}

	if err := osutil.Chroot(rootDir); err != nil {
		return fmt.Errorf("Failed to chroot: %s", err)
	}

	if !c.Bool("no-dropcaps") {
		log.Debug("drop capabilities")
		if err := osutil.DropCapabilities(keepCaps); err != nil {
			return fmt.Errorf("Failed to drop capabilities: %s", err)
		}
	}

	log.Debug("setgid", gid)
	if err := osutil.Setgid(gid); err != nil {
		return fmt.Errorf("Failed to set group %d: %s", gid, err)
	}
	log.Debug("setuid", uid)
	if err := osutil.Setuid(uid); err != nil {
		return fmt.Errorf("Failed to set user %d: %s", uid, err)
	}

	if osutil.ExistsFile(environ.DROOT_ENV_FILE_PATH) {
		envFromFile, err := environ.GetEnvironFromEnvFile(environ.DROOT_ENV_FILE_PATH)
		if err != nil {
			return fmt.Errorf("Failed to read environ from '%s'", environ.DROOT_ENV_FILE_PATH)
		}
		env, err = environ.MergeEnviron(envFromFile, env)
		if err != nil {
			return fmt.Errorf("Failed to merge environ: %s", err)
		}
	}
	return osutil.Execv(command[0], command[0:], env)
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
			return errwrap.Wrapf(err, "Failed to create .droot.keep: {{err}}")
		}
	}

	containerDir := fp.Join(rootDir, destDir)

	if err := fileutils.CreateIfNotExists(containerDir, true); err != nil { // mkdir -p
		return errwrap.Wrapff(err, "Failed to create directory: %s: {{err}}", containerDir)
	}

	ok, err = osutil.IsDirEmpty(containerDir)
	if err != nil {
		return err
	}
	if ok {
		log.Debug("bind mount", bindDir, "to", containerDir)
		if err := osutil.MountIfNotMounted(srcDir, containerDir, "none", "bind,rw"); err != nil {
			return errwrap.Wrapff(err, "Failed to bind mount %s: {{err}}", containerDir)
		}

		if readonly {
			log.Debug("robind mount", bindDir, "to", containerDir)
			if err := osutil.MountIfNotMounted(srcDir, containerDir, "none", "remount,ro,bind"); err != nil {
				return errwrap.Wrapff(err, "Failed to robind mount %s: {{err}}", containerDir)
			}
		}
	}

	return nil
}

func createDevices(rootDir string, uid, gid int) error {
	nullDir := fp.Join(rootDir, os.DevNull)
	if err := osutil.Mknod(nullDir, unix.S_IFCHR|uint32(os.FileMode(0666)), 1*256+3); err != nil {
		return err
	}

	if err := os.Lchown(nullDir, uid, gid); err != nil {
		return errwrap.Wrapff(err, "Failed to lchown %s: {{err}}", nullDir)
	}

	zeroDir := fp.Join(rootDir, "/dev/zero")
	if err := osutil.Mknod(zeroDir, unix.S_IFCHR|uint32(os.FileMode(0666)), 1*256+3); err != nil {
		return err
	}

	if err := os.Lchown(zeroDir, uid, gid); err != nil {
		return errwrap.Wrapff(err, "Failed to lchown %s:", zeroDir)
	}

	for _, f := range []string{"/dev/random", "/dev/urandom"} {
		randomDir := fp.Join(rootDir, f)
		if err := osutil.Mknod(randomDir, unix.S_IFCHR|uint32(os.FileMode(0666)), 1*256+9); err != nil {
			return err
		}

		if err := os.Lchown(randomDir, uid, gid); err != nil {
			return errwrap.Wrapff(err, "Failed to lchown %s: {{err}}", randomDir)
		}
	}

	return nil
}
