package commands

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/url"
	"os"

	"github.com/codegangsta/cli"
	"github.com/docker/docker/pkg/fileutils"

	"github.com/yuuki1/droot/archive"
	"github.com/yuuki1/droot/aws"
	"github.com/yuuki1/droot/log"
	"github.com/yuuki1/droot/osutil"
)

var CommandArgPull = "--dest DESTINATION_DIRECTORY --src S3_ENDPOINT [--user USER] [--grpup GROUP] [--mode MODE]"
var CommandPull = cli.Command{
	Name:   "pull",
	Usage:  "Pull an extracted docker image from s3",
	Action: fatalOnError(doPull),
	Flags: []cli.Flag{
		cli.StringFlag{Name: "dest, d", Usage: "Local filesystem path (ex. /var/containers/app)"},
		cli.StringFlag{Name: "src, s", Usage: "Amazon S3 endpoint (ex. s3://drootexample/app.tar.gz)"},
		cli.StringFlag{Name: "user, u", Usage: "User (ID or name) to set after extracting archive (required superuser)"},
		cli.StringFlag{Name: "group, g", Usage: "Group (ID or name) to set after extracting archive (required superuser)"},
		cli.StringFlag{Name: "mode, m", Usage: "Mode of deployment. 'rsync' or 'symlink'. default is 'rsync'"},
	},
}

func doPull(c *cli.Context) error {
	destDir := c.String("dest")
	srcURL := c.String("src")
	if destDir == "" || srcURL == "" {
		cli.ShowCommandHelp(c, "pull")
		return errors.New("--src and --dest option required ")
	}

	s3URL, err := url.Parse(srcURL)
	if err != nil {
		return err
	}
	if s3URL.Scheme != "s3" {
		return fmt.Errorf("Not s3 scheme %s", srcURL)
	}

	mode := c.String("mode")
	if mode == "" {
		mode = "rsync"
	}
	if mode != "rsync" && mode != "symlink" {
		return fmt.Errorf("Invalid mode %s. '--mode' must be 'rsync' or 'symlink'.", mode)
	}

	uid, gid := os.Getuid(), os.Getgid()
	if group := c.String("group"); group != "" {
		if gid, err = osutil.LookupGroup(group); err != nil {
			return fmt.Errorf("Failed to lookup group: %s", err)
		}
	}
	if user := c.String("user"); user != "" {
		if uid, err = osutil.LookupUser(user); err != nil {
			return fmt.Errorf("Failed to lookup user: %s", err)
		}
	}

	tmp, err := ioutil.TempFile(os.TempDir(), "droot_gzip")
	if err != nil {
		return fmt.Errorf("Failed to create temporary file: %s", err)
	}
	defer func(f *os.File) {
		f.Close()
		os.Remove(f.Name())
	}(tmp)

	log.Info("-->", "Downloading", s3URL, "to", tmp.Name())

	if _, err := aws.NewS3Client().Download(s3URL, tmp); err != nil {
		return fmt.Errorf("Failed to download file(%s) from s3: %s", srcURL, err)
	}

	rawDir, err := ioutil.TempDir(os.TempDir(), "droot_raw")
	if err != nil {
		return fmt.Errorf("Failed to create temporary dir: %s", err)
	}
	defer os.RemoveAll(rawDir)

	log.Info("-->", "Extracting archive", tmp.Name(), "to", rawDir)

	if err := archive.ExtractTarGz(tmp, rawDir, uid, gid); err != nil {
		return fmt.Errorf("Failed to extract archive: %s", err)
	}

	if mode == "rsync" {
		log.Info("-->", "Syncing", "from", rawDir, "to", destDir)

		if err := archive.Rsync(rawDir, destDir); err != nil {
			return fmt.Errorf("Failed to rsync: %s", err)
		}
	} else if mode == "symlink" {
		if err := deployWithSymlink(rawDir, destDir); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("Unreachable code. invalid mode %s", mode)
	}

	if err := os.Lchown(destDir, uid, gid); err != nil {
		return fmt.Errorf("Failed to chown %d:%d: %s", uid, gid, err)
	}
	if err := os.Chmod(destDir, os.FileMode(0755)); err != nil {
		return fmt.Errorf("Failed to chmod %s: %s", destDir, err)
	}

	return nil
}

// Atomic deploy by symlink
// 1. rsync maindir => backupdir
// 2. mv -T backuplink destdir
// 3. rsync srcdir => maindir
// 4. mv -T mainlink destdir
func deployWithSymlink(srcDir, destDir string) error {
	mainLink := destDir + ".drootmain"
	backupLink := destDir + ".drootbackup"
	mainDir := destDir + ".d/main"
	backupDir := destDir + ".d/backup"

	for _, dir := range []string{mainDir, backupDir} {
		if err := fileutils.CreateIfNotExists(dir, true); err != nil { // mkdir -p
			return fmt.Errorf("Failed to create directory %s: %s", dir, err)
		}
	}

	// Return error if the working directory that droot internally uses exists
	for _, link := range []string{mainLink, backupLink, destDir} {
		if !osutil.IsSymlink(link) && (osutil.ExistsFile(link) || osutil.ExistsDir(link)) {
			return fmt.Errorf("%s already exists. Please use another directory as --dest option or delete %s", link, link)
		}
	}

	if err := osutil.Symlink(mainDir, mainLink); err != nil {
		return fmt.Errorf("Failed to create symlink %s: %s", mainLink, err)
	}
	if err := osutil.Symlink(backupDir, backupLink); err != nil {
		return fmt.Errorf("Failed to create symlink %s: %s", backupLink, err)
	}

	log.Info("-->", "Syncing", "from", mainDir, "to", backupDir)
	if err := archive.Rsync(mainDir, backupDir); err != nil {
		return fmt.Errorf("Failed to rsync: %s", err)
	}

	log.Info("-->", "Renaming", "from", backupLink, "to", destDir)
	if err := os.Rename(backupLink, destDir); err != nil {
		return fmt.Errorf("Failed to rename %s: %s", destDir, err)
	}

	log.Info("-->", "Syncing", "from", srcDir, "to", mainDir)
	if err := archive.Rsync(srcDir, mainDir); err != nil {
		return fmt.Errorf("Failed to rsync: %s", err)
	}

	log.Info("-->", "Renaming", "from", mainLink, "to", destDir)
	if err := os.Rename(mainLink, destDir); err != nil {
		return fmt.Errorf("Failed to rename %s: %s", destDir, err)
	}

	return nil
}

