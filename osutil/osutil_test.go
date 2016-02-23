package osutil

import (
	"io/ioutil"
	"os"
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
		os.Remove(tmpDir)
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
		os.Remove(tmpDir)
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
		os.Remove(tmpDir)
	}()

	assert.True(t, ExistsDir(tmpDir))
	assert.False(t, ExistsDir(tmp.Name()))
}

