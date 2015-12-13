package errwrap

import (
	"fmt"

	"github.com/hashicorp/errwrap"
)

func Wrapf(err error, format string) error {
	return errwrap.Wrapf(format, err)
}

func Wrapff(err error, format string, v ...interface{}) error {
	format = fmt.Sprintf(format, v)
	return errwrap.Wrapf(format, err)
}
