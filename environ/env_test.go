package environ

import (
	"testing"

	"github.com/kylelemons/godebug/pretty"
)

func TestGetEnvironFromEnvFile(t *testing.T) {
	env, err := GetEnvironFromEnvFile("../testdata/drootenv")
	if err != nil {
		t.Errorf("should not be error: %v", err)
	}
	expected := []string{
		"HOME=/root",
		"GOLANG_DOWNLOAD_SHA256=5470eac05d273c74ff8bac7bef5bad0b5abbd1c4052efbdbc8db45332e836b0b",
		"PATH=/go/bin:/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"GOPATH=/go",
		"PWD=/go",
		"GOLANG_DOWNLOAD_URL=https://golang.org/dl/go1.6.linux-amd64.tar.gz",
		"GOLANG_VERSION=1.6",
	}
	if diff := pretty.Compare(env, expected); diff != "" {
		t.Fatalf("diff: (-actual +expected)\n%s", diff)
	}
}

func TestMergeEnviron(t *testing.T) {
	{
		e1 := []string{"EDITOR=vim", "LANG=ja_JP.UTF-8", "USER=yuuki"}
		e2 := []string{"PAGER=less", "PATH=/bin:/usr/bin"}

		env, err := MergeEnviron(e1, e2)

		if err != nil {
			t.Errorf("should not be error: %v", err)
		}
		expected := append(e1, e2...)
		if diff := pretty.Compare(env, expected); diff != "" {
			t.Fatalf("diff: (-actual +expected)\n%s", diff)
		}
	}

	{
		e1 := []string{"LANG=ja_JP.UTF-8", "EDITOR=vim", "USER=yuuki"}
		e2 := []string{"EDITOR=emacs", "PATH=/bin:/usr/bin"}

		env, err := MergeEnviron(e1, e2)

		if err != nil {
			t.Errorf("should not be error: %v", err)
		}
		expected := []string{"LANG=ja_JP.UTF-8", "EDITOR=emacs", "USER=yuuki", "PATH=/bin:/usr/bin"}
		if diff := pretty.Compare(env, expected); diff != "" {
			t.Fatalf("diff: (-actual +expected)\n%s", diff)
		}
	}
}
