package environ

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMergeEnviron(t *testing.T) {
	{
		e1 := []string{"EDITOR=vim", "LANG=ja_JP.UTF-8", "USER=yuuki"}
		e2 := []string{"PAGER=less", "PATH=/bin:/usr/bin"}

		env, err := MergeEnviron(e1, e2)

		assert.NoError(t, err)
		assert.Equal(t, append(e1, e2...), env)
	}

	{
		e1 := []string{"LANG=ja_JP.UTF-8", "EDITOR=vim", "USER=yuuki"}
		e2 := []string{"EDITOR=emacs", "PATH=/bin:/usr/bin"}

		env, err := MergeEnviron(e1, e2)

		assert.NoError(t, err)
		assert.Equal(t, []string{"LANG=ja_JP.UTF-8", "EDITOR=emacs", "USER=yuuki", "PATH=/bin:/usr/bin"}, env)
	}
}
