package archive

import (
	"bufio"
	"compress/gzip"
	"io"

	"github.com/docker/docker/pkg/archive"

	"github.com/yuuki/droot/errwrap"
)

func Untar(in io.Reader, dest string, sameOwner bool) error {
	return archive.Untar(in, dest, &archive.TarOptions{
		NoLchown:        !sameOwner,
		ExcludePatterns: []string{"dev/"}, // prevent 'operation not permitted'
	})
}
