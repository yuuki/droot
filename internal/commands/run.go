package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"

	"github.com/yuuki/droot/internal/cli"
	"github.com/yuuki/droot/internal/environ"
	"github.com/yuuki/droot/internal/identity"
	"github.com/yuuki/droot/internal/imageconfig"
	"github.com/yuuki/droot/internal/log"
	"github.com/yuuki/droot/internal/mounter"
	"github.com/yuuki/droot/internal/osutil"
	"github.com/yuuki/droot/internal/seccomp"
)

func runCommand() *cli.Command {
	return &cli.Command{
		Name:      "run",
		Usage:     "Run a command in a container root directory with chroot(2)",
		ArgsUsage: "[--] [COMMAND [ARG...]]",
		Description: "Mounts /proc, /sys and the requested bind mounts under ROOT_DIR, creates the\n" +
			"basic device files, chroots into ROOT_DIR, drops capabilities from the\n" +
			"bounding set, switches to --user/--group and executes COMMAND with the\n" +
			"environment saved by 'droot export' plus --env-file and --env.\n\n" +
			"Without COMMAND, the ENTRYPOINT and CMD of the image saved by 'droot export'\n" +
			"(/.droot/config.json) are run; COMMAND replaces both. The image's USER and\n" +
			"WORKDIR are the defaults of --user and --workdir.\n\n" +
			"The options must come before COMMAND: everything from COMMAND on is passed\n" +
			"to it verbatim. Mounts are left in place after COMMAND exits; remove them\n" +
			"with 'droot umount'. Must be run as root.",
		StopAtFirstArg: true, // everything from COMMAND on belongs to COMMAND
		ArgsComplete:   "command",
		Flags: []*cli.Flag{
			{Name: "root", Aliases: []string{"r"}, Kind: cli.String, Placeholder: "ROOT_DIR", Complete: "dir",
				Usage: "root directory to chroot into (required)"},
			{Name: "user", Aliases: []string{"u"}, Kind: cli.String, Placeholder: "USER", Complete: "user",
				Usage: "user (name or ID) to run COMMAND as (default: the image's USER, or droot's own user); names are looked up in ROOT_DIR/etc/passwd, then on the host"},
			{Name: "group", Aliases: []string{"g"}, Kind: cli.String, Placeholder: "GROUP", Complete: "group",
				Usage: "group (name or ID) to run COMMAND as (default: the primary group of --user)"},
			{Name: "workdir", Aliases: []string{"w"}, Kind: cli.String, Placeholder: "DIR",
				Usage: "working directory of COMMAND inside the container (default: the image's WORKDIR, or /)"},
			{Name: "bind", Aliases: []string{"b"}, Kind: cli.Strings, Placeholder: "SRC[:DEST][:OPTS]", Complete: "file",
				Usage: "bind mount SRC from the host on DEST (default: SRC) in the container; OPTS is a comma-separated list of ro, rw and rbind (recursive)"},
			{Name: "robind", Kind: cli.Strings, Placeholder: "SRC[:DEST][:OPTS]", Complete: "file",
				Usage: "read-only bind mount (same as --bind SRC:DEST:ro)"},
			{Name: "tmpfs", Kind: cli.Strings, Placeholder: "PATH[:OPTS]",
				Usage: "mount a tmpfs on PATH in the container; OPTS are mount flags (ro, exec, ...: nosuid,nodev,noexec by default) and tmpfs options (size=64m, mode=1777, ...)"},
			{Name: "copy-files", Aliases: []string{"cp"}, Kind: cli.Bool,
				Usage: "copy the host's " + strings.Join(hostFiles, ", ") + " into the container"},
			{Name: "bind-net-files", Kind: cli.Bool,
				Usage: "read-only bind mount the host's " + strings.Join(netFiles, " and ") + " into the container, so that COMMAND sees later changes made to them in place (unlike --cp)"},
			{Name: "no-dropcaps", Kind: cli.Bool,
				Usage: "do not drop capabilities (dangerous)"},
			{Name: "cap-add", Kind: cli.Strings, Placeholder: "CAP",
				Usage: "also keep the capability CAP (e.g. NET_RAW, SYS_PTRACE, or ALL) in the bounding set"},
			{Name: "cap-drop", Kind: cli.Strings, Placeholder: "CAP",
				Usage: "drop the capability CAP (e.g. CHOWN, or ALL) from the ones kept by default"},
			{Name: "ulimit", Kind: cli.Strings, Placeholder: "NAME=SOFT[:HARD]",
				Usage: "set a resource limit of COMMAND, e.g. nofile=65536 or core=0:unlimited (names of ulimit, as in docker run)"},
			{Name: "unshare", Kind: cli.Bool,
				Usage: "make the mounts in a private mount namespace: they disappear when COMMAND and its children exit, and need no 'droot umount'"},
			{Name: "unshare-ipc", Kind: cli.Bool,
				Usage: "run COMMAND in a private IPC namespace (System V IPC, POSIX message queues)"},
			{Name: "hostname", Kind: cli.String, Placeholder: "NAME",
				Usage: "run COMMAND in a private UTS namespace with the host name NAME"},
			{Name: "read-only", Kind: cli.Bool,
				Usage: "make the root directory read-only for COMMAND (implies --unshare); use --tmpfs or --bind for the paths it writes"},
			{Name: "no-mask", Kind: cli.Bool,
				Usage: "do not mask the sensitive files of /proc and /sys nor make /proc/sys, /sys and the like read-only (dangerous)"},
			{Name: "no-new-privileges", Kind: cli.Bool,
				Usage: "set no_new_privs: setuid programs and file capabilities cannot grant privileges to COMMAND (default: true; --no-new-privileges=false lets programs such as sudo or su work)"},
			{Name: "seccomp", Kind: cli.String, Placeholder: "PROFILE",
				Usage: "seccomp profile: default (deny the system calls Docker's default profile denies, such as unshare, mount, keyctl, bpf; amd64, arm64, arm and 386) or unconfined (the default)"},
			{Name: "env", Aliases: []string{"e"}, Kind: cli.Strings, Placeholder: "KEY[=VALUE]",
				Usage: "set an environment variable, overriding /.drootenv and --env-file; a bare KEY takes its value from droot's own environment"},
			{Name: "env-file", Kind: cli.Strings, Placeholder: "FILE", Complete: "file",
				Usage: "read KEY=VALUE lines from the host's FILE, overriding /.drootenv; a bare KEY takes its value from droot's own environment"},
		},
		Action: doRun,
	}
}

// hostFiles are copied from the host by --copy-files.
var hostFiles = []string{"/etc/group", "/etc/passwd", "/etc/resolv.conf", "/etc/hosts"}

// netFiles are bind mounted from the host by --bind-net-files.
var netFiles = []string{"/etc/resolv.conf", "/etc/hosts"}

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

// boundingSet returns the capabilities kept in the bounding set: keepCaps,
// emptied by --cap-drop ALL or filled by --cap-add ALL, plus the other
// --cap-add, minus the other --cap-drop.
func boundingSet(add, drop []string) (map[uintptr]bool, error) {
	parse := func(opt string, names []string) (all bool, caps []uintptr, err error) {
		for _, n := range names {
			if strings.EqualFold(n, "ALL") {
				all = true
				continue
			}
			c, ok := osutil.Capability(n)
			if !ok {
				return false, nil, fmt.Errorf("%s: unknown capability %q", opt, n)
			}
			caps = append(caps, c)
		}
		return all, caps, nil
	}
	addAll, adds, err := parse("--cap-add", add)
	if err != nil {
		return nil, err
	}
	dropAll, drops, err := parse("--cap-drop", drop)
	if err != nil {
		return nil, err
	}
	keep := maps.Clone(keepCaps)
	if dropAll {
		clear(keep)
	}
	if addAll {
		for _, c := range osutil.AllCapabilities() {
			keep[c] = true
		}
	}
	for _, c := range adds {
		keep[c] = true
	}
	for _, c := range drops {
		delete(keep, c)
	}
	return keep, nil
}

// devices are created in ROOT_DIR/dev.
var devices = []struct {
	path         string
	major, minor uint32
}{
	{"/dev/null", 1, 3},
	{"/dev/zero", 1, 5},
	{"/dev/full", 1, 7},
	{"/dev/random", 1, 8},
	{"/dev/urandom", 1, 9},
	{"/dev/tty", 5, 0},
}

// devLinks are the symbolic links created in ROOT_DIR/dev.
var devLinks = []struct{ path, target string }{
	{"/dev/fd", "/proc/self/fd"},
	{"/dev/stdin", "/proc/self/fd/0"},
	{"/dev/stdout", "/proc/self/fd/1"},
	{"/dev/stderr", "/proc/self/fd/2"},
	// The ptmx of the devpts instance mounted on /dev/pts. A character
	// device here would allocate pseudo-terminals in the host's instance.
	{"/dev/ptmx", "pts/ptmx"},
}

// devpts are the options of the devpts instance mounted on /dev/pts, like
// Docker's.
var devpts = mounter.FSOptions{NoSuid: true, NoExec: true, Data: []string{"newinstance", "ptmxmode=0666", "mode=0620", "gid=5"}}

// shmfs are the options of the tmpfs mounted on /dev/shm, like Docker's.
var shmfs = mounter.FSOptions{NoSuid: true, NoDev: true, NoExec: true, Data: []string{"mode=1777", "size=64m"}}

// tmpfsDefaults are the default flags of --tmpfs mounts.
var tmpfsDefaults = mounter.FSOptions{NoSuid: true, NoDev: true, NoExec: true}

type tmpfsMount struct {
	path string
	opts mounter.FSOptions
}

type userMount struct {
	target string
	mount  func() error
}

// parseTmpfsOption parses PATH[:OPTS] (see mounter.ParseTmpfsOptions).
func parseTmpfsOption(spec string) (tmpfsMount, error) {
	p, opts, _ := strings.Cut(spec, ":")
	if !filepath.IsAbs(p) {
		return tmpfsMount{}, fmt.Errorf("invalid tmpfs mount %q: %s is not an absolute path", spec, p)
	}
	o, err := mounter.ParseTmpfsOptions(opts, tmpfsDefaults)
	if err != nil {
		return tmpfsMount{}, fmt.Errorf("invalid tmpfs mount %q: %w", spec, err)
	}
	return tmpfsMount{path: filepath.Clean(p), opts: o}, nil
}

type bindMount struct {
	host, container string
	opts            mounter.BindOptions
}

func doRun(ctx context.Context, cmd *cli.Invocation) (err error) {
	if err := setup(cmd); err != nil {
		return err
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("droot run is only supported on Linux (running on %s)", runtime.GOOS)
	}

	argv := cmd.Args()
	if len(argv) > 0 && argv[0] == "" {
		return cmd.UsageError("COMMAND must not be empty")
	}
	if cmd.String("root") == "" {
		return cmd.UsageError("--root is required")
	}
	if p := cmd.String("seccomp"); cmd.IsSet("seccomp") && !slices.Contains(seccomp.Profiles, p) {
		return cmd.UsageError("unknown seccomp profile %q: want %s", p, strings.Join(seccomp.Profiles, " or "))
	}
	// An empty --user, e.g. from an unset variable in a unit file, must not
	// silently run COMMAND as root instead of the image's USER.
	for _, name := range []string{"user", "group"} {
		if cmd.IsSet(name) && cmd.String(name) == "" {
			return cmd.UsageError("--%s must not be empty", name)
		}
	}
	if cmd.IsSet("hostname") && (cmd.String("hostname") == "" || len(cmd.String("hostname")) > 64) {
		return cmd.UsageError("--hostname must have 1 to 64 characters")
	}
	workdir := cmd.String("workdir")
	if cmd.IsSet("workdir") && !filepath.IsAbs(workdir) {
		return cmd.UsageError("--workdir must be an absolute path: %q", workdir)
	}

	rootDir, err := mounter.ResolveRootDir(cmd.String("root"))
	if err != nil {
		return err
	}

	// The environment given on the command line, in increasing order of
	// precedence: --env-file, then --env. Files and values are read on the
	// host, before the chroot.
	var extraEnv []string
	for _, file := range cmd.Strings("env-file") {
		env, err := environ.ReadEnvFileFrom(file, os.LookupEnv)
		if err != nil {
			return fmt.Errorf("--env-file: %w", err)
		}
		extraEnv = append(extraEnv, env...)
	}
	cliEnv, err := environ.ExpandBare(cmd.Strings("env"), os.LookupEnv)
	if err != nil {
		return err
	}
	extraEnv = append(extraEnv, cliEnv...)
	if err := environ.Validate(extraEnv); err != nil {
		return err
	}

	var binds []bindMount
	for _, spec := range cmd.Strings("bind") {
		b, err := parseBindOption(spec, false)
		if err != nil {
			return err
		}
		binds = append(binds, b)
	}
	for _, spec := range cmd.Strings("robind") {
		b, err := parseBindOption(spec, true)
		if err != nil {
			return err
		}
		binds = append(binds, b)
	}
	if cmd.Bool("no-dropcaps") && (cmd.IsSet("cap-add") || cmd.IsSet("cap-drop")) {
		return cmd.UsageError("--cap-add and --cap-drop cannot be used with --no-dropcaps")
	}
	keep, err := boundingSet(cmd.Strings("cap-add"), cmd.Strings("cap-drop"))
	if err != nil {
		return err
	}
	var rlimits []osutil.Rlimit
	for _, spec := range cmd.Strings("ulimit") {
		l, err := osutil.ParseRlimit(spec)
		if err != nil {
			return err
		}
		rlimits = append(rlimits, l)
	}
	bindNetFiles := cmd.Bool("bind-net-files")
	var tmpfs []tmpfsMount
	for _, spec := range cmd.Strings("tmpfs") {
		t, err := parseTmpfsOption(spec)
		if err != nil {
			return err
		}
		tmpfs = append(tmpfs, t)
	}

	// Every path inside the container is resolved through this descriptor
	// so that a process of a previous run cannot redirect droot to the host
	// file system by swapping directories for symlinks.
	root, err := osutil.OpenRoot(rootDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }() // root may be opened again below

	// The image configuration saved by droot export, if any, gives the
	// defaults of COMMAND, --user and --workdir, and the environment.
	cfg, err := imageconfig.Read(root)
	if err != nil {
		return err
	}
	argv = cfg.Command(argv)
	if len(argv) == 0 || argv[0] == "" {
		if cfg != nil {
			return cmd.UsageError("a command to run is required: the image defines neither ENTRYPOINT nor CMD")
		}
		return cmd.UsageError("a command to run is required")
	}
	if os.Geteuid() != 0 {
		return errors.New("droot run must be run as root (it needs to mount, chroot and switch users)")
	}

	// From now on and up to the exec, everything happens on this OS
	// thread: the capability bounding set is a per-thread attribute, and
	// so are the namespaces of --unshare. The thread is never unlocked;
	// on error the process exits.
	runtime.LockOSThread()
	// Serialize the setup of concurrent runs on the same root (they would
	// race on the --cp copies and on the creation of mount points and
	// devices). The lock is released at the exec.
	unlock, err := osutil.LockRoot(ctx, rootDir, func() {
		log.Infof("waiting for another droot working on %s", rootDir)
	})
	if err != nil {
		return err
	}
	defer unlock()
	readOnly := cmd.Bool("read-only")
	if cmd.Bool("unshare") || readOnly {
		if err := osutil.UnshareMountNamespace(); err != nil {
			return err
		}
		if readOnly {
			if err := mounter.BindRootOnItself(rootDir); err != nil {
				return err
			}
		}
		// The descriptor of the root refers to a mount of the host's
		// namespace, where this thread can no longer mount.
		reopened, err := osutil.OpenRoot(rootDir)
		if err != nil {
			return err
		}
		_ = root.Close()
		root = reopened
	}
	if cmd.Bool("unshare-ipc") {
		if err := osutil.UnshareIPCNamespace(); err != nil {
			return err
		}
	}
	if cmd.IsSet("hostname") {
		if err := osutil.SetHostname(cmd.String("hostname")); err != nil {
			return err
		}
	}

	userSpec, groupSpec := cmd.String("user"), cmd.String("group")
	imageUser := !cmd.IsSet("user") && cfg != nil && cfg.User != ""
	if imageUser {
		// USER is NAME, UID, NAME:GROUP or UID:GID.
		var group string
		userSpec, group, _ = strings.Cut(cfg.User, ":")
		if !cmd.IsSet("group") {
			groupSpec = group
		}
		log.Debugf("running as the image's USER %s", cfg.User)
	}
	if !cmd.IsSet("workdir") && cfg != nil {
		workdir = cfg.WorkingDir
	}
	if workdir == "" {
		workdir = "/"
	}
	baseEnv, err := containerEnv(root, cfg)
	if err != nil {
		return err
	}
	env, err := environ.Merge(baseEnv, extraEnv)
	if err != nil {
		return err
	}

	// Abort between the setup steps if droot is interrupted: the signal
	// is caught to allow cleanups, so it would otherwise go unnoticed and
	// COMMAND would be started anyway.
	if err := ctx.Err(); err != nil {
		return err
	}
	if cmd.Bool("copy-files") {
		files := hostFiles
		if bindNetFiles {
			files = slices.DeleteFunc(slices.Clone(files), func(f string) bool { return slices.Contains(netFiles, f) })
		}
		if err := copyHostFiles(root, files); err != nil {
			return err
		}
	}

	cred, err := identity.Resolve(root, userSpec, groupSpec)
	if err != nil {
		if imageUser {
			return fmt.Errorf("the image's USER %s: %w (use --user to run as another user; with --cp, the container's /etc/passwd is the host's)", cfg.User, err)
		}
		return err
	}
	log.Debugf("running as uid=%d gid=%d groups=%v", cred.UID, cred.GID, cred.Groups)
	env = defaultEnv(env, cred)
	if cmd.IsSet("hostname") {
		env = environ.SetDefault(env, "HOSTNAME", cmd.String("hostname"))
	}

	mnt := mounter.NewMounter(root)
	// If anything fails before COMMAND is executed, remove the mounts made
	// so far (mounts of a previous run that were reused are kept). Once
	// droot has switched to an unprivileged user, it no longer can.
	released := 0
	defer func() {
		if err == nil {
			return
		}
		if released > 0 {
			log.Warnf("%d mount(s) made for COMMAND are left in place: remove them with 'droot umount --root %s'", released, rootDir)
		} else if rbErr := mnt.Rollback(); rbErr != nil {
			log.Warnf("could not remove the mounts made before the failure (use 'droot umount'): %v", rbErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := mnt.MountSysProc(); err != nil {
		return err
	}
	if !cmd.Bool("no-mask") {
		if err := mnt.ProtectKernelFS(); err != nil {
			return err
		}
	}
	// Bind and tmpfs mounts, parents before children (like Docker), so
	// that `--tmpfs /data --bind H:/data/x` binds H inside the tmpfs.
	var mounts []userMount
	for _, b := range binds {
		mounts = append(mounts, userMount{target: b.container, mount: func() error {
			if err := mnt.BindMount(b.host, b.container, b.opts); err != nil {
				return fmt.Errorf("bind mount %s on %s: %w", b.host, b.container, err)
			}
			return nil
		}})
	}
	for _, t := range tmpfs {
		mounts = append(mounts, userMount{target: t.path, mount: func() error {
			if err := mnt.MountFS("tmpfs", t.path, t.opts, nil); err != nil {
				return fmt.Errorf("mount a tmpfs on %s: %w", t.path, err)
			}
			return nil
		}})
	}
	slices.SortStableFunc(mounts, func(a, b userMount) int {
		return strings.Count(a.target, "/") - strings.Count(b.target, "/")
	})
	for _, m := range mounts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.mount(); err != nil {
			return err
		}
	}
	if bindNetFiles {
		for _, f := range netFiles {
			// Replace the mount of a previous run: the host may have
			// replaced the file since (e.g. with rename(2)).
			if err := mnt.BindMount(f, f, mounter.BindOptions{ReadOnly: true, Replace: true}); err != nil {
				return fmt.Errorf("bind mount %s: %w", f, err)
			}
		}
	}

	linkVarLock(root)

	shm := !slices.ContainsFunc(tmpfs, func(t tmpfsMount) bool { return t.path == "/dev/shm" })
	if err := setupDev(root, mnt, shm); err != nil {
		return err
	}
	if readOnly {
		if err := mnt.RemountRootReadonly(); err != nil {
			return err
		}
	}

	return execInRoot(ctx, rootDir, argv, env, cred, execOptions{
		beforeSwitch:    func() { released = mnt.Release() },
		workdir:         workdir,
		dropCaps:        !cmd.Bool("no-dropcaps"),
		keepCaps:        keep,
		rlimits:         rlimits,
		noNewPrivileges: !cmd.IsSet("no-new-privileges") || cmd.Bool("no-new-privileges"),
		seccomp:         cmd.String("seccomp") == "default",
	})
}

// bindOptions are the options accepted after SRC:DEST.
var bindOptions = []string{"ro", "rw", "rbind"}

// parseBindOption parses SRC[:DEST][:OPTS]. DEST defaults to SRC; both must
// be absolute paths. OPTS is a comma-separated list of bindOptions. The last
// part is only taken as OPTS if every item is a known option, so that a DEST
// containing ':' (accepted by droot <= 0.7) keeps working, e.g. /a:/b:c.
func parseBindOption(spec string, readonly bool) (bindMount, error) {
	host, container, _ := strings.Cut(spec, ":")
	var opts string
	if i := strings.LastIndex(container, ":"); i >= 0 && isBindOptions(container[i+1:]) {
		container, opts = container[:i], container[i+1:]
	} else if isBindOptions(container) {
		container, opts = "", container // SRC:OPTS
	}
	if container == "" {
		container = host
	}
	if !filepath.IsAbs(host) {
		return bindMount{}, fmt.Errorf("invalid bind mount %q: %s is not an absolute path", spec, host)
	}
	if !filepath.IsAbs(container) {
		return bindMount{}, fmt.Errorf("invalid bind mount %q: %s is not an absolute path", spec, container)
	}
	b := bindMount{host: filepath.Clean(host), container: filepath.Clean(container), opts: mounter.BindOptions{ReadOnly: readonly}}
	var ro, rw bool
	for o := range strings.SplitSeq(opts, ",") {
		switch o {
		case "ro":
			ro = true
		case "rw":
			rw = true
		case "rbind":
			b.opts.Recursive = true
		}
	}
	switch {
	case ro && rw:
		return bindMount{}, fmt.Errorf("invalid bind mount %q: both ro and rw", spec)
	case rw && readonly:
		return bindMount{}, fmt.Errorf("invalid bind mount %q: --robind is read-only", spec)
	case ro:
		b.opts.ReadOnly = true
	}
	return b, nil
}

func isBindOptions(s string) bool {
	if s == "" {
		return false
	}
	for o := range strings.SplitSeq(s, ",") {
		if !slices.Contains(bindOptions, o) {
			return false
		}
	}
	return true
}

func copyHostFiles(root *osutil.Root, files []string) error {
	for _, f := range files {
		// Never write through a symlink of the image into /proc or /sys.
		if err := root.CopyFileIn(f, f, mounter.NotKernelFS); err != nil {
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
	if err := root.SymlinkIfMissing("../run/lock", "/var/lock", mounter.NotKernelFS); err != nil {
		log.Debugf("cannot create /var/lock: %v", err)
	}
}

// setupDev populates /dev: the device nodes (owned by root, mode 0666), the
// usual symlinks, a devpts instance on /dev/pts and, if shm is set, a tmpfs
// on /dev/shm. If /dev is not on the root's own file system (for instance a
// bind mount of the host's /dev), nothing is done there: droot must not
// touch host devices.
func setupDev(root *osutil.Root, mnt *mounter.Mounter, shm bool) error {
	onRoot := mounter.OnMountOf(root.Fd())
	for _, d := range devices {
		err := root.MknodChar(d.path, d.major, d.minor, 0o666, onRoot)
		if errors.Is(err, mounter.ErrOtherMount) {
			log.Warnf("not populating /dev: /dev in the container is a mount point (e.g. a bind mount of the host's /dev)")
			return nil
		}
		if err != nil {
			return err
		}
	}
	// The following are conveniences: failures are not fatal.
	for _, l := range devLinks {
		if err := root.EnsureSymlink(l.target, l.path, onRoot); err != nil {
			log.Warnf("cannot create %s: %v", l.path, err)
		}
	}
	if err := mnt.MountFS("devpts", "/dev/pts", devpts, onRoot); err != nil {
		log.Warnf("cannot mount devpts on /dev/pts (pseudo-terminals will not work): %v", err)
	}
	if shm {
		if err := mnt.MountFS("tmpfs", "/dev/shm", shmfs, onRoot); err != nil {
			log.Warnf("cannot mount a tmpfs on /dev/shm: %v", err)
		}
	}
	return nil
}

// containerEnv returns the environment of the image: the Env of
// /.droot/config.json, overridden by /.drootenv (which older versions of
// droot wrote alone, and which users may have edited).
func containerEnv(root *osutil.Root, cfg *imageconfig.Config) ([]string, error) {
	var env []string
	if cfg != nil {
		env = cfg.Env
	}
	f, err := root.OpenFile(environ.EnvFilePath)
	if errors.Is(err, fs.ErrNotExist) {
		return environ.Merge(env, nil)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fileEnv, err := environ.ParseEnv(f)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", f.Name(), err)
	}
	return environ.Merge(env, fileEnv)
}

// defaultEnv sets the variables that a login would set, unless they are
// already set: PATH, and HOME, USER and LOGNAME from the container's
// /etc/passwd entry of the user (HOME=/ if the user has none, like Docker).
func defaultEnv(env []string, cred *identity.Credential) []string {
	set := func(k, v string) {
		if _, ok := environ.Lookup(env, k); !ok {
			env = append(env, k+"="+v)
		}
	}
	set("PATH", osutil.DefaultPath)
	home := cred.Home
	if home == "" {
		home = "/"
	}
	set("HOME", home)
	if cred.Username != "" {
		set("USER", cred.Username)
		set("LOGNAME", cred.Username)
	}
	return env
}

type execOptions struct {
	// beforeSwitch is called right before switching to an unprivileged
	// user, after which droot cannot unmount anything.
	beforeSwitch    func()
	workdir         string
	dropCaps        bool
	keepCaps        map[uintptr]bool
	rlimits         []osutil.Rlimit
	noNewPrivileges bool
	seccomp         bool
}

// execInRoot chroots into rootDir, drops privileges and executes argv. It only
// returns on error.
//
// The caller must have locked the goroutine to its OS thread: the capability
// bounding set is a per-thread attribute and execve(2) uses the calling
// thread's.
func execInRoot(ctx context.Context, rootDir string, argv, env []string, cred *identity.Credential, opts execOptions) error {
	if err := osutil.Chroot(rootDir); err != nil {
		return err
	}
	if err := os.Chdir(opts.workdir); err != nil {
		return fmt.Errorf("working directory: %w", err)
	}
	// Fail on a COMMAND that is missing, or that the user cannot execute,
	// while droot is still privileged, so that the mounts it made can be
	// removed. It is looked up again below with the credentials of
	// COMMAND.
	pathEnv, _ := environ.Lookup(env, "PATH")
	canExec := osutil.IsExecutable
	if cred.UID != 0 {
		groups := cred.Groups
		if groups == nil {
			groups, _ = os.Getgroups()
		}
		canExec = func(p string) error { return osutil.CanExecute(p, cred.UID, cred.GID, groups) }
	}
	if _, err := osutil.LookPath(argv[0], pathEnv, canExec); err != nil {
		return err
	}
	// Before dropping CAP_SYS_RESOURCE, which raising a hard limit needs.
	if err := osutil.SetRlimits(opts.rlimits); err != nil {
		return err
	}

	if opts.dropCaps {
		if err := osutil.DropBoundingCapabilities(opts.keepCaps); err != nil {
			return fmt.Errorf("drop capabilities: %w", err)
		}
		if err := osutil.ClearInheritableCapabilities(); err != nil {
			return fmt.Errorf("drop capabilities: %w", err)
		}
	}
	// While droot still has CAP_SYS_ADMIN, which installing a filter
	// without no_new_privs needs. The filter denies none of the system
	// calls droot makes until the exec.
	if opts.seccomp {
		if err := seccomp.Load(); err != nil {
			return err
		}
	}
	if cred.UID != 0 && opts.beforeSwitch != nil {
		opts.beforeSwitch()
	}
	if err := osutil.SetCredentials(cred.UID, cred.GID, cred.Groups); err != nil {
		return err
	}
	if opts.noNewPrivileges {
		if err := osutil.SetNoNewPrivs(); err != nil {
			return err
		}
	}

	// Resolve the command with the container's PATH and the new
	// credentials, not with the PATH of the (sudo) environment droot was
	// started from.
	argv0, err := osutil.LookPath(argv[0], pathEnv, osutil.IsExecutable)
	if err != nil {
		return err
	}

	// From now on, let termination signals act normally (on droot until
	// the exec, then on COMMAND), and check one last time whether one was
	// caught during the setup.
	signal.Reset(os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	if err := ctx.Err(); err != nil {
		return err
	}
	return osutil.Exec(argv0, argv, env)
}
