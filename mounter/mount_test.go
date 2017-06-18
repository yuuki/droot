package mounter

import (
	"testing"
)

func TestResolveRootDir(t *testing.T) {
	_, err := ResolveRootDir("/path/to/notexist")
	if err == nil {
		t.Error("should be error")
	}

	_, err = ResolveRootDir("../")
	if err != nil {
		t.Errorf("should not be error: %v", err)
	}
}
