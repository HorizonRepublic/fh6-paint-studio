//go:build !windows

package inject

// Elevated is always false off Windows (the injector is Windows-only).
func Elevated() bool { return false }

// RelaunchElevated is unsupported off Windows.
func RelaunchElevated() error { return ErrNotImplemented }
