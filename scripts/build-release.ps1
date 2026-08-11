# build-release.ps1 -- cut the DISTRIBUTABLE release: the GUI app + the Vulkan shim. TWO files.
#
#   powershell -ExecutionPolicy Bypass -File .\scripts\build-release.ps1 [-Out <dir>] [-Version <x.y.z>] [-SkipDLL]
#
# Vulkan is the one supported backend (owner decision 2026-08-03), so the release no longer carries
#
# Windows-optimised flags: stripped symbols (-s -w) + -trimpath, the GUI linked -H windowsgui (NO
# console window), CGO_ENABLED=0 (the DLL is loaded via syscall, no cgo), GOAMD64=v1 (max CPU compat).
#
# -Version stamps the Windows version resource (Properties -> Details: version, company, product) via
# goversioninfo + cmd/studio/versioninfo.json; CI passes the release tag, a local build defaults to 0.0.0.
#
# The Vulkan DLL is built from the toolchain in third_party\vulkan (scripts\setup-vulkan-ci.ps1
# assembles it on CI). -SkipDLL reuses the existing DLL (shim unchanged).
param(
    [string]$Out = "bin",
    [string]$Version = "0.0.0",
    [switch]$SkipDLL
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot; if (-not $root) { $root = (Get-Location).Path }
if (-not [System.IO.Path]::IsPathRooted($Out)) { $Out = Join-Path $root $Out }
$vkdll = Join-Path $root "bin\fh6vk.dll"
Push-Location $root
try {
    # 1) Cross-vendor Vulkan shim.
    if ($SkipDLL -and (Test-Path $vkdll)) {
        Write-Host "Reusing existing shim: $vkdll" -ForegroundColor Yellow
    } else {
        & (Join-Path $PSScriptRoot 'build-vulkan.ps1')
        if ($LASTEXITCODE -ne 0) { throw "Vulkan shim build failed" }
    }

    # 2) Clean the output to exactly the release files. Pattern-based, not a name list: bin\ doubles
    # as the dev scratch dir, so it accumulates profiling/PGO/A-B exes and shim link byproducts that
    # a fixed list keeps missing — and anything left here looks like part of the release.
    New-Item -ItemType Directory -Force -Path $Out | Out-Null
    Remove-Item (Join-Path $Out 'fh6paint*.exe'),                 (Join-Path $Out 'fh6vk.exp'), (Join-Path $Out 'fh6vk.lib') -ErrorAction SilentlyContinue

    # 3) Stamp the studio's version resource from -Version; verNum also feeds the binary version (-X).
    $verNum = (($Version -replace '^v', '') -split '-')[0]
    & (Join-Path $PSScriptRoot 'gen-winres.ps1') -Version $verNum

    # 4) GUI app, Vulkan build.
    $env:CGO_ENABLED = '0'
    $env:GOAMD64 = 'v1'
    & go build -tags vulkan -trimpath -ldflags "-s -w -H windowsgui -X main.version=$verNum" -o (Join-Path $Out 'fh6-paint-studio.exe') ./cmd/studio
    if ($LASTEXITCODE -ne 0) { throw "studio build failed" }
    if ($vkdll -ne (Join-Path $Out 'fh6vk.dll')) { Copy-Item $vkdll (Join-Path $Out 'fh6vk.dll') -Force }
    # Drop build byproducts (a .exe~ backup left when the previous exe is/was locked) + any runtime log.
    Remove-Item (Join-Path $Out '*.exe~'), (Join-Path $Out '*.log') -ErrorAction SilentlyContinue

    Write-Host "Release ready in $Out (2 files):" -ForegroundColor Green
    Get-ChildItem $Out -Filter 'fh6*' | Select-Object Name, @{n = 'MB'; e = { [math]::Round($_.Length / 1MB, 2) } } | Format-Table -AutoSize
}
finally { Pop-Location }
