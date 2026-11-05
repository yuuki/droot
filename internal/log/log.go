// Package log is the leveled logger of droot.
//
// Messages are written to stderr so that they never mix with data written to
// stdout (for example the tar stream produced by `droot export`). They are
// plain text ("warning: ...") by default, or JSON objects, one per line,
// written with log/slog, for log collectors (SetFormat).
package log

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	debug    atomic.Bool
	quiet    atomic.Bool
	jsonMode atomic.Bool

	mu  sync.Mutex
	out io.Writer = os.Stderr
)

// Formats lists the values accepted by SetFormat.
var Formats = []string{"text", "json"}

// SetDebug enables or disables debug output.
func SetDebug(enabled bool) { debug.Store(enabled) }

// IsDebug reports whether debug output is enabled.
func IsDebug() bool { return debug.Load() }

// SetQuiet hides informational messages and warnings; errors (and debug
// messages, if enabled) are still printed.
func SetQuiet(enabled bool) { quiet.Store(enabled) }

// IsQuiet reports whether SetQuiet was enabled.
func IsQuiet() bool { return quiet.Load() }

// SetFormat selects the format of the messages: "text" (or "") or "json".
func SetFormat(format string) error {
	switch format {
	case "", "text":
		jsonMode.Store(false)
	case "json":
		jsonMode.Store(true)
	default:
		return fmt.Errorf("unknown log format %q: want %s", format, strings.Join(Formats, " or "))
	}
	return nil
}

// IsJSON reports whether messages are written as JSON.
func IsJSON() bool { return jsonMode.Load() }

// SetOutput changes the destination of log messages. It is meant for tests.
func SetOutput(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	out = w
}

func printf(level slog.Level, prefix, format string, v ...any) {
	msg := fmt.Sprintf(format, v...)
	mu.Lock()
	defer mu.Unlock()
	if jsonMode.Load() {
		h := slog.NewJSONHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug})
		slog.New(h).Log(context.Background(), level, strings.TrimSuffix(msg, "\n"))
		return
	}
	if len(msg) == 0 || msg[len(msg)-1] != '\n' {
		msg += "\n"
	}
	_, _ = io.WriteString(out, prefix+msg)
}

// Debugf prints a debug message when debug output is enabled.
func Debugf(format string, v ...any) {
	if IsDebug() {
		printf(slog.LevelDebug, "[debug] ", format, v...)
	}
}

// Infof prints an informational message, unless quiet.
func Infof(format string, v ...any) {
	if !IsQuiet() {
		printf(slog.LevelInfo, "", format, v...)
	}
}

// Warnf prints a warning message, unless quiet.
func Warnf(format string, v ...any) {
	if !IsQuiet() {
		printf(slog.LevelWarn, "warning: ", format, v...)
	}
}

// Errorf prints an error message: "droot: ..." in text.
func Errorf(format string, v ...any) { printf(slog.LevelError, "droot: ", format, v...) }
