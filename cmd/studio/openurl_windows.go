//go:build windows

package main

import "golang.org/x/sys/windows"

// openURL opens url in the default browser (best-effort).
//
// ShellExecute directly, rather than spawning `rundll32 url.dll,FileProtocolHandler`. The rundll32
// form is the older recipe and it works, but launching rundll32 with an argument is also how a whole
// family of malware proxies its execution (MITRE T1218.011) — it is on the shortlist of behaviours
// endpoint tooling watches for. Opening a link is not worth looking like that, and this way there is
// no child process at all.
func openURL(url string) {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return
	}
	target, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return
	}
	_ = windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}
