package osutil

import (
	"errors"
)

func ChrootAndExec(keepCaps map[uint]bool, rootDir string, command ...string) error {
	return errors.New("Not support OS")
}
