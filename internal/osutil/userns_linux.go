package osutil

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
)

// IDRange maps Size IDs of a user namespace, from Inside, to the IDs of
// its parent namespace from Outside (a line of /proc/PID/uid_map).
type IDRange struct {
	Inside, Outside, Size int64
}

// ParseIDMap parses the content of /proc/PID/uid_map or gid_map.
func ParseIDMap(r io.Reader) ([]IDRange, error) {
	var ranges []IDRange
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 3 {
			continue
		}
		var v [3]int64
		for i := range f {
			n, err := strconv.ParseInt(f[i], 10, 64)
			if err != nil {
				return nil, err
			}
			v[i] = n
		}
		ranges = append(ranges, IDRange{Inside: v[0], Outside: v[1], Size: v[2]})
	}
	return ranges, sc.Err()
}

// IsInitialIDMap reports whether ranges map every ID to itself: the IDs of
// the initial user namespace.
func IsInitialIDMap(ranges []IDRange) bool {
	return len(ranges) == 1 && ranges[0] == IDRange{0, 0, 4294967295}
}

// Mapped reports whether id is mapped by ranges.
func Mapped(ranges []IDRange, id int) bool {
	for _, r := range ranges {
		if int64(id) >= r.Inside && int64(id) < r.Inside+r.Size {
			return true
		}
	}
	return false
}

func readIDMap(name string) ([]IDRange, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseIDMap(f)
}

// nsState is read once, from the /proc of droot: the first call must come
// before the chroot (the container's /proc could say anything).
var nsState = sync.OnceValue(func() struct{ userNS, setgroupsDenied bool } {
	ranges, err := readIDMap("/proc/self/uid_map")
	b, _ := os.ReadFile("/proc/self/setgroups")
	return struct{ userNS, setgroupsDenied bool }{
		userNS:          err == nil && !IsInitialIDMap(ranges),
		setgroupsDenied: strings.TrimSpace(string(b)) == "deny",
	}
})

// InUserNamespace reports whether droot runs in a user namespace other than
// the initial one, as in rootless mode: there, root has its privileges over
// the namespace only, and cannot create device nodes nor mount procfs
// without a PID namespace. The first call must come before the chroot.
func InUserNamespace() bool { return nsState().userNS }

// UIDMapped and GIDMapped report whether an ID exists in the user namespace
// of droot, i.e. whether droot can switch to it.
func UIDMapped(uid int) bool { return idMapped("/proc/self/uid_map", uid) }

// GIDMapped is UIDMapped for group IDs.
func GIDMapped(gid int) bool { return idMapped("/proc/self/gid_map", gid) }

func idMapped(name string, id int) bool {
	ranges, err := readIDMap(name)
	return err != nil || Mapped(ranges, id)
}

// SetgroupsAllowed reports whether setgroups(2) is allowed: it is not in a
// user namespace whose GID map was written without privileges. Like
// InUserNamespace, it is read once, before the chroot.
func SetgroupsAllowed() bool { return !nsState().userNS || !nsState().setgroupsDenied }
