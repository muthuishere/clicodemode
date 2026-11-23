#!/usr/bin/env bash
# Build the Code Mode engine as a WASI module and place it where the Rust
# crate embeds it (include_bytes!("../assets/codemode.wasm")).
set -euo pipefail
cd "$(dirname "$0")/.."
OUT=rust/assets/codemode.wasm
mkdir -p rust/assets
GOOS=wasip1 GOARCH=wasm go build -trimpath -ldflags="-s -w" -o "$OUT" ./cmd/codemode-wasi
raw=$(wc -c <"$OUT")
if command -v wasm-opt >/dev/null 2>&1; then
  wasm-opt -Oz --enable-bulk-memory --enable-sign-ext --enable-nontrapping-float-to-int --enable-mutable-globals -o "$OUT.opt" "$OUT" && mv "$OUT.opt" "$OUT" || rm -f "$OUT.opt"
fi
echo "codemode.wasm: raw ${raw} bytes, final $(wc -c <"$OUT") bytes"
if command -v gzip >/dev/null 2>&1; then echo "gzipped (what crates.io stores): $(gzip -9 -c "$OUT" | wc -c) bytes (limit 10 MB)"; fi
