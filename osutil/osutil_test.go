package osutil

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExistsFile(t *testing.T) {
	assert.False(t, ExistsFile("/paht/to/notexist"))

	tmpDir := os.TempDir()
	tmp, _ := ioutil.TempFile(tmpDir, "droot_test")
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()

	assert.True(t, ExistsFile(tmp.Name()))
	assert.False(t, ExistsFile(tmpDir))
}

func TestIsSymlink(t *testing.T) {
	tmpDir := os.TempDir()
	tmp, _ := ioutil.TempFile(tmpDir, "droot_test")
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()

	assert.False(t, IsSymlink(tmp.Name()))

	os.Symlink(tmp.Name(), tmpDir+"/symlink")

	assert.True(t, IsSymlink(tmpDir+"/symlink"))
}

func TestExistsDir(t *testing.T) {
	assert.False(t, ExistsDir("/paht/to/notexist"))

	tmpDir := os.TempDir()
	tmp, _ := ioutil.TempFile(tmpDir, "droot_test")
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()

	assert.True(t, ExistsDir(tmpDir))
	assert.False(t, ExistsDir(tmp.Name()))
}

func TestIsDirEmpty(t *testing.T) {
	ok := IsDirEmpty("/paht/to/notexist")
	assert.False(t, ok)

	tmpDir := os.TempDir()
	os.Mkdir(tmpDir+"/empty", 0755)
	os.Mkdir(tmpDir+"/noempty", 0755)
	os.Create(tmpDir + "/noempty/test")
	defer func() {
		os.Remove(tmpDir + "/empty")
		os.RemoveAll(tmpDir + "/noempty")
	}()

	ok = IsDirEmpty(tmpDir + "/empty")
	assert.True(t, ok)

	ok = IsDirEmpty(tmpDir + "/noempty")
	assert.False(t, ok)
}

func TestRunCmd(t *testing.T) {
	assert.NoError(t, RunCmd("/bin/ls"))
	assert.Error(t, RunCmd("/bin/hoge"))
}

func TestSymlink(t *testing.T) {
	tmpDir := os.TempDir()
	tmp, _ := ioutil.TempFile(tmpDir, "droot_test")
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()

	assert.NoError(t, Symlink(tmp.Name(), tmp.Name()+"/symlink"))
	assert.NoError(t, Symlink(tmp.Name(), tmp.Name()+"/symlink"), "Ignore already exist symlink file")
	os.Create(tmpDir + "/droot_dummy")
	assert.NoError(t, Symlink(tmp.Name(), tmpDir+"/droot_dummy"), "Ignore already exist file")
	os.Remove(tmp.Name() + "/symlink")
}

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
