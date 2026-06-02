# build-release.ps1 -- cut the DISTRIBUTABLE release: exactly TWO files, the GUI app + the FAT
# multi-arch CUDA DLL. No CLI — fh6paint.exe is a dev/batch tool only (build.ps1 builds it for
# development); end users need just the studio + its DLL.
#
#   powershell -ExecutionPolicy Bypass -File .\build-release.ps1 [-Toolkit <dir>] [-Out <dir>] [-SkipDLL]
#
# Windows-optimised flags: stripped symbols (-s -w) + -trimpath, the GUI linked -H windowsgui (NO
# console window), CGO_ENABLED=0 (the DLL is loaded via syscall, no cgo), GOAMD64=v1 (max CPU compat).
#
# The FAT DLL spans sm_61/75/86/89/120 (Pascal..Blackwell) + compute_61 PTX (JIT for any other >=6.1);
# see build-cuda-fat.ps1. -SkipDLL reuses an existing release\fh6cuda.dll (shim.cu unchanged).
param(
    [string]$Toolkit = $(if ($env:CUDA_TOOLKIT) { $env:CUDA_TOOLKIT } else { "D:\cuda12.8-portable\toolkit" }),
    [string]$Out = "bin",
    [switch]$SkipDLL
)
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot; if (-not $root) { $root = (Get-Location).Path }
if (-not [System.IO.Path]::IsPathRooted($Out)) { $Out = Join-Path $root $Out }
Push-Location $root
try {
    # 1) FAT multi-arch CUDA DLL via the portable CUDA 12.8 toolkit.
    $dll = Join-Path $root "release\fh6cuda.dll"
    if ($SkipDLL -and (Test-Path $dll)) {
        Write-Host "Reusing existing FAT DLL: $dll" -ForegroundColor Yellow
    } else {
        & (Join-Path $root 'build-cuda-fat.ps1') -Toolkit $Toolkit
        if ($LASTEXITCODE -ne 0) { throw "fat CUDA build failed" }
    }

    # 2) Clean the output to exactly the two release files (drop any dev CLI exe / link byproducts).
    New-Item -ItemType Directory -Force -Path $Out | Out-Null
    Remove-Item (Join-Path $Out 'fh6paint.exe'), (Join-Path $Out 'fh6cuda.exp'), (Join-Path $Out 'fh6cuda.lib') -ErrorAction SilentlyContinue

    # 3) GUI app only, Windows-optimised flags.
    $env:CGO_ENABLED = '0'
    $env:GOAMD64 = 'v1'
    & go build -tags cuda -trimpath -ldflags "-s -w -H windowsgui" -o (Join-Path $Out 'fh6-paint-studio.exe') ./cmd/studio
    if ($LASTEXITCODE -ne 0) { throw "studio build failed" }
    Copy-Item $dll (Join-Path $Out 'fh6cuda.dll') -Force
    # Drop build byproducts (a .exe~ backup left when the previous exe is/was locked) + any runtime log.
    Remove-Item (Join-Path $Out '*.exe~'), (Join-Path $Out '*.log') -ErrorAction SilentlyContinue

    Write-Host "Release ready in $Out (2 files):" -ForegroundColor Green
    Get-ChildItem $Out -Filter 'fh6*' | Select-Object Name, @{n = 'MB'; e = { [math]::Round($_.Length / 1MB, 2) } } | Format-Table -AutoSize
}
finally { Pop-Location }
