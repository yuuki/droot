// Package identity resolves the user and groups a chrooted process runs as.
//
// Names are looked up in the container's /etc/passwd and /etc/group first,
// because that is what the process will see once chrooted, and on the host
// second, for compatibility with droot <= 0.7 which only used the host's
// databases. On the host, /etc/passwd and /etc/group are read first, then
// getent(1) is asked, so that users and groups served only by NSS modules
// (LDAP, SSSD, ...) are found although the static droot binary cannot load
// them.
package identity

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	osuser "os/user"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yuuki/droot/internal/log"
	"github.com/yuuki/droot/internal/osutil"
)

// Credential is the identity a process runs as.
type Credential struct {
	UID int
	GID int
	// Groups are the supplementary groups. nil means "leave unchanged".
	Groups []int
	// Username and Home are set when the user is known.
	Username string
	Home     string
}

// User is an entry of a passwd(5) database.
type User struct {
	Name string
	UID  int
	GID  int
	Home string
}

// Group is an entry of a group(5) database.
type Group struct {
	Name    string
	GID     int
	Members []string
}

// Resolve computes the credential for --user userSpec and --group groupSpec.
// Each spec is either a name or a numeric ID.
//
//   - With neither spec, the current uid/gid are kept and groups are untouched;
//     Username and Home are taken from the container's /etc/passwd if the
//     uid is listed there.
//   - With a user, the gid defaults to the user's primary group and the
//     supplementary groups are the groups listing the user in the container's
//     /etc/group.
//   - A numeric uid that is unknown everywhere is accepted as is (like Docker);
//     its gid then defaults to the current gid unless --group is given.
//
// The databases are read through root: symlinks in the container are
// resolved inside it and only regular files are read.
//
// The image decides which uid a user name maps to; a warning is printed when
// a name other than "root" maps to uid 0. Numeric IDs do not depend on the
// image.
func Resolve(root *osutil.Root, userSpec, groupSpec string) (*Credential, error) {
	return ResolveAs(root, userSpec, groupSpec, os.Getuid(), os.Getgid())
}

// ResolveAs is Resolve for a process that runs as uid and gid instead of
// droot's own user: they are the credential without user and group, and gid
// is the group of a numeric uid that is unknown everywhere.
func ResolveAs(root *osutil.Root, userSpec, groupSpec string, uid, gid int) (*Credential, error) {
	cred := &Credential{UID: uid, GID: gid}
	if userSpec == "" && groupSpec == "" {
		// Keep the identity, but find its name and home directory in
		// the container, for HOME and USER. This is best effort: the
		// identity does not depend on it.
		users, err := readPasswd(root, "/etc/passwd")
		if err != nil {
			log.Debugf("cannot read the container's /etc/passwd: %v", err)
		}
		for _, u := range users {
			if u.UID == cred.UID {
				cred.Username, cred.Home = u.Name, u.Home
				break
			}
		}
		return cred, nil
	}

	users, err := readPasswd(root, "/etc/passwd")
	if err != nil {
		return nil, err
	}
	groups, err := readGroup(root, "/etc/group")
	if err != nil {
		return nil, err
	}

	if userSpec != "" {
		u, err := lookupUser(users, userSpec)
		if err != nil {
			return nil, err
		}
		if _, numeric := parseID(userSpec); !numeric && u.UID == 0 && u.Name != "root" {
			log.Warnf("--user %s maps to uid 0 (root) in the container's /etc/passwd", userSpec)
		}
		cred.UID, cred.Username, cred.Home = u.UID, u.Name, u.Home
		if u.GID >= 0 {
			cred.GID = u.GID
		} else if groupSpec == "" {
			log.Warnf("--user %s has no entry in the container's /etc/passwd: running with gid %d (droot's own group); use --group to choose one", userSpec, cred.GID)
		}
	}
	if groupSpec != "" {
		g, err := lookupGroup(groups, groupSpec)
		if err != nil {
			return nil, err
		}
		cred.GID = g.GID
	}

	cred.Groups = []int{cred.GID}
	if cred.Username != "" {
		for _, g := range groups {
			if slices.Contains(g.Members, cred.Username) && !slices.Contains(cred.Groups, g.GID) {
				cred.Groups = append(cred.Groups, g.GID)
			}
		}
	}
	return cred, nil
}

func parseID(s string) (int, bool) {
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil || n > math.MaxInt32 {
		return 0, false
	}
	return int(n), true
}

func lookupUser(users []User, spec string) (User, error) {
	id, numeric := parseID(spec)
	for _, u := range users {
		if (numeric && u.UID == id) || (!numeric && u.Name == spec) {
			return u, nil
		}
	}

	if u, ok := lookupHostUser(spec); ok {
		return u, nil
	}
	if numeric {
		return User{UID: id, GID: -1}, nil
	}
	return User{}, fmt.Errorf("user %q not found in the container's /etc/passwd nor on the host", spec)
}

func lookupGroup(groups []Group, spec string) (Group, error) {
	id, numeric := parseID(spec)
	for _, g := range groups {
		if (numeric && g.GID == id) || (!numeric && g.Name == spec) {
			return g, nil
		}
	}
	if numeric {
		return Group{GID: id}, nil
	}
	if g, ok := lookupHostGroup(spec); ok {
		return g, nil
	}
	return Group{}, fmt.Errorf("group %q not found in the container's /etc/group nor on the host", spec)
}

// lookupHostUser looks a user up on the host: in /etc/passwd (or through
// the C library when droot is built with cgo), then with getent(1).
func lookupHostUser(spec string) (User, bool) {
	id, numeric := parseID(spec)
	var hu *osuser.User
	var err error
	if numeric {
		hu, err = osuser.LookupId(spec)
	} else {
		hu, err = osuser.Lookup(spec)
	}
	if err == nil {
		uid, ok1 := parseID(hu.Uid)
		gid, ok2 := parseID(hu.Gid)
		if ok1 && ok2 {
			return User{Name: hu.Username, UID: uid, GID: gid, Home: hu.HomeDir}, true
		}
	}

	f := getentEntry("passwd", spec, 7)
	if f == nil {
		return User{}, false
	}
	uid, ok1 := parseID(f[2])
	gid, ok2 := parseID(f[3])
	if f[0] == "" || !ok1 || !ok2 || (numeric && uid != id) || (!numeric && f[0] != spec) {
		return User{}, false
	}
	log.Debugf("user %s found with getent: uid=%d gid=%d", spec, uid, gid)
	return User{Name: f[0], UID: uid, GID: gid, Home: f[5]}, true
}

// HostHasUID reports whether the host's user database has the user uid.
func HostHasUID(uid int) bool {
	_, ok := lookupHostUser(strconv.Itoa(uid))
	return ok
}

// HostHasGID reports whether the host's group database has the group gid: in
// /etc/group (or through the C library when droot is built with cgo), then
// with getent(1).
func HostHasGID(gid int) bool {
	key := strconv.Itoa(gid)
	if _, err := osuser.LookupGroupId(key); err == nil {
		return true
	}
	f := getentEntry("group", key, 3)
	if f == nil {
		return false
	}
	id, ok := parseID(f[2])
	return ok && id == gid
}

// lookupHostGroup looks a group name up on the host: in /etc/group (or
// through the C library when droot is built with cgo), then with getent(1).
func lookupHostGroup(name string) (Group, bool) {
	if hg, err := osuser.LookupGroup(name); err == nil {
		if gid, ok := parseID(hg.Gid); ok {
			return Group{Name: hg.Name, GID: gid}, true
		}
	}
	f := getentEntry("group", name, 3)
	if f == nil {
		return Group{}, false
	}
	gid, ok := parseID(f[2])
	if !ok || f[0] != name {
		return Group{}, false
	}
	log.Debugf("group %s found with getent: gid=%d", name, gid)
	return Group{Name: f[0], GID: gid}, true
}

// getentPaths are where getent(1) is looked for. $PATH is deliberately not
// used: droot runs as root, possibly with the PATH of the user who called
// sudo.
var getentPaths = []string{"/usr/bin/getent", "/bin/getent", "/usr/sbin/getent", "/sbin/getent"}

// getentTimeout bounds a lookup by an unreachable directory server.
const getentTimeout = 30 * time.Second

// getent runs `getent database key` and returns its output. It returns nil
// output and no error when the key is not found.
var getent = func(database, key string) ([]byte, error) {
	var path string
	for _, p := range getentPaths {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			path = p
			break
		}
	}
	if path == "" {
		return nil, errors.New("getent not found in " + strings.Join(getentPaths, ", "))
	}
	ctx, cancel := context.WithTimeout(context.Background(), getentTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, database, key)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
		return nil, nil // "One or more supplied key could not be found"
	}
	if err != nil {
		return nil, fmt.Errorf("%s %s %s: %w: %s", path, database, key, err, bytes.TrimSpace(stderr.Bytes()))
	}
	return out, nil
}

// getentEntry returns the fields of the first entry getent(1) prints for
// key in database, or nil.
func getentEntry(database, key string, minFields int) []string {
	// A key starting with "-" would be taken as an option, and such names
	// are not valid user or group names anyway.
	if key == "" || strings.HasPrefix(key, "-") {
		return nil
	}
	out, err := getent(database, key)
	if err != nil {
		log.Debugf("cannot look up %s in the %s database: %v", key, database, err)
		return nil
	}
	var fields []string
	_ = parseColonFile(bytes.NewReader(out), minFields, func(f []string) {
		if fields == nil {
			fields = f
		}
	})
	return fields
}

func readPasswd(root *osutil.Root, path string) ([]User, error) {
	var users []User
	err := readColonFile(root, path, 7, func(f []string) {
		uid, ok1 := parseID(f[2])
		gid, ok2 := parseID(f[3])
		if f[0] != "" && ok1 && ok2 {
			users = append(users, User{Name: f[0], UID: uid, GID: gid, Home: f[5]})
		}
	})
	return users, err
}

func readGroup(root *osutil.Root, path string) ([]Group, error) {
	var groups []Group
	err := readColonFile(root, path, 4, func(f []string) {
		gid, ok := parseID(f[2])
		if f[0] == "" || !ok {
			return
		}
		var members []string
		for m := range strings.SplitSeq(f[3], ",") {
			if m = strings.TrimSpace(m); m != "" {
				members = append(members, m)
			}
		}
		groups = append(groups, Group{Name: f[0], GID: gid, Members: members})
	})
	return groups, err
}

// readColonFile calls fn for every line of a passwd(5)-style file of the
// container that has at least minFields fields. A missing file is treated as
// empty.
func readColonFile(root *osutil.Root, path string, minFields int, fn func([]string)) error {
	f, err := root.OpenFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	if err := parseColonFile(f, minFields, fn); err != nil {
		return fmt.Errorf("read %s: %w", f.Name(), err)
	}
	return nil
}

func parseColonFile(r io.Reader, minFields int, fn func([]string)) error {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 4096), 1<<20)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		// Skip comments and NIS compat entries (+user, -user).
		if line == "" || line[0] == '#' || line[0] == '+' || line[0] == '-' {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) >= minFields {
			fn(fields)
		}
	}
	return s.Err()
}
