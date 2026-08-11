# setup-windows.ps1 — install all toolchain deps for building FH6 Paint Studio (Vulkan) on Windows.
# Run in an ELEVATED PowerShell (Run as Administrator):
#     powershell -ExecutionPolicy Bypass -File .\scripts\setup-windows.ps1
# Prefers winget (built into Win10/11). Falls back to Chocolatey if winget is missing.
# After it finishes, OPEN A NEW "x64 Native Tools Command Prompt for VS 2022" so nvcc finds cl.exe.

$ErrorActionPreference = 'Stop'

function Have($cmd) { return [bool](Get-Command $cmd -ErrorAction SilentlyContinue) }

Write-Host "=== FH6 Paint Studio — Windows toolchain setup ===" -ForegroundColor Cyan

$useWinget = Have winget
$useChoco  = Have choco

if (-not $useWinget -and -not $useChoco) {
    Write-Host "winget not found; installing Chocolatey..." -ForegroundColor Yellow
    Set-ExecutionPolicy Bypass -Scope Process -Force
    [System.Net.ServicePointManager]::SecurityProtocol = 3072
    iex ((New-Object System.Net.WebClient).DownloadString('https://community.chocolatey.org/install.ps1'))
    $useChoco = $true
}

function Install($wingetId, $chocoId, $name) {
    Write-Host "`n--- $name ---" -ForegroundColor Cyan
    if ($useWinget) {
        winget install --id $wingetId -e --accept-source-agreements --accept-package-agreements --silent
    } elseif ($useChoco) {
        choco install $chocoId -y
    }
}

# Core dev tools
Install 'Git.Git'        'git'      'Git'
Install 'GoLang.Go'      'golang'   'Go (1.23+)'


# MSVC C++ build tools (nvcc needs cl.exe). Add the C++ workload explicitly.
Write-Host "`n--- Visual Studio 2022 Build Tools (C++ / cl.exe) ---" -ForegroundColor Cyan
if ($useWinget) {
    winget install --id Microsoft.VisualStudio.2022.BuildTools -e --silent `
        --override "--quiet --wait --norestart --add Microsoft.VisualStudio.Workload.VCTools --includeRecommended"
} elseif ($useChoco) {
    choco install visualstudio2022buildtools -y
    choco install visualstudio2022-workload-vctools -y
}

Write-Host "`n=== Done. Open a NEW terminal and verify: ===" -ForegroundColor Green
Write-Host "  git --version"
Write-Host "  go version"
Write-Host "  nvidia-smi            (driver / GPU)"
Write-Host "  cl                    (run inside 'x64 Native Tools Command Prompt for VS 2022')"
Write-Host "`nNotes:" -ForegroundColor Yellow
Write-Host "   (it puts cl.exe on PATH, which nvcc + cgo require)."
Write-Host " - CPU build needs only Git + Go: 'go test ./...' and 'go run ./cmd/fh6paint ...' work without CUDA."
