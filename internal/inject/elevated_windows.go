//go:build windows

package inject

import (
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// Elevated reports whether the current process is running with an elevated (administrator)
// token. FH6 process-memory access usually requires this.
func Elevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// RelaunchElevated re-launches the current executable with the "runas" verb (triggers a UAC
// prompt) carrying the same arguments and working directory. On success the caller should exit
// so only the elevated instance remains; a cancelled UAC prompt returns an error.
func RelaunchElevated() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	exePtr, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	var argPtr *uint16
	if a := strings.Join(os.Args[1:], " "); a != "" {
		if argPtr, err = windows.UTF16PtrFromString(a); err != nil {
			return err
		}
	}
	var cwdPtr *uint16
	if cwd, err := os.Getwd(); err == nil {
		cwdPtr, _ = windows.UTF16PtrFromString(cwd)
	}
	return windows.ShellExecute(0, verb, exePtr, argPtr, cwdPtr, windows.SW_SHOWNORMAL)
}
