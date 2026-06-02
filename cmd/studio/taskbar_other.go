//go:build !windows

package main

// taskbar is a no-op off Windows (ITaskbarList3 is Win32-only). All methods are nil-safe so callers
// need no platform guards.
type taskbar struct{}

func newTaskbar(uintptr) *taskbar              { return nil }
func (t *taskbar) set(completed, total uint64) {}
func (t *taskbar) indeterminate()              {}
func (t *taskbar) clear()                      {}
func (t *taskbar) close()                      {}
