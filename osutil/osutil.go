package osutil

import (
	"compress/gzip"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"

	"github.com/yuuki1/dochroot/log"
)

var RsyncDefaultOpts = []string{"-av", "--delete"}

func ExistsDir(dir string) bool {
	if f, err := os.Stat(dir); os.IsNotExist(err) || ! f.IsDir() {
		return false
	}
	return true
}

func RunCmd(name string, arg ...string) error {
	out, err := exec.Command(name, arg...).CombinedOutput()
	if len(out) > 0 {
		log.Debug(string(out))
	}
	if err != nil {
		log.Errorf("failed: %s %s", name, arg)
		return err
	}
	log.Debug("runcmd: ", name, arg)
	return nil
}

func Gzip(destWriter io.Writer, srcReader io.Reader) (error) {
	w := gzip.NewWriter(destWriter)
	defer w.Close()

	bytes, err := ioutil.ReadAll(srcReader)
	if err != nil {
		return err
	}

	nBytes, err := w.Write(bytes)
	if err != nil {
		return err
	}
	log.Debug("gzip bytes", nBytes)

	return nil
}

func ExtractTarGz(filePath string) error {
	if err := RunCmd("tar", "xf", filePath); err != nil {
		return err
	}

	if err := os.Chmod(filePath, os.FileMode(0755)); err != nil {
		return err
	}

	return nil
}

func Rsync(from, to string, arg ...string) error {
	from = from + "/"
	// append "/" when not terminated by "/"
	if strings.LastIndex(to, "/") != len(to)-1 {
		to = to + "/"
	}

	// TODO --exclude, --excluded-from
	rsyncArgs := []string{}
	rsyncArgs = append(rsyncArgs, RsyncDefaultOpts...)
	rsyncArgs = append(arg, from, to)
	if err := RunCmd("rsync", rsyncArgs...); err != nil {
		return err
	}

	return nil
}

