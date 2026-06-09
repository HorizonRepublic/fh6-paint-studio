# build-allgpu.ps1 -- build the UNIFIED cross-vendor binary that auto-selects the GPU
# backend at runtime (CUDA on NVIDIA, else Vulkan on AMD/Intel, else CPU). Builds both
# native shims (fh6cuda.dll via nvcc, fh6vk.dll via the portable Vulkan toolchain) then
# `go build -tags allgpu`. Run from the repo root.
# Usage: powershell -ExecutionPolicy Bypass -File .\scripts\build-allgpu.ps1
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
if (-not $root) { $root = (Get-Location).Path }

Write-Host "=== [1/3] CUDA shim (fh6cuda.dll) ===" -ForegroundColor Cyan
& powershell -ExecutionPolicy Bypass -File (Join-Path $root "scripts\build-cuda.ps1")

Write-Host "=== [2/3] Vulkan shim (fh6vk.dll) ===" -ForegroundColor Cyan
& powershell -ExecutionPolicy Bypass -File (Join-Path $root "scripts\build-vulkan.ps1")

$goExe = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $goExe) { $goExe = "$env:USERPROFILE\go-dist\go\bin\go.exe" }
$env:CGO_ENABLED = "0"
$bin = Join-Path $root "bin"

Write-Host "=== [3/3] unified binaries (-tags allgpu) ===" -ForegroundColor Cyan
Push-Location $root
& $goExe build -tags allgpu -o bin\fh6paint.exe .\cmd\fh6paint
if ($LASTEXITCODE -ne 0) { Pop-Location; throw "go build -tags allgpu (fh6paint) failed" }
& $goExe build -tags allgpu -o bin\fh6-paint-studio.exe .\cmd\studio
$ok = $LASTEXITCODE -eq 0
Pop-Location
if (-not $ok) { throw "go build -tags allgpu (studio) failed" }

Write-Host "`nBuilt bin\fh6paint.exe + bin\fh6-paint-studio.exe (auto-select CUDA/Vulkan/CPU)." -ForegroundColor Green
Write-Host "Both fh6cuda.dll and fh6vk.dll sit in bin\ beside the exes (the OS loader finds them)." -ForegroundColor Green
