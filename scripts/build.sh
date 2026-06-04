#!/usr/bin/env sh
# build.sh -- build FH6 Paint Studio (CLI + desktop GUI), CPU backend.
# For the NVIDIA/CUDA build use  scripts/build.ps1 -Cuda  on Windows.
set -e
cd "$(dirname "$0")/.."
mkdir -p bin
go build -o bin/fh6paint ./cmd/fh6paint
go build -o bin/fh6-paint-studio ./cmd/studio
echo "Built bin/fh6paint + bin/fh6-paint-studio (CPU)."
