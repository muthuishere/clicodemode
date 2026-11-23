#!/usr/bin/env bash
# Build the Code Mode engine for JS: npm/dist/codemode.wasm + wasm_exec.js.
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p npm/dist
GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o npm/dist/codemode.wasm ./cmd/codemode-wasm
root="$(go env GOROOT)"
src="$root/lib/wasm/wasm_exec.js"
[ -f "$src" ] || src="$root/misc/wasm/wasm_exec.js"
cp "$src" npm/dist/wasm_exec.js
ls -l npm/dist/codemode.wasm npm/dist/wasm_exec.js
