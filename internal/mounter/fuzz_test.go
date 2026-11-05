package mounter

import (
	"fmt"
	"strings"
	"testing"
)

// FuzzParseMountInfo checks that ParseMountInfo never panics, and that it
// returns one entry per non-empty line unless it reports an error.
func FuzzParseMountInfo(f *testing.F) {
	f.Add(sampleMountInfo)
	f.Add("garbage\n")
	f.Add("1 2 3 4 5 6 - 7\n")
	f.Add("1 2 3 4 5 6 - 7 8\r\n\r\n36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw,errors=continue")
	f.Add("x y 0:1 / / rw - a b c\n")
	f.Add("1 2 0:1 /\\040\\011\\012\\134\\777\\08 /\\ rw - - - -\n")
	f.Fuzz(func(t *testing.T, data string) {
		// unescape never makes a string longer, and decodes what is fully
		// escaped.
		if u := unescape(data); len(u) > len(data) {
			t.Fatalf("unescape(%q) = %q is longer", data, u)
		}
		if got := unescape(escapeOctal(data, func(byte) bool { return true })); got != data {
			t.Fatalf("unescape(escape(%q)) = %q", data, got)
		}

		mounts, err := ParseMountInfo(strings.NewReader(data))
		if err != nil {
			return
		}
		lines := 0
		for line := range strings.SplitSeq(data, "\n") {
			if strings.TrimSuffix(line, "\r") != "" {
				lines++
			}
		}
		if len(mounts) != lines {
			t.Fatalf("%d entries for %d non-empty lines", len(mounts), lines)
		}
	})
}

// kernelEscape escapes s like the kernel does in /proc/<pid>/mountinfo
// (mangle() in fs/proc_namespace.c): space, tab, newline and backslash.
func kernelEscape(s string) string {
	return escapeOctal(s, func(c byte) bool { return strings.IndexByte(" \t\n\\", c) >= 0 })
}

func escapeOctal(s string, escape func(byte) bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if escape(s[i]) {
			fmt.Fprintf(&b, `\%03o`, s[i])
		} else {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// FuzzParseMountInfoLine checks that a line formatted like the kernel does
// is parsed back to its fields.
func FuzzParseMountInfoLine(f *testing.F) {
	f.Add(uint32(36), uint32(35), uint32(98<<16), "/mnt1", "/mnt2", uint8(1), "ext3", "/dev/root")
	f.Add(uint32(39), uint32(22), uint32(8<<16|1), "/srv/log", "/var/containers/app/var/log with space", uint8(0), "ext4", "/dev/sda1")
	f.Add(uint32(1), uint32(0), uint32(0), "/", "/a\tb\nc\\d", uint8(15), "fuse.sshfs", "user@host:/path with spaces")
	f.Add(uint32(2), uint32(1), uint32(5), "/x\\040y", "/ - /", uint8(6), "tmpfs", "-")
	f.Add(uint32(0x7fffffff), uint32(0x7fffffff), uint32(0xffffffff), "/\xff\xfe", "/é", uint8(2), "overlay", "none")
	f.Fuzz(func(t *testing.T, id, parent, dev uint32, root, mountpoint string, opt uint8, fstype, source string) {
		// The kernel prints C strings: no NUL. Paths are absolute and the
		// file system type is never empty, but the source can be (e.g.
		// `mount -t tmpfs "" /mnt`). Only space, tab, newline and backslash
		// are escaped: other whitespace (\v, \f, \r, U+00A0...) is not.
		if strings.Contains(root+mountpoint+fstype+source, "\x00") || fstype == "" {
			return
		}
		if !strings.HasPrefix(root, "/") {
			root = "/" + root
		}
		if !strings.HasPrefix(mountpoint, "/") {
			mountpoint = "/" + mountpoint
		}
		want := MountInfo{
			ID:         int(id & 0x7fffffff),
			Parent:     int(parent & 0x7fffffff),
			Dev:        fmt.Sprintf("%d:%d", dev>>16, dev&0xffff),
			Root:       root,
			Mountpoint: mountpoint,
			Options:    "rw,relatime",
			FSType:     fstype,
			Source:     source,
		}
		if opt&16 != 0 {
			want.Options = "ro,nosuid,nodev,noexec"
		}
		var optional []string
		for i, field := range []string{"shared:%d", "master:%d", "propagate_from:%d", "unbindable"} {
			if opt&(1<<i) != 0 {
				if strings.Contains(field, "%d") {
					field = fmt.Sprintf(field, int(opt)+i)
				}
				optional = append(optional, field)
			}
		}
		want.Optional = strings.Join(optional, " ")

		line := fmt.Sprintf("%d %d %s %s %s %s", want.ID, want.Parent, want.Dev,
			kernelEscape(root), kernelEscape(mountpoint), want.Options)
		for _, o := range optional {
			line += " " + o
		}
		line += " - " + kernelEscape(fstype) + " " + kernelEscape(source) + " rw,errors=continue"

		input := "22 1 8:1 / / rw shared:1 - ext4 /dev/sda1 rw\n" + line + "\n"
		mounts, err := ParseMountInfo(strings.NewReader(input))
		if err != nil {
			t.Fatalf("ParseMountInfo(%q): %v", line, err)
		}
		if len(mounts) != 2 {
			t.Fatalf("ParseMountInfo(%q) returned %d entries, want 2", line, len(mounts))
		}
		if got := mounts[1]; got != want {
			t.Fatalf("ParseMountInfo(%q):\n got  %#v\n want %#v", line, got, want)
		}
	})
}
