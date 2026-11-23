# clicodemode (Rust)

Code Mode for Rust CLIs. Register the commands your CLI already has as
operations; an agent then sends one small JavaScript-like program that calls
and composes them, instead of many separate tool calls.

The language engine is the [clicodemode](https://github.com/muthuishere/clicodemode)
Go engine compiled to WASI and embedded in this crate, running on the
pure-Rust [`wasmi`](https://crates.io/crates/wasmi) interpreter. No Node, no
Go toolchain and no C dependency at build or run time.

```toml
[dependencies]
clicodemode = "0.5"
serde_json = "1"
```

```rust
use clicodemode::{CodeMode, Operation, RunOptions, SideEffects};
use serde_json::json;

let mut cm = CodeMode::new("1.0.0")?;
cm.register(
    Operation::new("health.check", "Report whether this machine is healthy.")
        .side_effects(SideEffects::None)
        .input_schema(json!({"type": "object", "properties": {"verbose": {"type": "boolean"}}})),
    |args| Ok(json!({"ok": true})), // wrap your existing command here
)?;

let r = cm.run(
    "export default async () => await ops.health.check({})",
    RunOptions::default().input(json!({})),
)?;
assert!(r.ok);                       // r.value == {"ok": true}

cm.check(src, "prog.js")?;           // diagnostics with file:line:col, nothing runs
cm.compile(src, "prog.js")?;         // plan: hash, operations used, cache key
cm.capabilities()?;                  // operations + language + fingerprint to cache on
cm.docs()?; cm.skill()?;             // reference text and a generated SKILL.md
cm.eval("1 + 2")?;
```

A program that throws is `Ok(RunResult { ok: false, error, diagnostics, .. })`;
`Err` is only for library or engine failures. `RunResult::stdout` holds what the
program printed with `print` / `console.log`. See `examples/demo.rs`.

Handlers return `Err(String)` to reject the program's call (it can `try/catch`
it); a panic also becomes a rejection.

## Limits

- Handlers are synchronous `Fn(Value) -> Result<Value, String> + Send + Sync`
  and run on the thread that called `run`/`eval`, one at a time. Programs may
  start calls in parallel (`Promise.all`); the host answers them in turn.
- No `exec` / `execStream` / `shell`: there is no process access in WASI. Use
  operations instead.
- No imports (there is no filesystem inside the engine); programs are single files.
- Language: no `class`, `new`, `this`, generators, `switch`, `delete`, regex,
  dynamic `import()` or `eval`. `check` reports them with file:line:col.
- Registering an operation after the first call restarts the engine (about
  50 ms), so register everything up front.
- Speed: the engine is interpreted twice (JS program in Go in wasm in wasmi),
  on the order of 50x slower than native Go on pure-CPU loops (a 100k-iteration loop takes ~0.6 s). Operations, which
  run natively, dominate real programs. Raise `limits()` as needed.
- Not a sandbox for hostile programs: operations run with your process's rights.

## Rebuilding the engine

`assets/codemode.wasm` (about 7 MB, 2 MB in the published crate) is built from
`cmd/codemode-wasi` in the repository root with `scripts/build-wasi.sh` and
embedded with `include_bytes!`. Rebuild it after engine changes.
