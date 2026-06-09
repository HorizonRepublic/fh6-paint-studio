# build-release.ps1 -- cut the DISTRIBUTABLE release: the GUI app + both GPU shims (fat multi-arch
# CUDA DLL + the cross-vendor Vulkan DLL). The studio is the UNIFIED allgpu build, so one download runs
# on NVIDIA (CUDA), AMD/Intel (Vulkan), or CPU, with the in-app engine picker. No CLI -- fh6paint.exe is
# a dev/batch tool only (scripts\build.ps1 builds it for development).
#
#   powershell -ExecutionPolicy Bypass -File .\scripts\build-release.ps1 [-Toolkit <dir>] [-Out <dir>] [-Version <x.y.z>] [-SkipDLL]
#
# Windows-optimised flags: stripped symbols (-s -w) + -trimpath, the GUI linked -H windowsgui (NO
# console window), CGO_ENABLED=0 (the DLLs are loaded via syscall, no cgo), GOAMD64=v1 (max CPU compat).
#
# -Version stamps the Windows version resource (Properties -> Details: version, company, product) via
# goversioninfo + cmd/studio/versioninfo.json; CI passes the release tag, a local build defaults to 0.0.0.
#
# The FAT CUDA DLL spans sm_61/75/86/89/120 (Pascal..Blackwell) + compute_61 PTX (JIT for any other
# >=6.1); see scripts\build-cuda-fat.ps1. The Vulkan DLL is built from the toolchain in third_party\vulkan
# (scripts\setup-vulkan-ci.ps1 assembles it on CI). -SkipDLL reuses the existing DLLs (shims unchanged).
param(
    [string]$Toolkit = $(if ($env:CUDA_TOOLKIT) { $env:CUDA_TOOLKIT } else { "D:\cuda12.8-portable\toolkit" }),
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
    # 1) FAT multi-arch CUDA DLL via the portable CUDA 12.8 toolkit.
    $dll = Join-Path $root "release\fh6cuda.dll"
    if ($SkipDLL -and (Test-Path $dll) -and (Test-Path $vkdll)) {
        Write-Host "Reusing existing shims: $dll + $vkdll" -ForegroundColor Yellow
    } else {
        & (Join-Path $PSScriptRoot 'build-cuda-fat.ps1') -Toolkit $Toolkit
        if ($LASTEXITCODE -ne 0) { throw "fat CUDA build failed" }
        # 1b) Cross-vendor Vulkan DLL so the release runs on AMD/Intel too. Builds bin\fh6vk.dll from
        # the third_party\vulkan toolchain (present locally; setup-vulkan-ci.ps1 assembles it on CI).
        & (Join-Path $PSScriptRoot 'build-vulkan.ps1')
        if ($LASTEXITCODE -ne 0) { throw "Vulkan shim build failed" }
    }

    # 2) Clean the output to exactly the release files (drop any dev CLI exe / link byproducts).
    New-Item -ItemType Directory -Force -Path $Out | Out-Null
    Remove-Item (Join-Path $Out 'fh6paint.exe'), (Join-Path $Out 'fh6paint-vulkan.exe'),
                (Join-Path $Out 'fh6cuda.exp'), (Join-Path $Out 'fh6cuda.lib') -ErrorAction SilentlyContinue

    # 3) Stamp the studio's version resource from -Version; verNum also feeds the binary version (-X).
    $verNum = (($Version -replace '^v', '') -split '-')[0]
    & (Join-Path $PSScriptRoot 'gen-winres.ps1') -Version $verNum

    # 4) GUI app, unified allgpu (auto-select CUDA/Vulkan/CPU + the in-app engine picker).
    $env:CGO_ENABLED = '0'
    $env:GOAMD64 = 'v1'
    & go build -tags allgpu -trimpath -ldflags "-s -w -H windowsgui -X main.version=$verNum" -o (Join-Path $Out 'fh6-paint-studio.exe') ./cmd/studio
    if ($LASTEXITCODE -ne 0) { throw "studio build failed" }
    Copy-Item $dll (Join-Path $Out 'fh6cuda.dll') -Force
    if ($vkdll -ne (Join-Path $Out 'fh6vk.dll')) { Copy-Item $vkdll (Join-Path $Out 'fh6vk.dll') -Force }
    # Drop build byproducts (a .exe~ backup left when the previous exe is/was locked) + any runtime log.
    Remove-Item (Join-Path $Out '*.exe~'), (Join-Path $Out '*.log') -ErrorAction SilentlyContinue

    Write-Host "Release ready in $Out (3 files):" -ForegroundColor Green
    Get-ChildItem $Out -Filter 'fh6*' | Select-Object Name, @{n = 'MB'; e = { [math]::Round($_.Length / 1MB, 2) } } | Format-Table -AutoSize
}
finally { Pop-Location }
