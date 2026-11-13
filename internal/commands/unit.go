package commands

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/yuuki/droot/internal/cli"
	"github.com/yuuki/droot/internal/environ"
	"github.com/yuuki/droot/internal/identity"
	"github.com/yuuki/droot/internal/imageconfig"
	"github.com/yuuki/droot/internal/log"
	"github.com/yuuki/droot/internal/mounter"
	"github.com/yuuki/droot/internal/osutil"
	"github.com/yuuki/droot/internal/seccomp"
	"github.com/yuuki/droot/internal/systemd"
)

func unitCommand() *cli.Command {
	return &cli.Command{
		Name:      "unit",
		Usage:     "Print the settings of a systemd service that runs COMMAND in ROOT_DIR without droot",
		ArgsUsage: "[--] [COMMAND [ARG...]]",
		Description: "Prints the [Service] settings of a systemd unit that runs COMMAND in ROOT_DIR\n" +
			"with systemd alone (RootDirectory=), as 'droot run' with the same options would:\n" +
			"the ENTRYPOINT, CMD, USER, WORKDIR, ENV and STOPSIGNAL of the image saved by\n" +
			"'droot export', the mounts, the capabilities and the other protections. The\n" +
			"options are those of droot run, and so are the --config files. Install the\n" +
			"settings as a drop-in, e.g. /etc/systemd/system/app.service.d/droot.conf, next\n" +
			"to a unit of your own that sets Type=, Restart=, the limits and [Install], then\n" +
			"run 'systemctl daemon-reload'.\n\n" +
			"The settings are those of one release: ROOT_DIR is resolved (systemd does not\n" +
			"follow a symlink there), and the configuration of the image is read now.\n" +
			"Generate them again whenever ROOT_DIR changes.\n\n" +
			"systemd looks the user and the groups up in the host's databases, where they\n" +
			"must exist with the IDs of the image (droot warns otherwise), and adds the\n" +
			"host's supplementary groups of the user. The supplementary groups of the image\n" +
			"that the host does not have are left out. A bare --env KEY takes its value from\n" +
			"the environment of droot unit.\n\n" +
			"The options that have no counterpart in a unit are refused: --copy-files,\n" +
			"--env-file, --hostname, a --bind on /dev or below, and a --tmpfs on /dev.\n" +
			"--unshare is implied. Values that systemd would read differently, such as\n" +
			"environment variables whose names are not made of letters, digits and\n" +
			"underscores or whose values contain control characters, are errors.",
		StopAtFirstArg: true,
		ArgsComplete:   "command",
		Flags: append(runFlags(), &cli.Flag{Name: "output", Aliases: []string{"o"}, Kind: cli.String, Placeholder: "FILE", Complete: "file",
			Usage: "write the settings to FILE instead of STDOUT (written atomically); not a key of the --config file"}),
		Action: doUnit,
	}
}

// unitRefused are the options of droot run that have no counterpart in a
// unit, and what to do instead.
var unitRefused = []struct{ name, instead string }{
	{"copy-files", "use --bind-net-files, or copy the files into ROOT_DIR when deploying it"},
	{"env-file", "add EnvironmentFile= to your unit (systemd reads the file with its own syntax)"},
	{"hostname", "droot unit does not set the host name of the service"},
}

// hostHasUID and hostHasGID report whether the host's databases have a user
// or a group; tests replace them.
var (
	hostHasUID = identity.HostHasUID
	hostHasGID = identity.HostHasGID
)

func doUnit(ctx context.Context, cmd *cli.Invocation) error {
	if err := setup(cmd); err != nil {
		return err
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("droot unit is only supported on Linux (running on %s)", runtime.GOOS)
	}
	opts, err := parseRunOptions(cmd)
	if err != nil {
		return err
	}
	if err := checkUnitOptions(cmd, opts); err != nil {
		return err
	}
	root, err := osutil.OpenRoot(opts.rootDir)
	if err != nil {
		return err
	}
	defer root.Close()
	r := &runner{cmd: cmd, opts: opts, root: root}
	if r.cfg, err = imageconfig.Read(root); err != nil {
		return err
	}
	argv, err := commandToRun(cmd, r.cfg, opts.args)
	if err != nil {
		return err
	}
	u, err := r.unit(argv)
	if err != nil {
		return err
	}
	text := u.String()
	write := func(w io.Writer) error {
		_, err := io.WriteString(w, text)
		return err
	}
	if output := cmd.String("output"); output != "" && output != "-" {
		return writeFile(ctx, output, write)
	}
	return write(cmd.App.Output())
}

// checkUnitOptions refuses the options of droot run that a unit cannot
// express.
func checkUnitOptions(cmd *cli.Invocation, opts *runOptions) error {
	for _, o := range unitRefused {
		if cmd.IsSet(o.name) && (cmd.String(o.name) != "" || len(cmd.Strings(o.name)) > 0 || cmd.Bool(o.name)) {
			return cmd.UsageError("--%s has no counterpart in a unit: %s", o.name, o.instead)
		}
	}
	under := func(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }
	check := func(opt, p string, bind bool) error {
		switch {
		case p == "/":
			return cmd.UsageError("%s %s: droot never mounts on the root directory itself", opt, p)
		case under(p, "/proc") || under(p, "/sys"):
			return cmd.UsageError("%s %s: droot never mounts inside /proc and /sys", opt, p)
		case p == "/dev" || (bind && under(p, "/dev")):
			return cmd.UsageError("%s %s: the service gets a /dev of its own (PrivateDevices=), which denies the other devices", opt, p)
		}
		return nil
	}
	for _, b := range opts.binds {
		if err := check("--bind", b.container, true); err != nil {
			return err
		}
	}
	for _, t := range opts.tmpfs {
		if err := check("--tmpfs", t.path, false); err != nil {
			return err
		}
		if strings.Contains(t.path, ":") {
			return cmd.UsageError("--tmpfs %s: systemd cannot mount a tmpfs on a path with a colon", t.path)
		}
	}
	return nil
}

// unitFile is the [Service] section printed by droot unit.
type unitFile struct {
	comments []string
	settings []string // KEY=VALUE
}

// set adds the setting key with the words values, separated by spaces.
func (u *unitFile) set(key string, values ...string) {
	u.settings = append(u.settings, key+"="+strings.Join(values, " "))
}

func (u *unitFile) String() string {
	var b strings.Builder
	for _, c := range u.comments {
		b.WriteString("# " + c + "\n")
	}
	b.WriteString("[Service]\n")
	for _, s := range u.settings {
		b.WriteString(s + "\n")
	}
	return b.String()
}

// unit translates the run of argv, configured as for droot run, into the
// settings of a systemd service.
func (r *runner) unit(argv []string) (*unitFile, error) {
	for _, e := range r.cmd.Strings("env") {
		if k, _, ok := strings.Cut(e, "="); !ok {
			if _, found := os.LookupEnv(k); !found {
				log.Warnf("--env %s: not set in the environment of droot unit, so left out: the Environment= of your unit can give it", k)
			}
		}
	}
	extraEnv, err := commandLineEnv(r.cmd)
	if err != nil {
		return nil, err
	}
	baseEnv, err := containerEnv(r.root, r.cfg)
	if err != nil {
		return nil, err
	}
	env, err := environ.Merge(baseEnv, extraEnv)
	if err != nil {
		return nil, err
	}
	spec := r.user()
	// systemd runs a service as root unless told otherwise.
	cred, err := identity.ResolveAs(r.root, spec.user, spec.group, 0, 0)
	if err != nil {
		if spec.fromImage {
			return nil, fmt.Errorf("the image's USER %s: %w (use --user to run as another user)", r.cfg.User, err)
		}
		return nil, err
	}
	env = defaultEnv(env, cred)
	workdir := r.workdir()
	exe, err := lookPathInRoot(r.root, argv[0], workdir, env, cred)
	if err != nil {
		return nil, err
	}

	version, _, _ := strings.Cut(r.cmd.App.Version, " ")
	u := &unitFile{comments: []string{
		"Generated by droot unit " + version + ": runs the release in RootDirectory= as droot run",
		"would. Generate it again for another release.",
	}}
	if r.cfg != nil && r.cfg.Image != "" {
		// Quoted: the archive gives these values.
		u.comments = append(u.comments, "Image: "+strconv.Quote(r.cfg.Image)+" "+strconv.Quote(r.cfg.ImageID))
	}
	rootDir, err := systemd.PathValue("the root directory", r.opts.rootDir)
	if err != nil {
		return nil, err
	}
	u.set("RootDirectory", rootDir)
	// /proc, /sys, /run and a /dev with the usual device files, like
	// droot run's.
	u.set("MountAPIVFS", "yes")
	u.set("PrivateDevices", "yes")

	if err := u.setCommand(exe, argv); err != nil {
		return nil, err
	}
	wd, err := systemd.PathValue("the working directory", filepath.Clean(workdir))
	if err != nil {
		return nil, err
	}
	u.set("WorkingDirectory", wd)
	u.setCredential(cred)
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		a, err := systemd.EnvAssignment(k, v)
		if err != nil {
			return nil, err
		}
		u.set("Environment", a)
	}
	if r.cfg != nil && r.cfg.StopSignal != "" {
		if sig, ok := killSignal(r.cfg.StopSignal); ok {
			u.set("KillSignal", sig)
		} else {
			log.Warnf("leaving out the image's STOPSIGNAL %s, which droot does not know: set KillSignal= in your unit", r.cfg.StopSignal)
		}
	}
	if err := u.setMounts(r.opts, r.cmd.Bool("bind-net-files")); err != nil {
		return nil, err
	}
	if r.cmd.Bool("read-only") {
		u.set("ReadOnlyPaths", "+/") // "+": relative to RootDirectory=
	}
	u.setProtections(r.cmd, r.opts)
	return u, nil
}

// setCommand sets ExecStart= to run argv, whose executable is exe. The
// empty ExecStart= first resets the command of the unit the settings are a
// drop-in of.
func (u *unitFile) setCommand(exe string, argv []string) error {
	var words []string
	// systemd expands $VAR in the arguments, argv[0] included, but not in
	// the path of the executable: "@" sets them apart when they differ.
	if exe != argv[0] || strings.Contains(exe, "$") {
		path, err := systemd.Word("the command", exe)
		if err != nil {
			return err
		}
		words = append(words, "@"+path)
	}
	for i, a := range argv {
		w, err := systemd.ExecArg(fmt.Sprintf("argument %d of the command", i), a)
		if err != nil {
			return err
		}
		words = append(words, w)
	}
	u.set("ExecStart")
	u.set("ExecStart", words...)
	return nil
}

// setCredential sets the user and the groups of the service. systemd needs
// them in the host's databases: missing users and groups are reported, and
// missing supplementary groups left out.
func (u *unitFile) setCredential(cred *identity.Credential) {
	// User=0 would also make systemd drop CAP_SETUID with
	// NoNewPrivileges=: a service runs as root without User=.
	if cred.UID != 0 {
		u.set("User", strconv.Itoa(cred.UID))
		if !hostHasUID(cred.UID) {
			log.Warnf("the host has no user with the uid %d: systemd will not start the service (status 217/USER) until it has one", cred.UID)
		}
	}
	u.set("Group", strconv.Itoa(cred.GID))
	if !hostHasGID(cred.GID) {
		log.Warnf("the host has no group with the gid %d: systemd will not start the service (status 216/GROUP) until it has one", cred.GID)
	}
	if cred.Groups == nil {
		return
	}
	var groups, missing []string
	for _, g := range cred.Groups {
		id := strconv.Itoa(g)
		switch {
		case slices.Contains(groups, id) || slices.Contains(missing, id):
		case g == cred.GID || hostHasGID(g):
			groups = append(groups, id)
		default:
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		log.Warnf("leaving out the supplementary groups %s, which the host does not have: systemd would not start the service", strings.Join(missing, ", "))
	}
	u.set("SupplementaryGroups", groups...)
}

// setMounts sets the bind and tmpfs mounts of opts, and those of
// --bind-net-files if netFiles is set.
func (u *unitFile) setMounts(opts *runOptions, bindNet bool) error {
	binds := slices.Clone(opts.binds)
	if bindNet {
		for _, f := range netFiles {
			binds = append(binds, bindMount{host: f, container: f, opts: mounter.BindOptions{ReadOnly: true}})
		}
	}
	for _, b := range binds {
		src, err := systemd.PathWord("the source of a bind mount", b.host)
		if err != nil {
			return err
		}
		dest, err := systemd.PathWord("the destination of a bind mount", b.container)
		if err != nil {
			return err
		}
		key, rec := "BindPaths", "norbind"
		if b.opts.ReadOnly {
			key = "BindReadOnlyPaths"
		}
		if b.opts.Recursive {
			rec = "rbind"
		}
		u.set(key, src+":"+dest+":"+rec)
	}
	for _, t := range opts.tmpfs {
		if err := systemd.CheckPath("the path of a tmpfs", t.path); err != nil {
			return err
		}
		w, err := systemd.Word("the tmpfs "+t.path, t.path+":"+tmpfsOptions(t.opts))
		if err != nil {
			return err
		}
		u.set("TemporaryFileSystem", w)
	}
	return nil
}

// tmpfsOptions returns the mount options of a tmpfs for
// TemporaryFileSystem=, whose defaults (nodev,strictatime,mode=0755) are
// not those of droot run (nosuid,nodev,noexec and the kernel's mode 1777).
func tmpfsOptions(o mounter.FSOptions) string {
	pick := func(set bool, yes, no string) string {
		if set {
			return yes
		}
		return no
	}
	opts := []string{
		pick(o.ReadOnly, "ro", "rw"),
		pick(o.NoSuid, "nosuid", "suid"),
		pick(o.NoDev, "nodev", "dev"),
		pick(o.NoExec, "noexec", "exec"),
		"nostrictatime",
	}
	if o.NoAtime {
		opts = append(opts, "noatime")
	}
	if !slices.ContainsFunc(o.Data, func(d string) bool { return strings.HasPrefix(d, "mode=") }) {
		opts = append(opts, "mode=1777")
	}
	return strings.Join(append(opts, o.Data...), ",")
}

// setProtections sets the counterparts of the protections of droot run:
// the masked /proc and /sys, the capabilities, no_new_privs, the seccomp
// filter, the resource limits, the IPC namespace and --no-systemd.
func (u *unitFile) setProtections(cmd *cli.Invocation, opts *runOptions) {
	if !cmd.Bool("no-mask") {
		// /proc/sys, /sys and the like read-only, and /sys/fs/cgroup.
		u.set("ProtectKernelTunables", "yes")
		u.set("ProtectControlGroups", "yes")
		// "-": the path may be missing; "+": in RootDirectory=.
		var masked []string
		for _, p := range mounter.MaskedPaths {
			masked = append(masked, "-+"+p)
		}
		u.set("InaccessiblePaths", masked...)
	}
	if !cmd.Bool("no-dropcaps") {
		var caps []string
		for _, c := range slices.Sorted(maps.Keys(opts.keepCaps)) {
			caps = append(caps, osutil.CapabilityName(c))
		}
		// Empty: no capabilities at all.
		u.set("CapabilityBoundingSet", caps...)
	}
	if !cmd.IsSet("no-new-privileges") || cmd.Bool("no-new-privileges") {
		u.set("NoNewPrivileges", "yes")
	}
	if cmd.String("seccomp") == "default" {
		u.set("SystemCallFilter", "~"+strings.Join(seccomp.DeniedSystemCalls, " "))
		u.set("SystemCallErrorNumber", "EPERM")
		u.set("RestrictNamespaces", "yes")
		u.set("SystemCallArchitectures", "native")
	}
	if cmd.Bool("unshare-ipc") {
		u.set("PrivateIPC", "yes")
	}
	for _, l := range opts.rlimits {
		value := func(v uint64) string {
			if v == osutil.RlimInfinity {
				return "infinity"
			}
			return strconv.FormatUint(v, 10)
		}
		v := value(l.Soft)
		if l.Hard != l.Soft {
			v += ":" + value(l.Hard)
		}
		u.set("Limit"+strings.ToUpper(l.Name), v)
	}
	if cmd.Bool("no-systemd") {
		u.set("NotifyAccess", "none")
		u.set("UnsetEnvironment", systemd.ServiceVariables...)
	}
}

// lookPathInRoot finds the executable of COMMAND, file, in root as droot
// run finds it once chrooted: in the PATH of env unless it contains a
// slash, and relative to workdir if it is a relative path. Only the
// permission bits of the file itself are checked, not those of its
// directories.
func lookPathInRoot(root *osutil.Root, file, workdir string, env []string, cred *identity.Credential) (string, error) {
	isExecutable := func(p string) error {
		fi, err := root.Stat(p)
		if err != nil {
			return err
		}
		return osutil.CanExecuteFile(p, fi, cred.UID, cred.GID, cred.Groups)
	}
	if strings.Contains(file, "/") {
		p := filepath.Clean(file)
		if !filepath.IsAbs(file) {
			p = filepath.Join("/", workdir, file)
		}
		if err := isExecutable(p); err != nil {
			return "", &fs.PathError{Op: "exec", Path: file, Err: err}
		}
		return p, nil
	}
	pathEnv, _ := environ.Lookup(env, "PATH")
	p, err := osutil.LookPath(file, pathEnv, isExecutable)
	if err != nil {
		return "", err
	}
	return filepath.Clean(p), nil
}

// killSignal returns the signal of the image's STOPSIGNAL s for
// KillSignal=, e.g. SIGTERM for SIGTERM, TERM or 15, as docker accepts
// them.
func killSignal(s string) (string, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if n, err := strconv.Atoi(s); err == nil {
		if n <= 0 || n >= 32 {
			return "", false
		}
		name := unix.SignalName(syscall.Signal(n))
		return name, name != ""
	}
	if !strings.HasPrefix(s, "SIG") {
		s = "SIG" + s
	}
	if unix.SignalNum(s) == 0 {
		return "", false
	}
	return s, true
}
