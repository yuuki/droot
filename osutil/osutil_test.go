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
	tmp, _ := ioutil.TempFile(tmpDir, "")
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
		os.Remove(tmpDir)
	}()

	assert.True(t, ExistsFile(tmp.Name()))
	assert.False(t, ExistsFile(tmpDir))
}

