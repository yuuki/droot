package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/yuuki/droot/environ"
	"github.com/yuuki/droot/identity"
	"github.com/yuuki/droot/log"
	"github.com/yuuki/droot/mounter"
	"github.com/yuuki/droot/osutil"
)

func runCommand() *cli.Command {
	stopAfterCommand := 1 // everything after COMMAND belongs to COMMAND
	return &cli.Command{
		Name:      "run",
		Usage:     "Run a command in a container root directory with chroot(2)",
		ArgsUsage: "[--] COMMAND [ARG...]",
		Description: "Mounts /proc, /sys and the requested bind mounts under ROOT_DIR, creates the\n" +
			"basic device files, chroots into ROOT_DIR, drops capabilities from the\n" +
			"bounding set, switches to --user/--group and executes COMMAND with the\n" +
			"environment saved by 'droot export' (/.drootenv) plus --env.\n\n" +
			"Mounts are left in place after COMMAND exits; remove them with 'droot umount'.\n" +
			"Must be run as root.",
		StopOnNthArg: &stopAfterCommand,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "root", Aliases: []string{"r"}, Usage: "root directory `ROOT_DIR` to chroot into (required)"},
			&cli.StringFlag{Name: "user", Aliases: []string{"u"}, Usage: "`USER` (name or ID) to run COMMAND as; names are looked up in ROOT_DIR/etc/passwd, then on the host"},
			&cli.StringFlag{Name: "group", Aliases: []string{"g"}, Usage: "`GROUP` (name or ID) to run COMMAND as (default: the primary group of --user)"},
			&cli.StringSliceFlag{Name: "bind", Aliases: []string{"b"}, Usage: "bind mount `SRC[:DEST]` from the host into the container (repeatable)"},
			&cli.StringSliceFlag{Name: "robind", Usage: "read-only bind mount `SRC[:DEST]` (repeatable)"},
			&cli.BoolFlag{Name: "copy-files", Aliases: []string{"cp"}, Usage: "copy the host's " + strings.Join(hostFiles, ", ") + " into the container"},
			&cli.BoolFlag{Name: "no-dropcaps", Usage: "do not drop capabilities from the bounding set (dangerous)"},
			&cli.StringSliceFlag{Name: "env", Aliases: []string{"e"}, Usage: "set environment variable `KEY=VALUE`, overriding /.drootenv (repeatable)"},
		},
		Action: doRun,
	}
}

// hostFiles are copied from the host by --copy-files.
var hostFiles = []string{"/etc/group", "/etc/passwd", "/etc/resolv.conf", "/etc/hosts"}

// keepCaps are the capabilities kept in the bounding set unless --no-dropcaps
// is given. CAP_DAC_READ_SEARCH is deliberately not kept: together with
// open_by_handle_at(2) it allows reading any file of the host, escaping the
// chroot (the "shocker" exploit), which is why Docker drops it too.
var keepCaps = map[uintptr]bool{
	0:  true, // CAP_CHOWN
	1:  true, // CAP_DAC_OVERRIDE
	3:  true, // CAP_FOWNER
	6:  true, // CAP_SETGID
	7:  true, // CAP_SETUID
	10: true, // CAP_NET_BIND_SERVICE
}

// devices are created in ROOT_DIR/dev.
var devices = []struct {
	path         string
	major, minor uint32
}{
	{"/dev/null", 1, 3},
	{"/dev/zero", 1, 5},
	{"/dev/random", 1, 8},
	{"/dev/urandom", 1, 9},
}

type bindMount struct {
	host, container string
	readonly        bool
}

func doRun(_ context.Context, cmd *cli.Command) error {
	setup(cmd)
	if runtime.GOOS != "linux" {
		return fmt.Errorf("droot run is only supported on Linux (running on %s)", runtime.GOOS)
	}

	argv := cmd.Args().Slice()
	if len(argv) == 0 || argv[0] == "" {
		return usageError(cmd, "a command to run is required")
	}
	if cmd.String("root") == "" {
		return usageError(cmd, "--root is required")
	}
	if os.Geteuid() != 0 {
		return errors.New("droot run must be run as root (it needs to mount, chroot and switch users)")
	}

	rootDir, err := mounter.ResolveRootDir(cmd.String("root"))
	if err != nil {
		return err
	}

	extraEnv := cmd.StringSlice("env")
	if err := environ.Validate(extraEnv); err != nil {
		return err
	}

	var binds []bindMount
	for _, spec := range cmd.StringSlice("bind") {
		b, err := parseBindOption(spec)
		if err != nil {
			return err
		}
		binds = append(binds, b)
	}
	for _, spec := range cmd.StringSlice("robind") {
		b, err := parseBindOption(spec)
		if err != nil {
			return err
		}
		b.readonly = true
		binds = append(binds, b)
	}

	// Every path inside the container is resolved through this descriptor
	// so that a process of a previous run cannot redirect droot to the host
	// file system by swapping directories for symlinks.
	root, err := osutil.OpenRoot(rootDir)
	if err != nil {
		return err
	}
	defer root.Close()

	if cmd.Bool("copy-files") {
		if err := copyHostFiles(root); err != nil {
			return err
		}
	}

	cred, err := identity.Resolve(rootDir, cmd.String("user"), cmd.String("group"))
	if err != nil {
		return err
	}
	log.Debugf("running as uid=%d gid=%d groups=%v", cred.UID, cred.GID, cred.Groups)

	mnt := mounter.NewMounter(root)
	if err := mnt.MountSysProc(); err != nil {
		return err
	}
	for _, b := range binds {
		if err := mnt.BindMount(b.host, b.container, b.readonly); err != nil {
			return fmt.Errorf("bind mount %s on %s: %w", b.host, b.container, err)
		}
	}

	linkVarLock(root)

	if err := createDevices(root); err != nil {
		return err
	}

	return execInRoot(rootDir, argv, extraEnv, cred, !cmd.Bool("no-dropcaps"))
}

// parseBindOption parses SRC[:DEST]. DEST defaults to SRC; both must be
// absolute paths.
func parseBindOption(spec string) (bindMount, error) {
	host, container, _ := strings.Cut(spec, ":")
	if container == "" {
		container = host
	}
	if !filepath.IsAbs(host) {
		return bindMount{}, fmt.Errorf("invalid bind mount %q: %s is not an absolute path", spec, host)
	}
	if !filepath.IsAbs(container) {
		return bindMount{}, fmt.Errorf("invalid bind mount %q: %s is not an absolute path", spec, container)
	}
	return bindMount{host: filepath.Clean(host), container: filepath.Clean(container)}, nil
}

func copyHostFiles(root *osutil.Root) error {
	for _, f := range hostFiles {
		if err := root.CopyFileIn(f, f); err != nil {
			return fmt.Errorf("copy %s into the container: %w", f, err)
		}
		log.Debugf("copied %s into %s", f, root.Path())
	}
	return nil
}

// linkVarLock creates the /var/lock -> ../run/lock symlink when /var exists
// and /var/lock does not. Failures are not fatal.
func linkVarLock(root *osutil.Root) {
	varDir, err := root.OpenDir("/var", false, nil)
	if err != nil {
		return
	}
	_ = varDir.Close()
	if err := root.SymlinkIfMissing("../run/lock", "/var/lock"); err != nil {
		log.Debugf("cannot create /var/lock: %v", err)
	}
}

// createDevices creates the device nodes, owned by root with mode 0666. If
// /dev is not on the root's own file system (for instance a bind mount of
// the host's /dev), nothing is created there: droot must not touch host
// devices.
func createDevices(root *osutil.Root) error {
	onRoot := mounter.OnMountOf(root.Fd())
	for _, d := range devices {
		err := root.MknodChar(d.path, d.major, d.minor, 0o666, onRoot)
		if errors.Is(err, mounter.ErrOtherMount) {
			log.Warnf("not creating device nodes: /dev in the container is a mount point (e.g. a bind mount of the host's /dev)")
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// execInRoot chroots into rootDir, drops privileges and executes argv. It only
// returns on error.
func execInRoot(rootDir string, argv, extraEnv []string, cred *identity.Credential, dropCaps bool) error {
	// The capability bounding set is a per-thread attribute and execve(2)
	// uses the calling thread's: stay on this OS thread until exec. The
	// thread is never unlocked; on error the process exits.
	runtime.LockOSThread()

	if err := osutil.Chroot(rootDir); err != nil {
		return err
	}

	env := extraEnv
	if osutil.ExistsFile(environ.EnvFilePath) {
		fileEnv, err := environ.ReadEnvFile(environ.EnvFilePath)
		if err != nil {
			return err
		}
		if env, err = environ.Merge(fileEnv, extraEnv); err != nil {
			return err
		}
	}

	if dropCaps {
		if err := osutil.DropBoundingCapabilities(keepCaps); err != nil {
			return fmt.Errorf("drop capabilities: %w", err)
		}
	}
	if err := osutil.SetCredentials(cred.UID, cred.GID, cred.Groups); err != nil {
		return err
	}

	// Resolve the command with the container's PATH and the new
	// credentials, not with the PATH of the (sudo) environment droot was
	// started from.
	pathEnv, _ := environ.Lookup(env, "PATH")
	argv0, err := osutil.LookPath(argv[0], pathEnv, osutil.IsExecutable)
	if err != nil {
		return err
	}
	return osutil.Exec(argv0, argv, env)
}
