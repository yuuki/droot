package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	"github.com/yuuki/droot/internal/conffile"
	"github.com/yuuki/droot/internal/environ"
	"github.com/yuuki/droot/internal/identity"
	"github.com/yuuki/droot/internal/imageconfig"
	"github.com/yuuki/droot/internal/log"
	"github.com/yuuki/droot/internal/mounter"
	"github.com/yuuki/droot/internal/osutil"
	"github.com/yuuki/droot/internal/seccomp"
	"github.com/yuuki/droot/internal/systemd"
	"github.com/yuuki/droot/internal/userns"
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
			"with 'droot umount'.\n\n" +
			"Run by another user than root, droot runs rootless: it runs again as root of\n" +
			"a new user namespace, where your user is root and your subordinate IDs\n" +
			"(/etc/subuid and /etc/subgid, with the newuidmap and newgidmap commands) are\n" +
			"the other users, and waits for COMMAND, passing signals on to it. The mounts\n" +
			"are private (as with --unshare), /proc and the device files are bind mounts of\n" +
			"the host's. Extract archives with 'droot unshare' so that their files keep\n" +
			"their owners.\n\n" +
			"Under systemd, COMMAND gets the notification socket (NOTIFY_SOCKET, for\n" +
			"Type=notify), the sockets of socket activation (LISTEN_FDS, LISTEN_PID,\n" +
			"LISTEN_FDNAMES) and the watchdog (WATCHDOG_USEC, WATCHDOG_PID) of droot,\n" +
			"which --env and --env-file override and --no-systemd leaves out. A\n" +
			"NOTIFY_SOCKET that is a path is bind mounted read-only on the same path in\n" +
			"the container; its mount point is only created on ROOT_DIR's own file system\n" +
			"or in a --tmpfs. Rootless, droot stays the main process of the service and\n" +
			"relays the notifications of COMMAND, but not those of its children, from a\n" +
			"socket of its own bound there instead.\n\n" +
			"--config FILE reads options from FILE, in a subset of TOML: lines KEY = VALUE,\n" +
			"where KEY is the long name of an option and VALUE a string (\"...\" or '...'),\n" +
			"true or false, or an array of strings for repeatable options; command, an\n" +
			"array of strings, gives COMMAND. # starts a comment. The options given on the\n" +
			"command line take precedence, and repeatable ones are added to the file's:\n\n" +
			"   root = \"/var/containers/app/current\"\n" +
			"   unshare = true\n" +
			"   bind = [\"/var/log/app\", \"/srv/data:/data:ro\"]\n" +
			"   command = [\"/app/bin/server\", \"--port=8080\"]",
		StopAtFirstArg: true, // everything from COMMAND on belongs to COMMAND
		ArgsComplete:   "command",
		Flags: []*cli.Flag{
			{Name: "config", Kind: cli.String, Placeholder: "FILE", Complete: "file",
				Usage: "read options from FILE: the long names of the options of droot run as keys, and command, in a subset of TOML (see droot(1)); the command line takes precedence"},
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
				Usage: "make the mounts in a private mount namespace: they disappear when COMMAND and its children exit, and need no 'droot umount' (always the case rootless)"},
			{Name: "unshare-ipc", Kind: cli.Bool,
				Usage: "run COMMAND in a private IPC namespace (System V IPC, POSIX message queues)"},
			{Name: "hostname", Kind: cli.String, Placeholder: "NAME",
				Usage: "run COMMAND in a private UTS namespace with the host name NAME"},
			{Name: "read-only", Kind: cli.Bool,
				Usage: "make the root directory read-only for COMMAND (implies --unshare); use --tmpfs or --bind for the paths it writes"},
			{Name: "no-mask", Kind: cli.Bool,
				Usage: "do not mask the sensitive files of /proc and /sys nor make /proc/sys, /sys and the like read-only (dangerous)"},
			{Name: "no-systemd", Kind: cli.Bool,
				Usage: "do not pass systemd's notification socket, socket activation and watchdog variables (NOTIFY_SOCKET, LISTEN_*, WATCHDOG_*) on to COMMAND, nor bind the notification socket in the container; inherited descriptors are kept"},
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

// ttyGID is the group of the pseudo-terminals, tty.
const ttyGID = 5

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

func doRun(ctx context.Context, cmd *cli.Invocation) error {
	if err := setup(cmd); err != nil {
		return err
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("droot run is only supported on Linux (running on %s)", runtime.GOOS)
	}
	// Rootless, droot runs again as root of a user namespace (see
	// runRootless), after the checks it can make without reading the
	// files of --config and --env-file, which may be pipes: only the
	// child must read them. With --config, which may give any option,
	// the child makes every check.
	rootless := os.Geteuid() != 0
	if rootless && cmd.IsSet("config") {
		return runRootless(ctx)
	}
	opts, err := parseRunOptions(cmd)
	if err != nil {
		return err
	}

	// Every path inside the container is resolved through this descriptor
	// so that a process of a previous run cannot redirect droot to the host
	// file system by swapping directories for symlinks.
	root, err := osutil.OpenRoot(opts.rootDir)
	if err != nil {
		return err
	}
	r := &runner{cmd: cmd, opts: opts, root: root}
	defer func() { _ = r.root.Close() }() // r.root may be opened again (see unshareMounts)

	// The image configuration saved by droot export, if any, gives the
	// defaults of COMMAND, --user and --workdir, and the environment.
	if r.cfg, err = imageconfig.Read(root); err != nil {
		return err
	}
	argv := r.cfg.Command(opts.args)
	if len(argv) == 0 || argv[0] == "" {
		if r.cfg != nil {
			return cmd.UsageError("a command to run is required: the image defines neither ENTRYPOINT nor CMD")
		}
		return cmd.UsageError("a command to run is required")
	}
	if rootless {
		return runRootless(ctx)
	}
	return r.run(ctx, argv)
}

// runOptions are the options of droot run that are parsed before the setup.
type runOptions struct {
	args     []string // COMMAND, from the command line or the --config file
	rootDir  string   // --root, resolved
	binds    []bindMount
	tmpfs    []tmpfsMount
	keepCaps map[uintptr]bool // the bounding set (see boundingSet)
	rlimits  []osutil.Rlimit
}

// parseRunOptions applies the --config file, checks the options of droot
// run and parses those that need it.
func parseRunOptions(cmd *cli.Invocation) (*runOptions, error) {
	args, err := commandArgs(cmd)
	if err != nil {
		return nil, err
	}
	if err := checkRunFlags(cmd); err != nil {
		return nil, err
	}
	opts := &runOptions{args: args}
	if opts.rootDir, err = mounter.ResolveRootDir(cmd.String("root")); err != nil {
		return nil, err
	}
	if opts.binds, err = parseBinds(cmd.Strings("bind"), false); err != nil {
		return nil, err
	}
	robinds, err := parseBinds(cmd.Strings("robind"), true)
	if err != nil {
		return nil, err
	}
	opts.binds = append(opts.binds, robinds...)
	if cmd.Bool("no-dropcaps") && (cmd.IsSet("cap-add") || cmd.IsSet("cap-drop")) {
		return nil, cmd.UsageError("--cap-add and --cap-drop cannot be used with --no-dropcaps")
	}
	if opts.keepCaps, err = boundingSet(cmd.Strings("cap-add"), cmd.Strings("cap-drop")); err != nil {
		return nil, err
	}
	if opts.rlimits, err = parseEach(cmd.Strings("ulimit"), osutil.ParseRlimit); err != nil {
		return nil, err
	}
	if opts.tmpfs, err = parseEach(cmd.Strings("tmpfs"), parseTmpfsOption); err != nil {
		return nil, err
	}
	return opts, nil
}

// commandArgs applies the --config file, if any, and returns COMMAND as
// given: the arguments, or else the command of the file.
func commandArgs(cmd *cli.Invocation) ([]string, error) {
	args := cmd.Args()
	if cmd.IsSet("config") {
		command, err := applyConfigFile(cmd, cmd.String("config"))
		if err != nil {
			return nil, err
		}
		if len(args) == 0 {
			args = command
		}
	}
	if len(args) > 0 && args[0] == "" {
		return nil, cmd.UsageError("COMMAND must not be empty")
	}
	return args, nil
}

// checkRunFlags checks the options of droot run that are used as they are.
func checkRunFlags(cmd *cli.Invocation) error {
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
	if workdir := cmd.String("workdir"); cmd.IsSet("workdir") && !filepath.IsAbs(workdir) {
		return cmd.UsageError("--workdir must be an absolute path: %q", workdir)
	}
	return nil
}

// parseBinds parses the specs of --bind, or of --robind if readonly.
func parseBinds(specs []string, readonly bool) ([]bindMount, error) {
	return parseEach(specs, func(spec string) (bindMount, error) { return parseBindOption(spec, readonly) })
}

// parseEach parses every spec with parse, stopping at the first error.
func parseEach[T any](specs []string, parse func(string) (T, error)) ([]T, error) {
	var values []T
	for _, spec := range specs {
		v, err := parse(spec)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, nil
}

// runner sets up the container of droot run, as root, and executes COMMAND
// in it.
type runner struct {
	cmd  *cli.Invocation
	opts *runOptions
	// root is the root directory of the container, opened again in a
	// private mount namespace.
	root *osutil.Root
	cfg  *imageconfig.Config // nil without image configuration
	// rootless is set in a user namespace: rootless, or in a container.
	rootless bool
	// privateNS is set when the mounts are made in a private mount
	// namespace.
	privateNS bool
}

// run sets up the container and executes argv in it. It only returns on
// error.
func (r *runner) run(ctx context.Context, argv []string) error {
	extraEnv, err := commandLineEnv(r.cmd)
	if err != nil {
		return err
	}

	// Root of a user namespace: rootless, or in a container.
	r.rootless = osutil.InUserNamespace()

	// From now on and up to the exec, everything happens on this OS
	// thread: the capability bounding set is a per-thread attribute, and
	// so are the namespaces of --unshare. The thread is never unlocked;
	// on error the process exits.
	runtime.LockOSThread()
	// Serialize the setup of concurrent runs on the same root (they would
	// race on the --cp copies and on the creation of mount points and
	// devices). The lock is released at the exec.
	unlock, err := lockRoot(ctx, r.opts.rootDir)
	if err != nil {
		return err
	}
	defer unlock()
	// In a user namespace, only the mounts of a mount namespace it owns
	// can be changed.
	r.privateNS = r.cmd.Bool("unshare") || r.cmd.Bool("read-only") || r.rootless
	if err := r.unshare(); err != nil {
		return err
	}

	user := r.user()
	env, err := r.environment(extraEnv)
	if err != nil {
		return err
	}

	// Abort between the setup steps if droot is interrupted: the signal
	// is caught to allow cleanups, so it would otherwise go unnoticed and
	// COMMAND would be started anyway.
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.cmd.Bool("copy-files") {
		files := hostFiles
		if r.cmd.Bool("bind-net-files") {
			files = slices.DeleteFunc(slices.Clone(files), func(f string) bool { return slices.Contains(netFiles, f) })
		}
		if err := copyHostFiles(r.root, files); err != nil {
			return err
		}
	}

	cred, err := r.credential(user)
	if err != nil {
		return err
	}
	env = defaultEnv(env, cred)
	if r.cmd.IsSet("hostname") {
		env = environ.SetDefault(env, "HOSTNAME", r.cmd.String("hostname"))
	}
	return r.mountAndExec(ctx, argv, env, cred)
}

// commandLineEnv returns the environment given on the command line, in
// increasing order of precedence: --env-file, then --env. Files and values
// are read on the host, before the chroot.
func commandLineEnv(cmd *cli.Invocation) ([]string, error) {
	var extraEnv []string
	for _, file := range cmd.Strings("env-file") {
		env, err := environ.ReadEnvFileFrom(file, os.LookupEnv)
		if err != nil {
			return nil, fmt.Errorf("--env-file: %w", err)
		}
		extraEnv = append(extraEnv, env...)
	}
	cliEnv, err := environ.ExpandBare(cmd.Strings("env"), os.LookupEnv)
	if err != nil {
		return nil, err
	}
	extraEnv = append(extraEnv, cliEnv...)
	if err := environ.Validate(extraEnv); err != nil {
		return nil, err
	}
	return extraEnv, nil
}

// lockRoot locks rootDir for the setup of a run, waiting for the other runs
// that hold the lock, and returns the function that releases it.
func lockRoot(ctx context.Context, rootDir string) (unlock func(), err error) {
	waiting := func() { log.Infof("waiting for another droot working on %s", rootDir) }
	if userns.Supervised() {
		return osutil.LockRootUser(ctx, os.Getenv("XDG_RUNTIME_DIR"), rootDir, waiting)
	}
	return osutil.LockRoot(ctx, rootDir, waiting)
}

// unshare makes the private namespaces of the run: the mount namespace if
// privateNS is set, the IPC namespace of --unshare-ipc and the UTS namespace
// of --hostname.
func (r *runner) unshare() error {
	if r.privateNS {
		if err := r.unshareMounts(); err != nil {
			return err
		}
	}
	if r.cmd.Bool("unshare-ipc") {
		if err := osutil.UnshareIPCNamespace(); err != nil {
			return err
		}
	}
	if r.cmd.IsSet("hostname") {
		if err := osutil.SetHostname(r.cmd.String("hostname")); err != nil {
			return err
		}
	}
	return nil
}

// unshareMounts makes a private mount namespace, binds the root on itself
// there for --read-only, and opens the root again in it.
func (r *runner) unshareMounts() error {
	if err := osutil.UnshareMountNamespace(); err != nil {
		if r.rootless && errors.Is(err, syscall.EPERM) {
			err = fmt.Errorf("%w (in a user namespace without privileges: on Ubuntu, see the sysctl kernel.apparmor_restrict_unprivileged_userns)", err)
		}
		return err
	}
	if r.cmd.Bool("read-only") {
		if err := mounter.BindRootOnItself(r.opts.rootDir); err != nil {
			return err
		}
	}
	// The descriptor of the root refers to a mount of the host's
	// namespace, where this thread can no longer mount.
	reopened, err := osutil.OpenRoot(r.opts.rootDir)
	if err != nil {
		return err
	}
	_ = r.root.Close()
	r.root = reopened
	return nil
}

// userSpec is the user and the group to run COMMAND as, as given.
type userSpec struct {
	user, group string
	fromImage   bool // user is the image's USER
}

// user returns the user and the group of --user and --group, the image's
// USER giving those that are not set.
func (r *runner) user() userSpec {
	spec := userSpec{user: r.cmd.String("user"), group: r.cmd.String("group")}
	if r.cmd.IsSet("user") || r.cfg == nil || r.cfg.User == "" {
		return spec
	}
	// USER is NAME, UID, NAME:GROUP or UID:GID.
	var group string
	spec.user, group, _ = strings.Cut(r.cfg.User, ":")
	spec.fromImage = true
	if !r.cmd.IsSet("group") {
		spec.group = group
	}
	log.Debugf("running as the image's USER %s", r.cfg.User)
	return spec
}

// workdir returns the working directory of COMMAND: --workdir, or else the
// image's WORKDIR, or else /.
func (r *runner) workdir() string {
	workdir := r.cmd.String("workdir")
	if !r.cmd.IsSet("workdir") && r.cfg != nil {
		workdir = r.cfg.WorkingDir
	}
	if workdir == "" {
		workdir = "/"
	}
	return workdir
}

// environment returns the environment of COMMAND, without the defaults of
// defaultEnv: that of the image, then the variables of systemd, then
// extraEnv, the environment of the command line.
func (r *runner) environment(extraEnv []string) ([]string, error) {
	baseEnv, err := containerEnv(r.root, r.cfg)
	if err != nil {
		return nil, err
	}
	// The variables systemd sets for droot (NOTIFY_SOCKET, LISTEN_FDS,
	// ...), which describe the notification socket, the sockets and the
	// watchdog COMMAND inherits, between the image's and those of the
	// command line, unless --no-systemd.
	var sdEnv []string
	if !r.cmd.Bool("no-systemd") {
		sdEnv = systemd.Env(os.LookupEnv, os.Getpid())
	}
	return environ.Merge(baseEnv, append(sdEnv, extraEnv...))
}

// credential resolves the user and the groups of spec in the container and
// checks that they can be switched to.
func (r *runner) credential(spec userSpec) (*identity.Credential, error) {
	cred, err := identity.Resolve(r.root, spec.user, spec.group)
	if err != nil {
		if spec.fromImage {
			return nil, fmt.Errorf("the image's USER %s: %w (use --user to run as another user; with --cp, the container's /etc/passwd is the host's)", r.cfg.User, err)
		}
		return nil, err
	}
	if r.rootless {
		if err := checkMapped(cred); err != nil {
			return nil, err
		}
	}
	log.Debugf("running as uid=%d gid=%d groups=%v", cred.UID, cred.GID, cred.Groups)
	return cred, nil
}

// mountAndExec makes the mounts of the container and executes argv in it.
// It only returns on error.
func (r *runner) mountAndExec(ctx context.Context, argv, env []string, cred *identity.Credential) (err error) {
	mnt := mounter.NewMounter(r.root)
	// If anything fails before COMMAND is executed, remove the mounts made
	// so far (mounts of a previous run that were reused are kept). Once
	// droot has switched to an unprivileged user, it no longer can.
	released := 0
	defer func() {
		if err == nil {
			return
		}
		if released > 0 && !r.privateNS {
			log.Warnf("%d mount(s) made for COMMAND are left in place: remove them with 'droot umount --root %s'", released, r.opts.rootDir)
		} else if rbErr := mnt.Rollback(); rbErr != nil {
			log.Warnf("could not remove the mounts made before the failure (use 'droot umount'): %v", rbErr)
		}
	}()
	if err := r.mount(ctx, mnt, env); err != nil {
		return err
	}

	return execInRoot(ctx, r.opts.rootDir, argv, env, cred, execOptions{
		beforeSwitch:    func() { released = mnt.Release() },
		workdir:         r.workdir(),
		dropCaps:        !r.cmd.Bool("no-dropcaps"),
		keepCaps:        r.opts.keepCaps,
		rlimits:         r.opts.rlimits,
		noNewPrivileges: !r.cmd.IsSet("no-new-privileges") || r.cmd.Bool("no-new-privileges"),
		seccomp:         r.cmd.String("seccomp") == "default",
	})
}

// mount makes the mounts of the container with mnt: /proc and /sys, those
// of the command line and that of systemd's notification socket, populates
// /dev, and makes the root read-only for --read-only. env is the
// environment of COMMAND.
func (r *runner) mount(ctx context.Context, mnt *mounter.Mounter, env []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := mnt.MountSysProc(); err != nil {
		return err
	}
	if !r.cmd.Bool("no-mask") {
		if err := mnt.ProtectKernelFS(); err != nil {
			return err
		}
	}
	// The mounts of --tmpfs, where droot creates the mount point of the
	// notification socket if needed, as on the root's own file system.
	var tmpfs []*os.File
	defer func() {
		for _, f := range tmpfs {
			_ = f.Close()
		}
	}()
	mounts := r.userMounts(mnt, &tmpfs)
	for _, m := range mounts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.mount(); err != nil {
			return err
		}
	}
	if r.cmd.Bool("bind-net-files") {
		if err := bindNetFiles(mnt); err != nil {
			return err
		}
	}
	if err := r.bindNotifySocket(mnt, env, mounts, tmpfs); err != nil {
		return err
	}

	linkVarLock(r.root)

	shm := !slices.ContainsFunc(r.opts.tmpfs, func(t tmpfsMount) bool { return t.path == "/dev/shm" })
	if err := setupDev(r.root, mnt, shm, r.rootless); err != nil {
		return err
	}
	if r.cmd.Bool("read-only") {
		if err := mnt.RemountRootReadonly(); err != nil {
			return err
		}
	}
	return nil
}

// bindNotifySocket bind mounts the socket of systemd's notifications of
// COMMAND (env) in the container, if any (see notifySocketMount), unless
// --no-systemd is given or one of mounts, those of the command line, mounts
// on its path. Its mount point may be created on the root's own file system
// and in tmpfs, the mounts of --tmpfs. Rootless, the relay of the
// supervising droot takes the place of systemd's socket.
func (r *runner) bindNotifySocket(mnt *mounter.Mounter, env []string, mounts []userMount, tmpfs []*os.File) error {
	if r.cmd.Bool("no-systemd") {
		return nil
	}
	sock := notifySocketMount(env)
	if sock == "" {
		return nil
	}
	if slices.ContainsFunc(mounts, func(m userMount) bool { return m.target == sock }) {
		log.Debugf("not binding %s=%s: --bind or --tmpfs mounts on it", systemd.NotifySocket, sock)
		return nil
	}
	fds := []int{r.root.Fd()}
	for _, f := range tmpfs {
		fds = append(fds, int(f.Fd()))
	}
	b := notifyBind{sock: sock, source: sock, within: mounter.OnMountOf(fds...), hostNS: !r.privateNS}
	if relay := userns.NotifyRelay(); relay != "" {
		// Rootless: the supervising droot, the main process of the
		// service, relays the notifications of COMMAND.
		b.source = relay
	}
	return bindNotifySocket(r.root, mnt, b)
}

// userMounts returns the bind and tmpfs mounts of the command line, to be
// made with mnt, parents before children (like Docker), so that
// `--tmpfs /data --bind H:/data/x` binds H inside the tmpfs. The mounts of
// --tmpfs add a descriptor of theirs to tmpfs.
func (r *runner) userMounts(mnt *mounter.Mounter, tmpfs *[]*os.File) []userMount {
	var mounts []userMount
	for _, b := range r.opts.binds {
		mounts = append(mounts, userMount{target: b.container, mount: func() error {
			if err := mnt.BindMount(b.host, b.container, b.opts); err != nil {
				return fmt.Errorf("bind mount %s on %s: %w", b.host, b.container, err)
			}
			return nil
		}})
	}
	for _, t := range r.opts.tmpfs {
		mounts = append(mounts, userMount{target: t.path, mount: func() error {
			f, err := mnt.MountFS("tmpfs", t.path, t.opts, nil)
			if err != nil {
				return fmt.Errorf("mount a tmpfs on %s: %w", t.path, err)
			}
			*tmpfs = append(*tmpfs, f)
			return nil
		}})
	}
	slices.SortStableFunc(mounts, func(a, b userMount) int {
		return strings.Count(a.target, "/") - strings.Count(b.target, "/")
	})
	return mounts
}

// bindNetFiles bind mounts the host's netFiles read-only for
// --bind-net-files.
func bindNetFiles(mnt *mounter.Mounter) error {
	for _, f := range netFiles {
		// Replace the mount of a previous run: the host may have
		// replaced the file since (e.g. with rename(2)).
		if err := mnt.BindMount(f, f, mounter.BindOptions{ReadOnly: true, Replace: true}); err != nil {
			return fmt.Errorf("bind mount %s: %w", f, err)
		}
	}
	return nil
}

// runRootless runs droot again as root of a new user namespace, mapping the
// user to root and their subordinate IDs, if any, to the other users (see
// package userns), and waits for it, relaying the notifications of COMMAND
// to systemd.
func runRootless(ctx context.Context) error {
	m, err := userns.CurrentMapping()
	if err != nil {
		return err
	}
	return userns.Run(ctx, m, userns.Options{RelayNotifications: true})
}

// checkMapped checks, in a user namespace, that the user and the groups of
// COMMAND exist there: droot could not switch to them. Supplementary groups
// that do not exist are left out.
func checkMapped(cred *identity.Credential) error {
	if !osutil.UIDMapped(cred.UID) || !osutil.GIDMapped(cred.GID) {
		return fmt.Errorf("cannot run as uid %d and gid %d: they do not exist in the user namespace of rootless droot, "+
			"where your user is root and your subordinate IDs (/etc/subuid and /etc/subgid, mapped with "+
			"newuidmap and newgidmap) are the other users; give your user subordinate IDs, or run as root with --user 0",
			cred.UID, cred.GID)
	}
	if cred.Groups == nil {
		// Not the supplementary groups of the user on the host, which
		// do not exist in the namespace (kept only if setgroups(2) is
		// denied).
		cred.Groups = []int{cred.GID}
	}
	if cred.Groups != nil {
		cred.Groups = slices.DeleteFunc(cred.Groups, func(g int) bool {
			if !osutil.GIDMapped(g) {
				log.Debugf("leaving out the group %d, which does not exist in the user namespace", g)
				return true
			}
			return false
		})
	}
	return nil
}

// maxConfigSize bounds the size of a --config file.
const maxConfigSize = 1 << 20

// applyConfigFile applies the options of the --config file to cmd, and
// returns its command, if any.
func applyConfigFile(cmd *cli.Invocation, path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxConfigSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxConfigSize {
		return nil, fmt.Errorf("%s: larger than %d bytes", path, maxConfigSize)
	}
	values, err := conffile.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var command []string
	if v, ok := values["command"]; ok {
		command, ok = v.([]string)
		if !ok || len(command) == 0 {
			return nil, fmt.Errorf("%s: command must be a non-empty array of strings, e.g. [\"/app/bin/server\", \"--port=80\"]", path)
		}
		delete(values, "command")
	}
	if _, ok := values["config"]; ok {
		return nil, fmt.Errorf("%s: a configuration file cannot include another one", path)
	}
	if err := cmd.ApplyConfig(values); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	log.Debugf("read the options of %s", path)
	return command, nil
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

// notifySocketMount returns the path of the socket of systemd's
// notifications (NOTIFY_SOCKET) to bind mount in the container, if any:
// that of droot's own NOTIFY_SOCKET, when it is a path, and when it is also
// COMMAND's (env), not replaced by --env. An abstract socket (@NAME) is
// reachable as it is: droot shares the host's network namespace.
func notifySocketMount(env []string) string {
	sock := systemd.NotifySocketPath(os.Getenv(systemd.NotifySocket))
	if sock == "" {
		return ""
	}
	if v, _ := environ.Lookup(env, systemd.NotifySocket); v != sock {
		return ""
	}
	return filepath.Clean(sock)
}

// notifyBind is the bind mount of the socket of systemd's notifications in
// the container.
type notifyBind struct {
	// sock is NOTIFY_SOCKET, the path of the mount in the container.
	sock string
	// source is what is bound there: sock, or rootless, the socket of the
	// relay of the supervising droot (see userns.NotifyRelay).
	source string
	// within accepts the directories where the mount point may be
	// created (see mounter.BindOptions.Within).
	within osutil.DirCheck
	// hostNS is set when the mount is made in the host's mount namespace.
	hostNS bool
}

// bindNotifySocket bind mounts the socket of systemd's notifications, or the
// relay that takes its place, on the path of NOTIFY_SOCKET in the container,
// unless systemd's socket is already there (in a bind mount of its
// directory). The mount is read-only: COMMAND can send notifications
// (connect(2) and sendmsg(2) need no write access to the mount) but cannot
// chmod or chown the host's socket. A mount left by a previous run is
// replaced, in case systemd created the socket again since.
//
// The user did not ask for this mount, so droot creates its mount point only
// in directories that within accepts: those of the root's own file system
// and of --tmpfs, not those bound from the host, where it would be left
// behind. In the host's mount namespace, droot does not mount on an existing
// file of those either: it would be busy until 'droot umount'.
//
// A socket that cannot be bound is only reported: droot may have inherited
// a NOTIFY_SOCKET that was not meant for it (from a process of a
// Type=notify service), and COMMAND may not send notifications at all. A
// failure of the mount itself is an error.
func bindNotifySocket(root *osutil.Root, mnt *mounter.Mounter, b notifyBind) error {
	if err := notifySocketTarget(root, b); err != nil {
		warnNotifySocket(b.sock, err)
		return nil
	}
	opts := mounter.BindOptions{ReadOnly: true, Replace: true, SkipSame: true, Within: b.within, WithinParent: b.hostNS}
	if err := mnt.BindMount(b.source, b.sock, opts); err != nil {
		if errors.Is(err, mounter.ErrOtherMount) {
			warnNotifySocket(b.sock, err)
			return nil
		}
		return fmt.Errorf("bind mount the socket of %s, %s: %w", systemd.NotifySocket, b.source, err)
	}
	if b.source != b.sock {
		log.Debugf("%s=%s is the relay %s of the supervising droot in the container", systemd.NotifySocket, b.sock, b.source)
	} else {
		log.Debugf("%s=%s is reachable in the container", systemd.NotifySocket, b.sock)
	}
	return nil
}

// warnNotifySocket reports that the socket sock of systemd's notifications
// cannot be bound in the container, because of err.
func warnNotifySocket(sock string, err error) {
	if errors.Is(err, mounter.ErrOtherMount) {
		log.Debugf("%s: %v", systemd.NotifySocket, err)
		err = fmt.Errorf("%s: not binding %s in the container: its path there is on a mount other than ROOT_DIR's own file system "+
			"and --tmpfs (a --bind of a host directory?), where droot makes no mount on its own; --bind %s binds it",
			systemd.NotifySocket, sock, sock)
	}
	log.Warnf("%v: COMMAND cannot send notifications to systemd", err)
}

// notifySocketTarget checks that the source of b is a socket, and creates
// its mount point in the container if needed, in directories b.within
// accepts.
func notifySocketTarget(root *osutil.Root, b notifyBind) error {
	fi, err := os.Stat(b.source)
	if err != nil {
		return fmt.Errorf("%s: %w", systemd.NotifySocket, err)
	}
	if fi.Mode().Type() != fs.ModeSocket {
		return fmt.Errorf("%s: %s is not a socket", systemd.NotifySocket, b.source)
	}
	t, err := root.OpenMountTargetWithin(b.sock, false, mounter.NotKernelFS, b.within)
	if err != nil {
		if errors.Is(err, mounter.ErrOtherMount) {
			return err
		}
		return fmt.Errorf("%s: %w", systemd.NotifySocket, err)
	}
	return t.Close()
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
//
// In a user namespace (userns), where device nodes cannot be created, the
// host's are bind mounted instead.
func setupDev(root *osutil.Root, mnt *mounter.Mounter, shm, userns bool) error {
	onRoot := mounter.OnMountOf(root.Fd())
	for _, d := range devices {
		var err error
		if userns {
			err = mnt.BindDevice(d.path, onRoot)
		} else {
			err = root.MknodChar(d.path, d.major, d.minor, 0o666, onRoot)
		}
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
	pts, err := mnt.MountFS("devpts", "/dev/pts", devpts, onRoot)
	if err != nil && userns && !osutil.GIDMapped(ttyGID) {
		// Without the group tty, as in rootless mode without
		// subordinate IDs.
		opts := devpts
		opts.Data = slices.DeleteFunc(slices.Clone(devpts.Data), func(o string) bool { return strings.HasPrefix(o, "gid=") })
		pts, err = mnt.MountFS("devpts", "/dev/pts", opts, onRoot)
	}
	if err != nil {
		log.Warnf("cannot mount devpts on /dev/pts (pseudo-terminals will not work): %v", err)
	} else {
		_ = pts.Close()
	}
	if shm {
		f, err := mnt.MountFS("tmpfs", "/dev/shm", shmfs, onRoot)
		if err != nil {
			log.Warnf("cannot mount a tmpfs on /dev/shm: %v", err)
		} else {
			_ = f.Close()
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
	// Rootless: the change of credentials cleared it.
	if err := userns.KeepParentDeathSignal(); err != nil {
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
