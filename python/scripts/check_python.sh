#!/usr/bin/env bash
# Full clean sequence: venv -> install -> build wasm -> pytest -> mypy -> wheel.
# Usage: python/scripts/check_python.sh [python-executable]   (default: uv, else python3.13/3.12/3.11)
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
cd "$here"

PY="${1:-}"
if [ -z "$PY" ]; then
  for c in python3.13 python3.12 python3.11 python3.10; do command -v "$c" >/dev/null 2>&1 && PY="$(command -v "$c")" && break; done
fi
[ -n "$PY" ] || { echo "no python >= 3.10 found (pass one as \$1)"; exit 2; }

echo "== 1. clean venv ($PY)"
rm -rf .venv dist build
if command -v uv >/dev/null 2>&1; then uv venv -q --python "$PY" .venv; else "$PY" -m venv .venv; fi
VPY="$here/.venv/bin/python"
pip_install() { if command -v uv >/dev/null 2>&1; then uv pip install -q --python "$VPY" "$@"; else "$VPY" -m pip install -q "$@"; fi; }

echo "== 2. build the Go engine to WebAssembly"
scripts/build_wasm.sh

echo "== 3. install (editable) with the dev extras"
pip_install -e ".[dev]"

echo "== 4. pytest"
"$VPY" -m pytest -q

echo "== 5. mypy --strict"
"$VPY" -m mypy

echo "== 6. wheel + sdist (the wasm is copied into the package in step 2, so the wheel contains it)"
"$VPY" -m build -q
ls -l dist
"$VPY" - <<'PY'
import glob, zipfile
w = glob.glob("dist/*.whl")[0]
z = zipfile.ZipFile(w)
names = z.namelist()
assert any(n.endswith("_codemode.wasm") for n in names), "wheel is missing the wasm"
print("wheel contains:", [n for n in names if n.endswith(".wasm")], "- wheel size", sum(i.compress_size for i in z.infolist()), "bytes")
PY
echo "OK"
