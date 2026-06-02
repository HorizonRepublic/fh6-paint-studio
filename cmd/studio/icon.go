package main

// The studio executable embeds its icon (used by the window title bar, the taskbar button, and
// Explorer) together with the application manifest from rsrc_windows_amd64.syso, which the Go
// toolchain links into the main package automatically on windows/amd64.
//
// To regenerate the resource after changing icon.png:
//
//	go install github.com/akavel/rsrc@latest          # once
//	go run ./debug/cmd/png2ico cmd/studio/icon.png cmd/studio/icon.ico
//	rsrc -ico cmd/studio/icon.ico -manifest cmd/studio/studio.manifest -o cmd/studio/rsrc_windows_amd64.syso
//
// (png2ico packs the 1024px source into a multi-size .ico; any PNG→ICO converter that emits
// 16..256 px entries works equally well.)
