package commands

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yuuki/droot/internal/osutil"
)

// compressions are the values of droot export --compress.
var compressions = []string{"gzip", "zstd"}

// compressor wraps w with the compression algo ("" for none). finish
// flushes the compressed stream; abort releases the resources after an
// error.
type compressor struct {
	io.Writer
	finish func() error
	abort  func()
}

func newCompressor(ctx context.Context, w io.Writer, algo string) (*compressor, error) {
	switch algo {
	case "":
		return &compressor{Writer: w, finish: func() error { return nil }, abort: func() {}}, nil
	case "gzip":
		gz := gzip.NewWriter(w) // no name nor time in the header: reproducible
		return &compressor{Writer: gz, finish: gz.Close, abort: func() {}}, nil
	case "zstd":
		// zstd is not in the standard library: use the zstd command rather
		// than adding a dependency.
		path, err := exec.LookPath("zstd")
		if err != nil {
			return nil, errors.New("--compress zstd needs the zstd command, which was not found in PATH")
		}
		cmd := exec.CommandContext(ctx, path, "-q", "-c", "-T0")
		cmd.Stdout = w
		cmd.Stderr = os.Stderr
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("start zstd: %w", err)
		}
		return &compressor{
			Writer: stdin,
			finish: func() error {
				if err := stdin.Close(); err != nil {
					return err
				}
				if err := cmd.Wait(); err != nil {
					return fmt.Errorf("zstd: %w", err)
				}
				return nil
			},
			abort: func() {
				_ = stdin.Close()
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			},
		}, nil
	}
	return nil, fmt.Errorf("unknown compression %q (want gzip or zstd)", algo)
}

// progress counts the bytes written through it and, if out is a terminal,
// shows them on a status line.
type progress struct {
	w     io.Writer
	n     atomic.Int64
	start time.Time
	label string
	out   *os.File
	stop  chan struct{}
	done  sync.WaitGroup
}

func newProgress(w io.Writer, label string, out *os.File) *progress {
	p := &progress{w: w, start: time.Now(), label: label}
	if out != nil && osutil.IsTerminal(out) {
		p.out, p.stop = out, make(chan struct{})
		p.done.Add(1)
		go func() {
			defer p.done.Done()
			t := time.NewTicker(500 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-p.stop:
					return
				case <-t.C:
					fmt.Fprintf(p.out, "\r\033[K%s: %s", p.label, humanBytes(p.n.Load()))
				}
			}
		}()
	}
	return p
}

func (p *progress) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.n.Add(int64(n))
	return n, err
}

// end stops the status line, replacing it with a summary if ok.
func (p *progress) end(ok bool) {
	if p.out == nil {
		return
	}
	close(p.stop)
	p.done.Wait()
	fmt.Fprint(p.out, "\r\033[K")
	if ok {
		fmt.Fprintf(p.out, "%s: %s in %s\n", p.label, humanBytes(p.n.Load()), time.Since(p.start).Round(100*time.Millisecond))
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
