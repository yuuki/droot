package deploy

import (
	"fmt"
	"os"

	"github.com/docker/docker/pkg/fileutils"

	"github.com/yuuki/droot/log"
	"github.com/yuuki/droot/osutil"
)

// Atomic deploy by symlink
// 1. rsync maindir => backupdir
// 2. mv -T backuplink destdir
// 3. rsync srcdir => maindir
// 4. mv -T mainlink destdir
func DeployWithSymlink(srcDir, destDir string) error {
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
	if err := Rsync(mainDir, backupDir); err != nil {
		return fmt.Errorf("Failed to rsync: %s", err)
	}

	log.Info("-->", "Renaming", "from", backupLink, "to", destDir)
	if err := os.Rename(backupLink, destDir); err != nil {
		return fmt.Errorf("Failed to rename %s: %s", destDir, err)
	}

	log.Info("-->", "Syncing", "from", srcDir, "to", mainDir)
	if err := Rsync(srcDir, mainDir); err != nil {
		return fmt.Errorf("Failed to rsync: %s", err)
	}

	log.Info("-->", "Renaming", "from", mainLink, "to", destDir)
	if err := os.Rename(mainLink, destDir); err != nil {
		return fmt.Errorf("Failed to rename %s: %s", destDir, err)
	}

	return nil
}

func CleanupSymlink(destDir string) error {
	mainLink := destDir + ".drootmain"
	backupLink := destDir + ".drootbackup"
	contentDir := destDir + ".d/"

	log.Info("-->", "Removing", backupLink)
	if err := osutil.RunCmd("rm", "-f", backupLink); err != nil {
		return err
	}
	log.Info("-->", "Removing", mainLink)
	if err := osutil.RunCmd("rm", "-f", mainLink); err != nil {
		return err
	}
	log.Info("-->", "Removing", contentDir)
	if err := osutil.RunCmd("rm", "-fr", contentDir); err != nil {
		return err
	}

	return osutil.RunCmd("rm", "-f", destDir)
}
