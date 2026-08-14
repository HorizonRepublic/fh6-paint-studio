# build.ps1 -- build FH6 Paint Studio (CLI + desktop GUI) on Windows.
#
#   .\scripts\build.ps1          # CPU build  (no extra toolchain needed)
#
# Outputs:  bin\fh6paint.exe  (CLI)   and  bin\fh6-paint-studio.exe  (GUI).
#
# The GUI is linked with -H windowsgui so it has NO console window (it is a windowed app); the CLI
# stays a console app so its output and -h are visible in a terminal.
param()
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
if (-not $root) { $root = (Get-Location).Path }
New-Item -ItemType Directory -Force -Path (Join-Path $root 'bin') | Out-Null

# The studio's version resource is a build artifact (gitignored); generate it if missing.
$syso = Join-Path $root 'cmd\studio\rsrc_windows_amd64.syso'
if (-not (Test-Path $syso)) { & (Join-Path $PSScriptRoot 'gen-winres.ps1') -Version 0.0.0 | Out-Null }

$guiLdflags = '-H windowsgui'

