package deploy

import (
	"fmt"
	"strings"

	"github.com/yuuki/droot/mounter"
	"github.com/yuuki/droot/osutil"
)

var RsyncDefaultOpts = []string{"-av", "--delete", "--exclude=/proc/", "--exclude=/sys/"}

func Rsync(from, to string, arg ...string) error {
	from = from + "/"
	// append "/" when not terminated by "/"
	if strings.LastIndex(to, "/") != len(to)-1 {
		to = to + "/"
	}

	rsyncArgs := []string{}
	rsyncArgs = append(rsyncArgs, RsyncDefaultOpts...)

	// Exclude bind-mounted directory by droot run
	mnt := mounter.NewMounter(to)
	mounts, err := mnt.GetMountsRoot()
	if err != nil {
		return err
	}
	for _, m := range mounts {
		mp := strings.TrimPrefix(m.Mountpoint, to)
		rsyncArgs = append(rsyncArgs, fmt.Sprintf("--exclude=/%s", mp))
	}

	rsyncArgs = append(rsyncArgs, from, to)

	if err := osutil.RunCmd("rsync", rsyncArgs...); err != nil {
		return err
	}

	return nil
}

