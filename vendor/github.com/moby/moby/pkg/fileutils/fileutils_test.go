package fileutils

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

// Apache License 2.0 https://github.com/moby/moby/blob/master/LICENSE
// Copyright 2013-2017 Docker, Inc.
// https://github.com/moby/moby/blob/89658bed6/pkg/fileutils/fileutils_test.go#L455
func TestCreateIfNotExistsDir(t *testing.T) {
	tempFolder, err := ioutil.TempDir("", "docker-fileutils-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempFolder)

	folderToCreate := filepath.Join(tempFolder, "tocreate")

	if err := CreateIfNotExists(folderToCreate, true); err != nil {
		t.Fatal(err)
	}
	fileinfo, err := os.Stat(folderToCreate)
	if err != nil {
		t.Fatalf("Should have create a folder, got %v", err)
	}

	if !fileinfo.IsDir() {
		t.Fatalf("Should have been a dir, seems it's not")
	}
}

// Apache License 2.0 https://github.com/moby/moby/blob/master/LICENSE
// Copyright 2013-2017 Docker, Inc.
// https://github.com/moby/moby/blob/89658bed6/pkg/fileutils/fileutils_test.go#L477
func TestCreateIfNotExistsFile(t *testing.T) {
	tempFolder, err := ioutil.TempDir("", "docker-fileutils-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempFolder)

	fileToCreate := filepath.Join(tempFolder, "file/to/create")

	if err := CreateIfNotExists(fileToCreate, false); err != nil {
		t.Fatal(err)
	}
	fileinfo, err := os.Stat(fileToCreate)
	if err != nil {
		t.Fatalf("Should have create a file, got %v", err)
	}

	if fileinfo.IsDir() {
		t.Fatalf("Should have been a file, seems it's not")
	}
}
