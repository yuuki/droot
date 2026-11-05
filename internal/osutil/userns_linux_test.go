package osutil

import (
	"strings"
	"testing"
)

func TestParseIDMap(t *testing.T) {
	ranges, err := ParseIDMap(strings.NewReader("         0       1000          1\n         1     100000      65536\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 2 || ranges[1] != (IDRange{1, 100000, 65536}) {
		t.Fatalf("got %+v", ranges)
	}
	if IsInitialIDMap(ranges) {
		t.Error("a rootless map is not the initial one")
	}
	for id, want := range map[int]bool{0: true, 1: true, 65536: true, 65537: false, -1: false} {
		if got := Mapped(ranges, id); got != want {
			t.Errorf("Mapped(%d) = %v", id, got)
		}
	}
	initial, _ := ParseIDMap(strings.NewReader("         0          0 4294967295\n"))
	if !IsInitialIDMap(initial) {
		t.Error("the initial map")
	}
	if _, err := ParseIDMap(strings.NewReader("0 x 1\n")); err == nil {
		t.Error("accepted an invalid map")
	}
}

func TestInUserNamespace(t *testing.T) {
	// The tests do not run in a user namespace, except in containers.
	ranges, err := readIDMap("/proc/self/uid_map")
	if err != nil {
		t.Skip(err)
	}
	if got, want := InUserNamespace(), !IsInitialIDMap(ranges); got != want {
		t.Errorf("InUserNamespace() = %v", got)
	}
	if !UIDMapped(0) && !InUserNamespace() {
		t.Error("uid 0 is mapped in the initial namespace")
	}
}
