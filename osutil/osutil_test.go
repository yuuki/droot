package osutil

import (
	"io/ioutil"
	"os"
	"testing"
)

func TestExistsFile(t *testing.T) {
	if ExistsFile("/path/to/notexist") != false {
		t.Error("ExistsFile(\"/path/to/notexist\") should be false")
	}

	tmpDir := os.TempDir()
	tmp, _ := ioutil.TempFile(tmpDir, "droot_test")
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()

	if ExistsFile(tmp.Name()) != true {
		t.Errorf("ExistsFile(\"%v\") should be true", tmp.Name())
	}
	if ExistsFile(tmpDir) != false {
		t.Errorf("ExistsFile(\"%v\") should be false", tmpDir)
	}
}

func TestIsSymlink(t *testing.T) {
	tmpDir := os.TempDir()
	tmp, _ := ioutil.TempFile(tmpDir, "droot_test")
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()

	if IsSymlink(tmp.Name()) != false {
		t.Errorf("IsSymlink(\"%v\") should be false", tmp.Name())
	}

	os.Symlink(tmp.Name(), tmpDir+"/symlink")

	if IsSymlink(tmpDir+"/symlink") != true {
		t.Errorf("IsSymlink(\"%v\") should be false", tmpDir+"/symlink")
	}
}

func TestExistsDir(t *testing.T) {
	if ExistsDir("/path/to/notexist") != false {
		t.Error("ExistsDir(\"/path/to/notexist\") should be false")
	}

	tmpDir := os.TempDir()
	tmp, _ := ioutil.TempFile(tmpDir, "droot_test")
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()

	if ExistsDir(tmpDir) != true {
		t.Errorf("ExistsDir(\"%v\") should be true", tmpDir)
	}
	if ExistsDir(tmp.Name()) != false {
		t.Errorf("ExistsDir(\"%v\") should be false", tmp.Name())
	}
}

func TestIsDirEmpty(t *testing.T) {
	if IsDirEmpty("/path/to/notexist") != false {
		t.Error("IsDirEmpty(\"/path/to/notexist\") should be false")
	}

	tmpDir := os.TempDir()
	os.Mkdir(tmpDir+"/empty", 0755)
	os.Mkdir(tmpDir+"/noempty", 0755)
	os.Create(tmpDir + "/noempty/test")
	defer func() {
		os.Remove(tmpDir + "/empty")
		os.RemoveAll(tmpDir + "/noempty")
	}()

	if IsDirEmpty(tmpDir+"/empty") != true {
		t.Errorf("IsDirEmpty(\"%v\") should be true", tmpDir+"/empty")
	}
	if IsDirEmpty(tmpDir+"/noempty") != false {
		t.Errorf("IsDirEmpty(\"%v\") should be false", tmpDir+"/noempty")
	}
}

func TestRunCmd(t *testing.T) {
	err := RunCmd("/bin/ls")
	if err != nil {
		t.Errorf("should not be error: %v", err)
	}
	err = RunCmd("/bin/hoge")
	if err == nil {
		t.Error("should be error")
	}
}

func TestSymlink(t *testing.T) {
	tmpDir := os.TempDir()
	tmp, _ := ioutil.TempFile(tmpDir, "droot_test")
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()

	err := Symlink(tmp.Name(), tmp.Name()+"/symlink")
	if err != nil {
		t.Errorf("should not be error: %v", err)
	}
	err = Symlink(tmp.Name(), tmp.Name()+"/symlink")
	if err != nil {
		t.Errorf("should not be error: %v, Ignore already exist symlink file", err)
	}
	os.Create(tmpDir + "/droot_dummy")
	err = Symlink(tmp.Name(), tmpDir+"/droot_dummy")
	if err != nil {
		t.Errorf("should not be error: %v, Ignore already exist file", err)
	}
	os.Remove(tmp.Name() + "/symlink")
}
