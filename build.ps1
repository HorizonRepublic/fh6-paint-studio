# build.ps1 -- build FH6 Paint Studio (CLI + desktop GUI) on Windows.
#
#   .\build.ps1          # CPU build  (no extra toolchain needed)
#   .\build.ps1 -Cuda    # GPU build  (compiles bin\fh6cuda.dll via build-cuda.ps1, then -tags cuda)
#
# Outputs:  bin\fh6paint.exe  (CLI)   and  bin\fh6-paint-studio.exe  (GUI).
# For the GPU build, bin\fh6cuda.dll must sit beside the exe (it does — both land in bin\).
#
# The GUI is linked with -H windowsgui so it has NO console window (it is a windowed app); the CLI
# stays a console app so its output and -h are visible in a terminal.
param([switch]$Cuda)
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
if (-not $root) { $root = (Get-Location).Path }
New-Item -ItemType Directory -Force -Path (Join-Path $root 'bin') | Out-Null

$guiLdflags = '-H windowsgui'

if ($Cuda) {
    Write-Host "== building CUDA backend (bin\fh6cuda.dll) ==" -ForegroundColor Cyan
    & (Join-Path $root 'build-cuda.ps1')
    if ($LASTEXITCODE -ne 0) { throw "build-cuda.ps1 failed" }
    & go build -tags cuda -o (Join-Path $root 'bin\fh6paint.exe') ./cmd/fh6paint
    & go build -tags cuda -ldflags $guiLdflags -o (Join-Path $root 'bin\fh6-paint-studio.exe') ./cmd/studio
    Write-Host "Built bin\fh6paint.exe + bin\fh6-paint-studio.exe (CUDA, fh6cuda.dll alongside)." -ForegroundColor Green
} else {
    & go build -o (Join-Path $root 'bin\fh6paint.exe') ./cmd/fh6paint
    & go build -ldflags $guiLdflags -o (Join-Path $root 'bin\fh6-paint-studio.exe') ./cmd/studio
    Write-Host "Built bin\fh6paint.exe + bin\fh6-paint-studio.exe (CPU)." -ForegroundColor Green
}
