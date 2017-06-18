package fileutils

import (
	"os"
	"path/filepath"
)

// CreateIfNotExists creates a file or a directory only if it does not already exist.
//
// Apache License 2.0 https://github.com/moby/moby/blob/master/LICENSE
// Copyright 2013-2017 Docker, Inc.
// https://github.com/moby/moby/blob/89658bed6/pkg/fileutils/fileutils.go#L281
func CreateIfNotExists(path string, isDir bool) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			if isDir {
				return os.MkdirAll(path, 0755)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(path, os.O_CREATE, 0755)
			if err != nil {
				return err
			}
			f.Close()
		}
	}
	return nil
}
