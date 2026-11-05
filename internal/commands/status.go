package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode"
	"unicode/utf8"

	"github.com/yuuki/droot/internal/cli"
	"github.com/yuuki/droot/internal/mounter"
	"github.com/yuuki/droot/internal/osutil"
)

func statusCommand() *cli.Command {
	return &cli.Command{
		Name:  "status",
		Usage: "Show the mounts and the processes of a root directory",
		Description: "Lists the mount points below ROOT_DIR, i.e. the mounts made by 'droot run' that\n" +
			"'droot umount' would remove, and the processes whose root directory is\n" +
			"ROOT_DIR: the commands started by 'droot run' and their children.\n\n" +
			"Processes of a 'droot run --unshare' are listed, but their mounts are in a\n" +
			"private mount namespace and are not. Run as root to see every process.",
		Flags: []*cli.Flag{
			{Name: "root", Aliases: []string{"r"}, Kind: cli.String, Placeholder: "ROOT_DIR", Usage: "root directory used with 'droot run' (required)", Complete: "dir"},
		},
		Action: doStatus,
	}
}

func doStatus(_ context.Context, cmd *cli.Invocation) error {
	if err := setup(cmd); err != nil {
		return err
	}
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
	mounts, err := mounter.NewMounter(root).MountInfos()
	if err != nil {
		return err
	}
	slices.SortStableFunc(mounts, func(a, b mounter.MountInfo) int { return strings.Compare(a.Mountpoint, b.Mountpoint) })
	procs, denied, err := processesInRoot(rootDir)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(cmd.App.Output(), 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "Mounts below %s: %d\n", rootDir, len(mounts))
	if len(mounts) > 0 {
		fmt.Fprintln(w, "  MOUNT POINT\tTYPE\tSOURCE\tOPTIONS")
		for _, m := range mounts {
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", printable(strings.TrimPrefix(m.Mountpoint, rootDir)), printable(m.FSType), printable(m.Source), printable(m.Options))
		}
	}
	fmt.Fprintf(w, "\nProcesses in %s: %d\n", rootDir, len(procs))
	if len(procs) > 0 {
		fmt.Fprintln(w, "  PID\tUID\tCOMMAND")
		for _, p := range procs {
			fmt.Fprintf(w, "  %d\t%d\t%s\n", p.pid, p.uid, printable(p.cmdline))
		}
	}
	if denied && os.Geteuid() != 0 {
		fmt.Fprintln(w, "\n(some processes could not be inspected: run as root to see every process)")
	}
	return w.Flush()
}

// printable escapes the characters of s that are not printable, so that
// names chosen in the container (command lines, mount points) cannot send
// escape sequences to the terminal.
func printable(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size <= 1: // invalid UTF-8
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case unicode.IsPrint(r):
			b.WriteString(s[i : i+size])
		case r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
		i += size
	}
	return b.String()
}

type process struct {
	pid, uid int
	cmdline  string
}

// processesInRoot returns the processes whose root directory is rootDir,
// found by comparing /proc/PID/root (which the kernel resolves to the
// process's root, whatever its mount namespace) with rootDir. denied
// reports that some processes could not be inspected.
func processesInRoot(rootDir string) (procs []process, denied bool, err error) {
	want, err := os.Stat(rootDir)
	if err != nil {
		return nil, false, err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, false, err
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := filepath.Join("/proc", e.Name())
		fi, err := os.Stat(filepath.Join(dir, "root"))
		if errors.Is(err, fs.ErrPermission) {
			denied = true
			continue
		}
		if err != nil || !os.SameFile(fi, want) {
			continue // exited, or another root
		}
		p := process{pid: pid, uid: -1}
		if st, err := os.Stat(dir); err == nil {
			if uid, _, _, ok := osutil.FileOwner(st); ok {
				p.uid = uid
			}
		}
		if b, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
			p.cmdline = string(bytes.Join(bytes.Split(bytes.TrimRight(b, "\x00"), []byte{0}), []byte{' '}))
		}
		if p.cmdline == "" {
			if b, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil {
				p.cmdline = "[" + strings.TrimSpace(string(b)) + "]"
			}
		}
		procs = append(procs, p)
	}
	slices.SortFunc(procs, func(a, b process) int { return a.pid - b.pid })
	return procs, denied, nil
}
