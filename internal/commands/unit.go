package commands

import (
	"context"
	"fmt"
	"io"
	"maps"
	"math"
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
			"the environment of droot unit; without one, KEY is left out, the image's value\n" +
			"too, so that the Environment= of your unit gives it.\n\n" +
			"The service has no pseudo-terminals: systemd's /dev would show the host's.\n" +
			"The options that have no counterpart in a unit are refused: --copy-files,\n" +
			"--env-file, --hostname, and a --bind or a --tmpfs on /dev or below (but\n" +
			"--tmpfs /dev/shm). --unshare is implied. Values that systemd would read\n" +
			"differently or refuse, such as environment variables whose names are not made\n" +
			"of letters, digits and underscores or whose values contain control characters,\n" +
			"are errors.",
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
		return writeFile(ctx, output, false, write)
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
		case under(p, "/dev") && (bind || p != "/dev/shm"):
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
	fromUnit := bareEnvFromUnit(r.cmd)
	env, cred, err := r.unitIdentity()
	if err != nil {
		return nil, err
	}
	workdir, err := r.unitWorkdir(cred)
	if err != nil {
		return nil, err
	}
	exe, err := lookPathInRoot(r.root, argv[0], workdir, env, cred)
	if err != nil {
		return nil, err
	}

	u := &unitFile{comments: unitHeader(r.cmd.App.Version, r.cfg)}
	rootDir, err := systemd.PathValue("the root directory", r.opts.rootDir)
	if err != nil {
		return nil, err
	}
	u.set("RootDirectory", rootDir)
	// /proc, /sys, /run and a /dev with the usual device files, like
	// droot run's.
	u.set("MountAPIVFS", "yes")
	u.set("PrivateDevices", "yes")
	// systemd's /dev has the host's devpts, /dev/shm and /dev/hugepages,
	// where droot run makes a devpts instance and a tmpfs of its own: no
	// pseudo-terminals (see setMounts for /dev/shm).
	u.set("InaccessiblePaths", "-+/dev/pts", "-+/dev/hugepages")

	if err := u.setCommand(exe, argv); err != nil {
		return nil, err
	}
	wd, err := systemd.PathValue("the working directory", workdir)
	if err != nil {
		return nil, err
	}
	u.set("WorkingDirectory", wd)
	if err := u.setCredential(cred); err != nil {
		return nil, err
	}
	if err := u.setEnvironment(env, fromUnit); err != nil {
		return nil, err
	}
	u.setKillSignal(r.cfg)
	if err := u.setMounts(r.opts, r.cmd.Bool("bind-net-files")); err != nil {
		return nil, err
	}
	if r.cmd.Bool("read-only") {
		u.set("ReadOnlyPaths", "+/") // "+": relative to RootDirectory=
	}
	if err := u.setProtections(r.cmd, r.opts); err != nil {
		return nil, err
	}
	return u, nil
}

// bareEnvFromUnit returns the keys of the bare --env KEY that droot unit
// does not have. droot run takes a bare --env KEY from its own environment,
// which the Environment= of its unit gives: the settings leave these keys
// out, the image's value too, which would override the Environment= of the
// unit they are a drop-in of.
func bareEnvFromUnit(cmd *cli.Invocation) []string {
	var keys []string
	for _, e := range cmd.Strings("env") {
		if _, found := os.LookupEnv(e); !strings.Contains(e, "=") && !found {
			keys = append(keys, e)
		}
	}
	if len(keys) > 0 {
		log.Warnf("--env %s: not set when droot unit runs, so left out of the settings (the image's value too): "+
			"the Environment= of your unit gives it, as droot run would take it from there", strings.Join(keys, ", --env "))
	}
	return keys
}

// unitIdentity returns the environment of COMMAND and its credential, as
// droot run computes them, for a service that systemd runs as root unless
// told otherwise.
func (r *runner) unitIdentity() ([]string, *identity.Credential, error) {
	extraEnv, err := commandLineEnv(r.cmd)
	if err != nil {
		return nil, nil, err
	}
	baseEnv, err := containerEnv(r.root, r.cfg)
	if err != nil {
		return nil, nil, err
	}
	env, err := environ.Merge(baseEnv, extraEnv)
	if err != nil {
		return nil, nil, err
	}
	spec := r.user()
	cred, err := identity.ResolveAs(r.root, spec.user, spec.group, 0, 0)
	if err != nil {
		if spec.fromImage {
			return nil, nil, fmt.Errorf("the image's USER %s: %w (use --user to run as another user)", r.cfg.User, err)
		}
		return nil, nil, err
	}
	return defaultEnv(env, cred), cred, nil
}

// unitHeader returns the comments at the top of the settings, written by
// droot version for the image of cfg (nil without image configuration).
func unitHeader(version string, cfg *imageconfig.Config) []string {
	version, _, _ = strings.Cut(version, " ")
	comments := []string{
		"Generated by droot unit " + version + ": runs the release in RootDirectory= as droot run",
		"would. Generate it again for another release.",
	}
	if cfg != nil && cfg.Image != "" {
		// Quoted: the archive gives these values.
		comments = append(comments, "Image: "+strconv.Quote(cfg.Image)+" "+strconv.Quote(cfg.ImageID))
	}
	return comments
}

// setEnvironment sets the variables of env, but those of the keys skip.
func (u *unitFile) setEnvironment(env, skip []string) error {
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		if slices.Contains(skip, k) {
			continue
		}
		a, err := systemd.EnvAssignment(k, v)
		if err != nil {
			return err
		}
		u.set("Environment", a)
	}
	return nil
}

// setKillSignal sets KillSignal= to the STOPSIGNAL of the image of cfg, if
// any, unless droot does not know it.
func (u *unitFile) setKillSignal(cfg *imageconfig.Config) {
	if cfg == nil || cfg.StopSignal == "" {
		return
	}
	sig, ok := killSignal(cfg.StopSignal)
	if !ok {
		log.Warnf("leaving out the image's STOPSIGNAL %s, which droot does not know: set KillSignal= in your unit", cfg.StopSignal)
		return
	}
	u.set("KillSignal", sig)
}

// unitWorkdir returns the working directory of COMMAND (see workdir) as the
// kernel resolves it, which must exist. systemd enters it as the user of the
// service, after the switch, where droot run enters it as root: a directory
// the user cannot search is reported.
func (r *runner) unitWorkdir(cred *identity.Credential) (string, error) {
	workdir := r.workdir()
	fi, err := r.root.Stat(workdir)
	if err != nil {
		return "", fmt.Errorf("the working directory: %w", err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("the working directory %s is not a directory", workdir)
	}
	if !osutil.CanSearch(fi, cred.UID, cred.GID, cred.Groups) {
		log.Warnf("systemd enters the working directory %s as uid %d, who cannot: the service would fail (status 200/CHDIR)", workdir, cred.UID)
	}
	return pathInRoot(r.root, workdir, true)
}

// pathInRoot returns the path p of root as the kernel resolves it, for a
// unit. A path without ".." component is only cleaned. With one, the
// directories are resolved through their symlinks, as ".." leaves the
// target of a symlink, not the symlink itself; the last component is kept
// as it is (a symlink to an executable stays one) unless dir is set.
func pathInRoot(root *osutil.Root, p string, dir bool) (string, error) {
	if !slices.Contains(strings.Split(p, "/"), "..") {
		return filepath.Clean(p), nil
	}
	d, base := p, ""
	if !dir {
		i := strings.LastIndex(p, "/")
		d, base = p[:i+1], p[i+1:]
	}
	f, err := root.OpenDir(d, false, nil)
	if err != nil {
		return "", err
	}
	defer f.Close()
	host, err := os.Readlink(osutil.FdPath(int(f.Fd())))
	if err != nil {
		return "", err
	}
	rel, ok := strings.CutPrefix(host, root.Path())
	if !ok || (rel != "" && rel[0] != '/') {
		return "", fmt.Errorf("cannot tell where %s leads in %s (%s)", p, root.Path(), host)
	}
	return filepath.Join("/", rel, base), nil
}

// setCommand sets ExecStart= to run argv, whose executable is exe. The
// empty ExecStart= first resets the command of the unit the settings are a
// drop-in of.
func (u *unitFile) setCommand(exe string, argv []string) error {
	// systemd refuses quotes and backslashes in the path of the command.
	if err := systemd.CheckPath("the command", exe); err != nil {
		return err
	}
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
// missing supplementary groups left out. The empty User= and
// SupplementaryGroups= reset those of the unit the settings are a drop-in
// of.
func (u *unitFile) setCredential(cred *identity.Credential) error {
	for _, id := range append([]int{cred.UID, cred.GID}, cred.Groups...) {
		if id == 65535 {
			return fmt.Errorf("systemd refuses the user and group ID 65535 (the 16-bit -1): use --user and --group to choose others")
		}
	}
	// Root is the default, which an empty User= restores: with User=0,
	// systemd 255 drops CAP_SETUID (with NoNewPrivileges= and the
	// sandboxing of these settings).
	if cred.UID != 0 {
		u.set("User", strconv.Itoa(cred.UID))
		if !hostHasUID(cred.UID) {
			log.Warnf("the host has no user with the uid %d: systemd will not start the service (status 217/USER) until it has one", cred.UID)
		}
	} else {
		u.set("User")
	}
	u.set("Group", strconv.Itoa(cred.GID))
	if !hostHasGID(cred.GID) {
		log.Warnf("the host has no group with the gid %d: systemd will not start the service (status 216/GROUP) until it has one", cred.GID)
	}
	u.set("SupplementaryGroups")
	if cred.Groups == nil {
		return nil
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
	return nil
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
	tmpfs := slices.Clone(opts.tmpfs)
	if !slices.ContainsFunc(tmpfs, func(t tmpfsMount) bool { return t.path == "/dev/shm" }) {
		// Over the host's /dev/shm of systemd's /dev, as droot run.
		tmpfs = append(tmpfs, tmpfsMount{path: "/dev/shm", opts: shmfs})
	}
	for _, t := range tmpfs {
		w, err := systemd.TmpfsWord(t.path, tmpfsOptions(t.opts))
		if err != nil {
			return err
		}
		u.set("TemporaryFileSystem", w)
	}
	return nil
}

// tmpfsOptions returns the mount options of a tmpfs for
// TemporaryFileSystem=, whose defaults (nodev,strictatime,mode=0755,
// size=10%,nr_inodes=400k) are not those of droot run (nosuid,nodev,noexec
// and the kernel's mode 1777 and size of 50% of the memory). The number of
// inodes keeps systemd's default, which the kernel's cannot be written as.
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
	has := func(prefixes ...string) bool {
		return slices.ContainsFunc(o.Data, func(d string) bool {
			return slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(d, p) })
		})
	}
	if !has("mode=") {
		opts = append(opts, "mode=1777")
	}
	if !has("size=", "nr_blocks=") {
		opts = append(opts, "size=50%")
	}
	return strings.Join(append(opts, o.Data...), ",")
}

// setProtections sets the counterparts of the protections of droot run:
// the masked /proc and /sys, the capabilities, no_new_privs, the seccomp
// filter, the resource limits, the IPC namespace and --no-systemd.
func (u *unitFile) setProtections(cmd *cli.Invocation, opts *runOptions) error {
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
		// systemd ORs the lines of CapabilityBoundingSet=: the empty one
		// resets the set (that of the unit the settings are a drop-in of
		// included), to no capabilities at all.
		u.set("CapabilityBoundingSet")
		if len(caps) > 0 {
			u.set("CapabilityBoundingSet", caps...)
		}
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
		if err := checkUnitRlimit(l); err != nil {
			return err
		}
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
	return nil
}

// unitRlimitMax are the largest values systemd 255 accepts for the limits
// that are not counts or sizes: LimitCPU= in seconds, LimitRTTIME= in
// microseconds and LimitNICE= as the raw value of RLIMIT_NICE, which has no
// infinity.
var unitRlimitMax = map[string]uint64{"cpu": 18446744073708, "rttime": math.MaxInt64, "nice": 40}

// checkUnitRlimit refuses a --ulimit that systemd would ignore.
func checkUnitRlimit(l osutil.Rlimit) error {
	limit, ok := unitRlimitMax[l.Name]
	if !ok {
		return nil
	}
	for _, v := range []uint64{l.Soft, l.Hard} {
		if v > limit && (v != osutil.RlimInfinity || l.Name == "nice") {
			return fmt.Errorf("--ulimit %s: systemd accepts %s up to %d", l.Name, l.Name, limit)
		}
	}
	return nil
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
		p := file
		if !filepath.IsAbs(p) {
			// Not joined lexically: root resolves ".." like the kernel.
			p = workdir + "/" + file
		}
		if err := isExecutable(p); err != nil {
			return "", err
		}
		return pathInRoot(root, p, false)
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
