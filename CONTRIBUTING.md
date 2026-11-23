# Contributing to CLI CodeMode

**Go is the primary library.** The Go packages (`js/`, `codemode/`, `adapters/`)
are the product and the only implementation of the language. The Rust,
TypeScript and Python packages are experimental, community-evolvable wrappers
that ship **the same Go engine compiled to WebAssembly**. They are not native
Rust, TS or Python interpreters, and they do not claim parity with the Go
library beyond what their own tests and the conformance suite show.

## Layout

| Path | What it is |
|---|---|
| `js/`, `codemode/`, `adapters/` | The Go engine and library (primary) |
| `docs/SPEC.md` | Normative spec; requirement IDs are checked by `codemode/spec_test.go` |
| `conformance/` | Language-neutral cases (`cases/*.json`) that every host must pass; known Go deviations are in `conformance/DEVIATIONS.md` |
| `cmd/codemode-wasm/` + `npm/` | TypeScript wrapper (Go built for `js/wasm`, async handlers) |
| `cmd/codemode-wasi/` + `rust/` | Rust crate (Go built for `wasip1`, JSON-lines protocol, sync handlers) |
| `python/` | Python wrapper (the `wasip1` build via `wasmtime`, same protocol, sync handlers) |
| `scripts/` | Build scripts for the wasm artifacts |

Built `.wasm`, `dist/`, `target/`, virtualenvs and wheels are build outputs.
They are git-ignored and must not be committed.

## Go (primary)

```sh
go vet ./...
go test ./... -race -count=1
go test -bench . -run xxx ./js        # run before and after any change in js/
```

Changes to `js/` must keep the interpreter fast paths intact and report the
benchmark before and after. A language change needs a requirement in
`docs/SPEC.md` (keep existing IDs) and a conformance case. A bug fix needs a
regression test next to the code.

The wasm entry points must keep building without breaking native builds:

```sh
GOOS=js GOARCH=wasm go build -o /dev/null ./cmd/codemode-wasm
GOOS=wasip1 GOARCH=wasm go build -o /dev/null ./cmd/codemode-wasi
```

## Conformance

```sh
go test ./codemode -run Conformance -count=1
CONFORMANCE_RUN_DEVIATIONS=1 go test ./codemode -run Conformance   # also run known deviations
node conformance/run-js.mjs npm/dist/index.js                      # after building the TS package
```

A case marked `"known_deviation": true` is reported as skipped, never as
passed. Do not weaken a fixture to make a host pass; record a genuine Go bug
in `conformance/DEVIATIONS.md` and open an issue.

## TypeScript (`npm/`, experimental)

```sh
bash scripts/build-wasm.sh        # writes npm/dist/codemode.wasm + wasm_exec.js
cd npm && npm install && npm test # tsc build + node --test
node ../scripts/wasm-smoke.mjs
```

## Rust (`rust/`, experimental)

```sh
bash scripts/build-wasi.sh        # writes rust/assets/codemode.wasm (needed by include_bytes!)
cd rust && cargo test && cargo clippy --all-targets
```

## Python (`python/`, experimental)

```sh
bash python/scripts/check_python.sh   # venv, install, build wasm, pytest, wheel
```

See `python/README.md` for the API and limitations.

## Review checklist

- Does the change cite a spec requirement ID, and is there a conformance case?
- Regression test for every bug fix; no test skipped or weakened to pass.
- `js/` change: benchmark before/after in the PR description.
- A wrapper change: does the same program give the same result as the Go CLI? Say where it does not.
- No generated artifacts committed; versions of Go (`codemode/registry.go`
  `LibraryVersion`), `npm/package.json`, `rust/Cargo.toml` and
  `python/pyproject.toml` stay in step.
- Security: see below.

## Security model

The interpreter bounds steps, time, call depth and concurrency, and exposes
only registered operations. It is **not a sandbox for hostile code**:
operations run with the host process's privileges, so register only what a
program may do and use the policy hook to gate calls. Under WebAssembly,
`exec`, `execStream` and `shell` are denied. The Go engine (also when compiled
to WASM) owns the parser, language semantics and portable validation (syntax,
static checks, operation-input schemas). The host-language layers are thin
adapters; host authorization and filesystem/path security are enforced in the
host handler and policy, and are not sandboxed by WASM alone. Report security issues privately
through a GitHub security advisory on this repository, not a public issue.

## Maintainer

Muthukumaran Navaneethakrishnan (github.com/muthuishere).
