package mounter

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestResolveRootDir(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "real", "app")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	// An intermediate symlink: base/link -> base/real, so base/link/app is real.
	if err := os.Symlink(filepath.Join(base, "real"), filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveRootDir(filepath.Join(base, "link", "app"))
	if err != nil || got != real {
		t.Fatalf("ResolveRootDir(link/app) = %q, %v; want %q", got, err, real)
	}

	for _, bad := range []string{"/path/to/notexist", filepath.Join(base, "file"), "/", "/.."} {
		if _, err := ResolveRootDir(bad); err == nil {
			t.Errorf("ResolveRootDir(%q) should fail", bad)
		}
	}
}

const sampleMountInfo = `22 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw
36 22 0:5 / /var/containers/app/proc rw,nosuid,nodev,noexec,relatime - proc proc rw
37 22 0:20 / /var/containers/app/sys rw,relatime master:7 - sysfs sysfs rw
38 37 0:21 / /var/containers/app/sys/fs/cgroup rw,relatime master:8 - cgroup2 cgroup2 rw
39 22 8:1 /srv/log /var/containers/app/var/log\040with\040space rw,relatime - ext4 /dev/sda1 rw
40 22 0:5 / /var/containers/app2/proc rw,relatime - proc proc rw
41 22 8:1 /data /var/containers/app rw - ext4 /dev/sda1 rw
42 39 8:1 /srv/log /var/containers/app/var/log\040with\040space rw,relatime - ext4 /dev/sda1 rw
`

func TestParseMountInfo(t *testing.T) {
	mounts, err := ParseMountInfo(strings.NewReader(sampleMountInfo))
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 8 {
		t.Fatalf("got %d mounts", len(mounts))
	}
	m := mounts[4]
	if m.ID != 39 || m.Parent != 22 || m.Root != "/srv/log" || m.Mountpoint != "/var/containers/app/var/log with space" ||
		m.FSType != "ext4" || m.Source != "/dev/sda1" || m.Options != "rw,relatime" {
		t.Fatalf("unexpected entry: %+v", m)
	}

	if _, err := ParseMountInfo(strings.NewReader("garbage\n")); err == nil {
		t.Fatal("want error for malformed input")
	}
}

func TestUnescape(t *testing.T) {
	for in, want := range map[string]string{
		`/plain`:          "/plain",
		`/a\040b`:         "/a b",
		`/tab\011nl\012`:  "/tab\tnl\n",
		`/back\134slash`:  `/back\slash`,
		`/trailing\04`:    `/trailing\04`,
		`/not\8octal\999`: `/not\8octal\999`,
		`\040\040`:        "  ",
	} {
		if got := unescape(in); got != want {
			t.Errorf("unescape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMountpointsBelow(t *testing.T) {
	mounts, err := ParseMountInfo(strings.NewReader(sampleMountInfo))
	if err != nil {
		t.Fatal(err)
	}
	got := mountpointsBelow(mounts, "/var/containers/app")
	want := []string{
		"/var/containers/app/sys/fs/cgroup",
		"/var/containers/app/var/log with space", // the later (top) mount first
		"/var/containers/app/var/log with space",
		"/var/containers/app/sys",
		"/var/containers/app/proc",
		// not the root itself, although it is a mount point
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	for _, mp := range got {
		if strings.HasPrefix(mp, "/var/containers/app2") {
			t.Fatalf("sibling directory %s must not be included", mp)
		}
	}
}
