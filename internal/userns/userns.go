// Package userns runs droot rootless: a non-root droot runs itself again in
// a new user namespace, where it is root, and supervises it.
//
// The user's own UID and GID become 0 in the namespace. If /etc/subuid and
// /etc/subgid give the user subordinate IDs and the newuidmap and newgidmap
// helpers (from shadow-utils, setuid root) are installed, they are mapped
// to 1, 2, ... too, so that the users of an image exist in the namespace;
// otherwise root is its only user.
//
// The child is started in two stages: the first waits until the parent has
// written the ID maps, then executes droot again, so that the second stage
// starts as root of the namespace, with its capabilities there.
package userns

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Environment variables of the stages.
const (
	// envSync names the descriptor on which the first stage waits.
	envSync = "_DROOT_USERNS_SYNC"
	// envParent is the PID of the supervising droot, which the child
	// must not survive.
	envParent = "_DROOT_USERNS_PARENT"
)

// Range maps Size IDs of the namespace, from Inside, to the IDs of the host
// from Outside.
type Range struct {
	Inside, Outside, Size int
}

// Mapping is the ID mapping of a user namespace.
type Mapping struct {
	UIDs, GIDs []Range
	// Helpers is set when the mapping needs newuidmap and newgidmap.
	Helpers bool
}

// String describes the mapping for messages.
func (m Mapping) String() string {
	return "uid " + rangesString(m.UIDs) + ", gid " + rangesString(m.GIDs)
}

func rangesString(rs []Range) string {
	var parts []string
	for _, r := range rs {
		if r.Size == 1 {
			parts = append(parts, fmt.Sprintf("%d→%d", r.Inside, r.Outside))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d→%d-%d", r.Inside, r.Inside+r.Size-1, r.Outside, r.Outside+r.Size-1))
		}
	}
	return strings.Join(parts, " ")
}

// SubIDs parses /etc/subuid or /etc/subgid and returns the ranges of the
// user, given by name or by ID (lines NAME:START:COUNT or ID:START:COUNT).
func SubIDs(r io.Reader, name string, id int) ([]Range, error) {
	var ranges []Range
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, ":")
		if len(f) != 3 || (f[0] != name && f[0] != strconv.Itoa(id)) || f[0] == "" {
			continue
		}
		start, err1 := strconv.Atoi(f[1])
		count, err2 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil || start < 0 || count <= 0 {
			continue
		}
		ranges = append(ranges, Range{Outside: start, Size: count})
	}
	return ranges, sc.Err()
}

// buildMapping maps id to 0, then the subordinate ranges after it.
func buildMapping(id int, sub []Range) []Range {
	m := []Range{{Inside: 0, Outside: id, Size: 1}}
	next := 1
	for _, r := range sub {
		m = append(m, Range{Inside: next, Outside: r.Outside, Size: r.Size})
		next += r.Size
	}
	return m
}

// helperArgs are the arguments of newuidmap or newgidmap for pid.
func helperArgs(pid int, ranges []Range) []string {
	args := []string{strconv.Itoa(pid)}
	for _, r := range ranges {
		args = append(args, strconv.Itoa(r.Inside), strconv.Itoa(r.Outside), strconv.Itoa(r.Size))
	}
	return args
}

// mapString is the content of /proc/PID/uid_map or gid_map for ranges.
func mapString(ranges []Range) string {
	var b strings.Builder
	for _, r := range ranges {
		fmt.Fprintf(&b, "%d %d %d\n", r.Inside, r.Outside, r.Size)
	}
	return b.String()
}

// ExitError is the exit status of the supervised droot, for main to exit
// with, silently: the child has reported its errors itself.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }
