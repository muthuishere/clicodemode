#!/usr/bin/env bash
# Build the Go engine as a wasip1 module into the Python package (git-ignored).
# Same compiler flags as ../../scripts/build-wasi.sh, different output path.
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
repo="$(cd "$here/.." && pwd)"
OUT="$here/src/clicodemode/_codemode.wasm"
mkdir -p "$(dirname "$OUT")"
cd "$repo"
GOOS=wasip1 GOARCH=wasm go build -trimpath -ldflags="-s -w" -o "$OUT" ./cmd/codemode-wasi
raw=$(wc -c <"$OUT")
if command -v wasm-opt >/dev/null 2>&1 && [ "${NO_WASM_OPT:-0}" != 1 ]; then
  wasm-opt -Oz --enable-bulk-memory --enable-sign-ext --enable-nontrapping-float-to-int --enable-mutable-globals -o "$OUT.opt" "$OUT" && mv "$OUT.opt" "$OUT" || rm -f "$OUT.opt"
fi
echo "_codemode.wasm: raw ${raw} bytes, final $(wc -c <"$OUT") bytes -> $OUT"
