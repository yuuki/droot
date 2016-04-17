package deploy

import (
	"strings"

	"github.com/yuuki/droot/osutil"
)

var RsyncDefaultOpts = []string{"-av", "--delete"}

func Rsync(from, to string, arg ...string) error {
	from = from + "/"
	// append "/" when not terminated by "/"
	if strings.LastIndex(to, "/") != len(to)-1 {
		to = to + "/"
	}

	// TODO --exclude, --excluded-from
	rsyncArgs := []string{}
	rsyncArgs = append(rsyncArgs, RsyncDefaultOpts...)
	rsyncArgs = append(rsyncArgs, from, to)
	if err := osutil.RunCmd("rsync", rsyncArgs...); err != nil {
		return err
	}

	return nil
}

