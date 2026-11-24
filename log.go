package runnel

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

// Logger writes to the log of the run: Runnel reads stderr of the plugin line by line and
// takes DEBUG, INFO, WARN or ERROR at the start of a line as its level. A message of several
// lines becomes one line; lines are cut at 60 KiB (Runnel keeps 64 KiB). A Logger may be
// used by several goroutines. Secrets that Runnel passed to the plugin are masked by Runnel,
// but should not be logged in the first place.
type Logger struct {
	mu *sync.Mutex
	w  io.Writer
}

const maxLogLine = 60 << 10

// NewLogger returns a Logger that writes to w, e.g. os.Stderr.
func NewLogger(w io.Writer) *Logger { return &Logger{mu: &sync.Mutex{}, w: w} }

// Debug logs a message with level debug (shown with runnel run -v).
func (l *Logger) Debug(format string, args ...any) { l.write("DEBUG", format, args) }

// Info logs a message with level info.
func (l *Logger) Info(format string, args ...any) { l.write("INFO", format, args) }

// Warn logs a message with level warn.
func (l *Logger) Warn(format string, args ...any) { l.write("WARN", format, args) }

// Error logs a message with level error. It does not fail the action; return an error from
// Run for that.
func (l *Logger) Error(format string, args ...any) { l.write("ERROR", format, args) }

func (l *Logger) write(level, format string, args []any) {
	if l == nil || l.w == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	msg = strings.NewReplacer("\r\n", " | ", "\n", " | ", "\r", " | ").Replace(msg)
	if len(msg) > maxLogLine {
		msg = strings.ToValidUTF8(msg[:maxLogLine], "") + "…"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.w, level+" "+msg+"\n")
}
