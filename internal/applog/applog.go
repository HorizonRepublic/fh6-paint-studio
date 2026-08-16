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
	"sync"
	"time"
)

// mu guards logger/file: the studio's worker + update goroutines may Printf while main defers Close,
// and Close nil-ing the file under a concurrent Printf is otherwise a data race.
var (
	mu     sync.Mutex
	logger *log.Logger
	file   *os.File
)

// Init opens <logdir>/name for appending and tees output to stderr + the file.
// logdir is the executable's directory (or the cwd when running via `go run`,
// where the binary lives in a temp dir). Returns the resolved log path.
func Init(name string) string {
	dir := logDir()
	path := filepath.Join(dir, name)
	// O_APPEND for the life of the install grows without bound; a >20MB log is nobody's evidence.
	// Start fresh past that — the interesting lines are always the current session's.
	if st, err := os.Stat(path); err == nil && st.Size() > 20<<20 {
		_ = os.Remove(path)
	}
	var w io.Writer = os.Stderr
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err == nil {
		w = io.MultiWriter(os.Stderr, f)
	} else {
		fmt.Fprintf(os.Stderr, "applog: could not open %s: %v (logging to stderr only)\n", path, err)
	}
	mu.Lock()
	file = f // under mu, as the doc comment promises (Close reads it under the same lock)
	logger = log.New(w, "", log.LstdFlags|log.Lmicroseconds)
	mu.Unlock()
	Printf("==== session start %s (pid %d) ====", time.Now().Format(time.RFC3339), os.Getpid())
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
	mu.Lock()
	l := logger
	mu.Unlock()
	if l != nil {
		l.Printf(format, args...)
		return
	}
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// Close flushes and closes the log file. Safe to call multiple times.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		file.Close()
		file = nil
	}
	logger = nil // post-close Printf falls back to stderr instead of writing the closed file's MultiWriter
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
