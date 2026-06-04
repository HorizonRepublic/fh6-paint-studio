package main

// The studio exe embeds its icon, the application manifest, and the Windows version resource (Properties
// -> Details: version, company, product) from rsrc_windows_amd64.syso. That file is a build artifact
// (gitignored): the build scripts generate it from versioninfo.json via goversioninfo, so a plain
// `go build` makes an exe WITHOUT the icon/manifest/version. Use scripts/build.ps1 (dev, version 0.0.0)
// or scripts/build-release.ps1 (stamps the release version) to get a complete exe.
//
// Regenerate manually after changing the icon or versioninfo.json:
//
//	go run ./debug/cmd/png2ico cmd/studio/icon.png cmd/studio/icon.ico   # only if icon.png changed
//	powershell -File scripts/gen-winres.ps1                              # -Version x.y.z stamps a version
