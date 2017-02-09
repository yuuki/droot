// Modified from the original golang os/user package (2015-09-30)

// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package group

import (
	"testing"
)

func check(t *testing.T) {
	if !implemented {
		t.Skip("group: not implemented; skipping tests")
	}
}

func TestCurrent(t *testing.T) {
	check(t)

	g, err := Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if g.Groupname == "" {
		t.Fatalf("didn't get a groupname")
	}
}

func compare(t *testing.T, want, got *Group) {
	if want.Groupname != got.Groupname {
		t.Errorf("got Groupname=%q; want %q", got.Groupname, want.Groupname)
	}
	if want.Gid != got.Gid {
		t.Errorf("got Gid=%q; want %q", got.Gid, want.Gid)
	}
}

func TestLookup(t *testing.T) {
	check(t)

	want, err := Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	got, err := Lookup(want.Groupname)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	compare(t, want, got)
}

func TestLookupId(t *testing.T) {
	check(t)

	want, err := Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	got, err := LookupId(want.Gid)
	if err != nil {
		t.Fatalf("LookupId: %v", err)
	}
	compare(t, want, got)
}
