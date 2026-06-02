// Package applog provides simple file+stderr logging that drops a log next to
// the executable, captures panics with a stack trace, and is safe to call
// before Init (falls back to stderr).
package applog

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

var (
	logger *log.Logger
	file   *os.File
)

// Init opens <logdir>/name for appending and tees output to stderr + the file.
// logdir is the executable's directory (or the cwd when running via `go run`,
// where the binary lives in a temp dir). Returns the resolved log path.
func Init(name string) string {
	dir := logDir()
	path := filepath.Join(dir, name)
	var w io.Writer = os.Stderr
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		file = f
		w = io.MultiWriter(os.Stderr, f)
	} else {
		fmt.Fprintf(os.Stderr, "applog: could not open %s: %v (logging to stderr only)\n", path, err)
	}
	logger = log.New(w, "", log.LstdFlags|log.Lmicroseconds)
	logger.Printf("==== session start %s (pid %d) ====", time.Now().Format(time.RFC3339), os.Getpid())
	return path
}

func logDir() string {
	if exe, err := os.Executable(); err == nil {
		d := filepath.Dir(exe)
		if !strings.HasPrefix(d, os.TempDir()) {
			return d
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

// Printf logs a formatted line to the file + stderr (or stderr only pre-Init).
func Printf(format string, args ...any) {
	if logger != nil {
		logger.Printf(format, args...)
		return
	}
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// Close flushes and closes the log file. Safe to call multiple times.
func Close() {
	if file != nil {
		file.Close()
		file = nil
	}
}

// Recover logs a panic with its stack trace, then exits non-zero. Use as the
// first deferred call in main so crashes always leave a trace in the log file.
func Recover() {
	if r := recover(); r != nil {
		Printf("PANIC: %v\n%s", r, debug.Stack())
		Close()
		os.Exit(1)
	}
}
