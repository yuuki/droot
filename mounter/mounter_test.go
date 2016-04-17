package mounter

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveRootDir(t *testing.T) {
	_, err := ResolveRootDir("/path/to/notexist")
	assert.Error(t, err)

	_, err = ResolveRootDir("../")
	assert.NoError(t, err)
}
