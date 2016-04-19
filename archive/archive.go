package archive

import (
	"bufio"
	"compress/gzip"
	"io"

	"github.com/docker/docker/pkg/archive"

	"github.com/yuuki/droot/errwrap"
)

const compressionBufSize = 32768

func Untar(in io.Reader, dest string, sameOwner bool) error {
	return archive.Untar(in, dest, &archive.TarOptions{
		NoLchown:        !sameOwner,
		ExcludePatterns: []string{"dev/"}, // prevent 'operation not permitted'
	})
}

func ExtractTarGz(in io.Reader, dest string, sameOwner bool) error {
	return archive.Untar(in, dest, &archive.TarOptions{
		Compression:     archive.Gzip,
		NoLchown:        !sameOwner,
		ExcludePatterns: []string{"dev/"}, // prevent 'operation not permitted'
	})
}

func Compress(in io.Reader) io.ReadCloser {
	pReader, pWriter := io.Pipe()
	bufWriter := bufio.NewWriterSize(pWriter, compressionBufSize)
	compressor := gzip.NewWriter(bufWriter)

	go func() {
		_, err := io.Copy(compressor, in)
		if err == nil {
			err = compressor.Close()
		}
		if err == nil {
			err = bufWriter.Flush()
		}
		if err != nil {
			pWriter.CloseWithError(errwrap.Wrapf(err, "Failed to compress: {{err}}"))
		} else {
			pWriter.Close()
		}
	}()

	return pReader
}
