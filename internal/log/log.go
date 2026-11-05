// Package log is a tiny leveled logger used by droot.
//
// Messages are written to stderr so that they never mix with data written to
// stdout (for example the tar stream produced by `droot export`).
package log

import (
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

var (
	debug atomic.Bool

	mu  sync.Mutex
	out io.Writer = os.Stderr
)

// SetDebug enables or disables debug output.
func SetDebug(enabled bool) { debug.Store(enabled) }

// IsDebug reports whether debug output is enabled.
func IsDebug() bool { return debug.Load() }

// SetOutput changes the destination of log messages. It is meant for tests.
func SetOutput(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	out = w
}

func printf(prefix, format string, v ...any) {
	msg := fmt.Sprintf(format, v...)
	if len(msg) == 0 || msg[len(msg)-1] != '\n' {
		msg += "\n"
	}
	mu.Lock()
	defer mu.Unlock()
	_, _ = io.WriteString(out, prefix+msg)
}

// Debugf prints a debug message when debug output is enabled.
func Debugf(format string, v ...any) {
	if IsDebug() {
		printf("[debug] ", format, v...)
	}
}

// Infof prints an informational message.
func Infof(format string, v ...any) { printf("", format, v...) }

// Warnf prints a warning message.
func Warnf(format string, v ...any) { printf("warning: ", format, v...) }
