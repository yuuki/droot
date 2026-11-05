// Package identity resolves the user and groups a chrooted process runs as.
//
// Names are looked up in the container's /etc/passwd and /etc/group first,
// because that is what the process will see once chrooted, and on the host
// second, for compatibility with droot <= 0.7 which only used the host's
// databases.
package identity

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	osuser "os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
//   - With neither spec, the current uid/gid are kept and groups are untouched.
//   - With a user, the gid defaults to the user's primary group and the
//     supplementary groups are the groups listing the user in the container's
//     /etc/group.
//   - A numeric uid that is unknown everywhere is accepted as is (like Docker);
//     its gid then defaults to the current gid unless --group is given.
func Resolve(rootDir, userSpec, groupSpec string) (*Credential, error) {
	cred := &Credential{UID: os.Getuid(), GID: os.Getgid()}
	if userSpec == "" && groupSpec == "" {
		return cred, nil
	}

	users, err := readPasswd(filepath.Join(rootDir, "etc", "passwd"))
	if err != nil {
		return nil, err
	}
	groups, err := readGroup(filepath.Join(rootDir, "etc", "group"))
	if err != nil {
		return nil, err
	}

	if userSpec != "" {
		u, err := lookupUser(users, userSpec)
		if err != nil {
			return nil, err
		}
		cred.UID, cred.Username, cred.Home = u.UID, u.Name, u.Home
		if u.GID >= 0 {
			cred.GID = u.GID
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

	// Fall back to the host's database.
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
			return User{Name: hu.Username, UID: uid, GID: gid, Home: hu.HomeDir}, nil
		}
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
	if hg, err := osuser.LookupGroup(spec); err == nil {
		if gid, ok := parseID(hg.Gid); ok {
			return Group{Name: hg.Name, GID: gid}, nil
		}
	}
	return Group{}, fmt.Errorf("group %q not found in the container's /etc/group nor on the host", spec)
}

func readPasswd(path string) ([]User, error) {
	var users []User
	err := readColonFile(path, 7, func(f []string) {
		uid, ok1 := parseID(f[2])
		gid, ok2 := parseID(f[3])
		if f[0] != "" && ok1 && ok2 {
			users = append(users, User{Name: f[0], UID: uid, GID: gid, Home: f[5]})
		}
	})
	return users, err
}

func readGroup(path string) ([]Group, error) {
	var groups []Group
	err := readColonFile(path, 4, func(f []string) {
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

// readColonFile calls fn for every line of a passwd(5)-style file that has at
// least minFields fields. A missing file is treated as empty.
func readColonFile(path string, minFields int, fn func([]string)) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	if err := parseColonFile(f, minFields, fn); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
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
