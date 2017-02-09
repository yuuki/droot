// Modified from the original golang os/user package (2015-09-30)

// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package group allows group lookups by name or id.

package group

import (
	"strconv"
)

var implemented = true // set to false by lookup_stubs.go's init

// Group represents a user group.
//
// On posix systems Gid contains a decimal number representing gid.
type Group struct {
	Gid       string
	Groupname string
}

// UnknownGroupIdError is returned by LookupId when
// a group cannot be found.
type UnknownGroupIdError int

func (e UnknownGroupIdError) Error() string {
	return "group: unknown groupid " + strconv.Itoa(int(e))
}

// UnknownGroupError is returned by Lookup when
// a group cannot be found.
type UnknownGroupError string

func (e UnknownGroupError) Error() string {
	return "group: unknown group " + string(e)
}
