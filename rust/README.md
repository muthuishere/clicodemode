# clicodemode (Rust)

Code Mode for Rust CLIs: register the commands your CLI already has as
operations, and an agent sends one small JavaScript-like program that calls and
composes them instead of making many separate tool calls.

**Status: experimental.** The Go library
([clicodemode](https://github.com/muthuishere/clicodemode)) is the primary
implementation. This crate is a thin wrapper that ships the Go engine compiled
to WebAssembly (WASI) and runs it inside the pure-Rust
[`wasmi`](https://crates.io/crates/wasmi) interpreter. It is not a native Rust
interpreter. The engine owns parsing, language semantics and validation; **your
program owns authorization** (what an operation is allowed to do, which paths
it may touch, which processes it may start).

## Install and quickstart (fresh machine)

```sh
cargo new hello && cd hello
cargo add clicodemode serde_json
# paste the main.rs below, then:
cargo run
```

The crate on crates.io ships the engine (`assets/codemode.wasm`), so no Go is
needed.

### Git dependency (needs Go 1.26+ installed)

```sh
cargo new hello && cd hello
cargo add clicodemode --git https://github.com/muthuishere/clicodemode
cargo add serde_json
# paste the main.rs below, then:
cargo run
```

The first build compiles the Go engine to `codemode.wasm` with
`GOOS=wasip1 GOARCH=wasm go build` (about 10 s, downloads one small Go module),
into cargo's `OUT_DIR`. Nothing is written to the source tree. The crate lives
in the `rust/` subdirectory of the repository; cargo finds it by name.

### Release asset (no Go needed)

Download `codemode.wasm` from the
[GitHub release](https://github.com/muthuishere/clicodemode/releases) and point the build at it:

```sh
curl -L -o codemode.wasm https://github.com/muthuishere/clicodemode/releases/latest/download/codemode.wasm
CLICODEMODE_WASM="$PWD/codemode.wasm" cargo run
```

`build.rs` picks the wasm in this order: `CLICODEMODE_WASM`, then
`assets/codemode.wasm` (shipped inside a packaged crate), then a Go build of
`../cmd/codemode-wasi`. If none is possible it fails with a message naming
the two options above.

### `src/main.rs`

```rust
use clicodemode::{CodeMode, Operation, RunOptions, SideEffects};
use serde_json::json;

fn main() -> Result<(), clicodemode::Error> {
    let mut cm = CodeMode::new("1.0.0")?;
    cm.register(
        Operation::new("math.add", "Add two numbers.")
            .side_effects(SideEffects::None)
            .input_schema(json!({
                "type": "object", "required": ["a", "b"],
                "properties": {"a": {"type": "number"}, "b": {"type": "number"}}
            })),
        |args| Ok(json!(args["a"].as_f64().unwrap_or(0.0) + args["b"].as_f64().unwrap_or(0.0))),
    )?;

    let program = r#"export default async () => {
        const sum = await ops.math.add({ a: input.x, b: 2 });
        console.log("sum is", sum);
        return { sum };
    }"#;
    let r = cm.run(program, RunOptions::default().input(json!({ "x": 40 })))?;
    println!("ok={} value={} printed={:?}", r.ok, r.value, r.stdout);
    Ok(())
}
```

Output: `ok=true value={"sum":42} printed="sum is 42\n"`


## Ship your own JS modules

Bake `.js` files into the binary; programs import them by name:

```rust
cm.register_modules("mycli", clicodemode::embed_modules!("js", ["layout.js"]))?;
// whole folder: build.rs -> clicodemode::build::embed_dir("js", "modules.rs"),
// then cm.register_modules("mycli", include!(concat!(env!("OUT_DIR"), "/modules.rs")))?
```

## More of the API

```rust
cm.check(src, "prog.js")?;     // diagnostics with file:line:col; nothing runs
cm.compile(src, "prog.js")?;   // plan: hash, operations used, cache key
cm.capabilities()?;            // operations + language + fingerprint to cache on
cm.docs()?; cm.skill()?;       // reference text and a generated SKILL.md
cm.eval("1 + 2")?;
cm.limits(Limits { max_steps: 1_000_000, timeout_ms: 5_000, ..Limits::default() });
cm.run(src, RunOptions::default().timeout(Duration::from_secs(2)))?; // per-run wall clock
```

A program that throws is `Ok(RunResult { ok: false, error, diagnostics, .. })`;
`Err` is only for library or engine failures (`Error::Engine`, `Runtime`,
`Usage`, `Json`). `RunResult::stdout` holds what the program printed. A handler
returns `Err(String)` to reject the program's call (the program can
`try/catch` it); a handler panic also becomes a rejection. See `examples/demo.rs`.

Object key order: `serde_json::Value` sorts object keys unless your crate
enables serde_json's `preserve_order` feature. A handler result then reaches the
program in document order (`{"b":1,"a":2,"1":3}` iterates as `1,b,a`, as in
JavaScript); without the feature it iterates as `1,a,b`.

## Limitations

| Area | Behaviour |
|---|---|
| Handlers | Synchronous `Fn(Value) -> Result<Value, String> + Send + Sync`, run one at a time on the thread that called `run`/`eval`. Programs may start calls in parallel (`Promise.all`); the host answers them in turn. |
| Timeouts | `RunOptions::timeout` / `Limits::timeout_ms` stop an infinite loop in the program. They cannot interrupt a handler that is already running: a timeout is noticed once the handler returns, and a handler's side effects still happen. |
| Imports | None: there is no filesystem inside the engine. Programs are single files. |
| `exec` / `execStream` / `shell` | Denied (no process access in WASI). Calls fail with "not permitted". Expose what you need as operations. |
| Language | No generators, `delete`, class getters/setters, regex lookaround or backreferences, dynamic `import()` or `eval`. `check` reports them with file:line:col. |
| Registering after first use | Restarts the engine (about 6 to 8 ms warm), so register everything up front. |
| Speed | The program is interpreted twice (JS in Go in wasm in wasmi): about 50x slower than native Go on pure-CPU loops. Measured on an Apple M-series laptop, release build: a 100,000-iteration loop takes 0.4 to 0.5 s. Operations run natively and dominate real programs. |
| Cold start | Measured, release build: first engine in a process 47 to 56 ms (compiles the wasm module once); each further engine 6 to 8 ms; a warm `eval` about 0.5 ms. |
| Size | `codemode.wasm` is 7.15 MB raw, 2.05 MB gzip. The packaged `.crate` is about 2.1 MB (limit 10 MB) and contains the wasm. |
| Security | Not a sandbox for hostile programs. Operations run with your process's rights; check authorization, paths and arguments inside your handlers. |

## Developing

`scripts/build-wasi.sh` (repository root) rebuilds `rust/assets/codemode.wasm`
from `cmd/codemode-wasi` and prints raw and gzipped sizes. The file is
git-ignored; `cargo package` includes it explicitly, so a published crate needs
neither Go nor network access to build. `cargo test` also runs the repository's
language-neutral conformance cases (`../conformance/cases`) through this crate
and prints the totals (use `-- --nocapture`).
