//go:build linux

package identity

import (
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yuuki/droot/internal/log"
	"github.com/yuuki/droot/internal/osutil"
)

// FuzzResolve checks the reading of /etc/passwd and /etc/group of the image,
// which the image's author controls, and that Resolve gives a user name the
// identity these files give it: the uid and primary gid of its first entry,
// then the groups listing it, once each.
func FuzzResolve(f *testing.F) {
	f.Add([]byte("root:x:0:0:root:/root:/bin/sh\napp:x:1500:1600:App:/home/app:/bin/sh\n"),
		[]byte("root:x:0:\napp:x:1600:\ndocker:x:1700:app,worker\naudio:x:1800: app\n"))
	f.Add([]byte("# comment\r\n+nisuser\r\n-other\r\n  app:x:1500:1600::/home/app:/bin/sh  \r\nbroken:x:notanumber:0::/:/bin/sh\n"),
		[]byte("app:x:1600:app\nwheel:x:10:,app,, app ,\ndup:x:1600:app\n"))
	f.Add([]byte("toor:x:0:0::/root:/bin/sh\napp:x:1:2:::\napp:x:3:4:::\n1500:x:7:8:::\n"),
		[]byte("toor:x:0:toor\n2:x:5:app\n"))
	f.Add([]byte("big:x:2147483647:4294967295:::\nneg:x:-1:0:::\nshort:x:1:1\n"), []byte("g:x:2147483648:big\n"))

	dir := f.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "etc"), 0o755); err != nil {
		f.Fatal(err)
	}
	root, err := osutil.OpenRoot(dir)
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { _ = root.Close() })
	log.SetOutput(io.Discard) // warnings about names mapped to uid 0
	f.Cleanup(func() { log.SetOutput(os.Stderr) })

	f.Fuzz(func(t *testing.T, passwd, group []byte) {
		for name, b := range map[string][]byte{"passwd": passwd, "group": group} {
			if err := os.WriteFile(filepath.Join(dir, "etc", name), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		users, err := readPasswd(root, "/etc/passwd")
		if err != nil {
			t.Fatalf("readPasswd: %v", err) // only lines over 1 MiB fail
		}
		groups, err := readGroup(root, "/etc/group")
		if err != nil {
			t.Fatalf("readGroup: %v", err)
		}
		for _, u := range users {
			if u.Name == "" || strings.ContainsAny(u.Name+u.Home, ":\n") ||
				u.UID < 0 || u.UID > math.MaxInt32 || u.GID < 0 || u.GID > math.MaxInt32 {
				t.Fatalf("invalid user %+v", u)
			}
		}
		for _, g := range groups {
			if g.Name == "" || strings.ContainsAny(g.Name, ":\n") || g.GID < 0 || g.GID > math.MaxInt32 {
				t.Fatalf("invalid group %+v", g)
			}
			for _, m := range g.Members {
				if m == "" || m != strings.TrimSpace(m) || strings.ContainsAny(m, ",:\n") {
					t.Fatalf("group %q: invalid member %q", g.Name, m)
				}
			}
		}

		// The first entry of a name is the one used. Numeric specs are
		// uids, not names: they are left out.
		var first []User
		for i, u := range users {
			if _, numeric := parseID(u.Name); !numeric && !slices.ContainsFunc(users[:i], func(o User) bool { return o.Name == u.Name }) {
				first = append(first, u)
			}
		}
		for _, u := range first[:min(len(first), 4)] {
			cred, err := Resolve(root, u.Name, "")
			if err != nil {
				t.Fatalf("Resolve(%q): %v", u.Name, err)
			}
			want := []int{u.GID}
			for _, g := range groups {
				if slices.Contains(g.Members, u.Name) && !slices.Contains(want, g.GID) {
					want = append(want, g.GID)
				}
			}
			if cred.UID != u.UID || cred.GID != u.GID || cred.Username != u.Name || cred.Home != u.Home || !slices.Equal(cred.Groups, want) {
				t.Fatalf("Resolve(%q) = %+v, want uid %d, gid %d, home %q, groups %v", u.Name, cred, u.UID, u.GID, u.Home, want)
			}
		}

		// --group NAME replaces the primary group with the first group of
		// that name.
		if len(first) == 0 {
			return
		}
		u := first[0]
		for i, g := range groups {
			if _, numeric := parseID(g.Name); numeric || slices.ContainsFunc(groups[:i], func(o Group) bool { return o.Name == g.Name }) {
				continue
			}
			cred, err := Resolve(root, u.Name, g.Name)
			if err != nil {
				t.Fatalf("Resolve(%q, %q): %v", u.Name, g.Name, err)
			}
			if cred.UID != u.UID || cred.GID != g.GID || cred.Groups[0] != g.GID {
				t.Fatalf("Resolve(%q, %q) = %+v, want uid %d, gid %d", u.Name, g.Name, cred, u.UID, g.GID)
			}
			break
		}
	})
}
