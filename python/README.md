# clicodemode for Python (experimental)

Register the commands your Python CLI already has as Code Mode operations, then
let an agent run small JavaScript-like programs that call them.

**Go is the primary library.** This package is an experimental thin wrapper
around the Go engine compiled to WebAssembly (`GOOS=wasip1`) and run by
[wasmtime](https://github.com/bytecodealliance/wasmtime-py). The engine owns
parsing, language semantics and portable validation; Python never interprets
the language and nothing is reimplemented. The host (your Python code) owns
authorization and path/exec security. Version 0.6.0 matches the Go library
version; `cm.engine_version()` reports the engine that is embedded.

## Install (no package registry needed; not on PyPI)

**a) From a GitHub release asset** (the URL is valid once the v0.6.0 release exists):

```bash
python3 -m venv .venv && . .venv/bin/activate
pip install https://github.com/muthuishere/clicodemode/releases/download/v0.6.0/clicodemode-0.6.0-py3-none-any.whl
```

The wheel is pure Python plus the embedded engine (`_codemode.wasm`); the only
dependency, `wasmtime`, is installed from its own wheels. Needs Python >= 3.9.

**b) From source** (needs Go and Python >= 3.9; Python 3.10+ for the check script):

```bash
git clone https://github.com/muthuishere/clicodemode && cd clicodemode
bash python/scripts/check_python.sh        # venv, build wasm, pytest, mypy --strict, wheel in python/dist/
# or just the two commands it wraps for a wheel:
bash python/scripts/build_wasm.sh && python3 -m pip install build && python3 -m build python
pip install python/dist/clicodemode-0.6.0-py3-none-any.whl
```

## Quickstart (a Click CLI, copy-paste and run)

```python
import click
from clicodemode import CodeMode, Policy

@click.group()
def inventory() -> None:
    """Existing CLI; nothing here knows about Code Mode."""

@inventory.command()
@click.argument("sku")
@click.option("--qty", type=int, default=1)
def add(sku: str, qty: int) -> dict:
    """Add stock; returns the new level."""
    return {"sku": sku, "qty": qty}

cm = CodeMode("1.0.0", policy=Policy(allow=lambda op: op.side_effects != "external"))
cm.register_click(inventory, "inv")        # becomes ops.inv.add

r = cm.run("""
export default async () => {
  const a = await ops.inv.add({ sku: "apple", qty: 5 });
  return { a, total: a.qty + 2 };
}""")
print(r.ok, r.value)                       # True {'a': {'qty': 5, 'sku': 'apple'}, 'total': 7}
print(cm.engine_version())
```

## Limitations at a glance (measured: Apple M-series, Python 3.12, wasmtime 51)

| Area | Status |
|---|---|
| `exec` / `execStream` / `shell` | denied (the guest has no processes) |
| Module imports | not available (no filesystem in the guest) |
| Handlers | synchronous; a running handler cannot be pre-empted, so timeout and cancel apply after it returns. This is **partial** coverage of SPEC LIM-2/LIM-3; a test (`test_slow_handler_is_not_cut_off_by_timeout`) pins it so nobody relies on it |
| Click / Typer | leaf commands only; group callbacks and group-level options do not run |
| argparse | leaf (sub-)parsers become operations, nested sub-parsers included; options on a parent parser that precede the sub-command do not apply |
| Parallelism | `Promise.all` calls are answered one after another by the host |
| Security | the Go engine owns parsing, semantics and portable validation (syntax, static checks, operation-input schemas). Authorization and filesystem/path/exec security are enforced by your Python host (`Policy.allow`, handler code); WebAssembly does not sandbox them |
| Cold vs warm | cold: first engine in a process about 300 ms; warm: later engines about 6 ms, a steady `run` with one host call about 0.3 ms (details under "Measured") |

## Who does what

| Go engine (in WASM) | Python layer (this package) |
|---|---|
| JS parser/AST, language and evaluation semantics | operation registration |
| portable core validation: syntax and static checks, diagnostics (`file:line:col`), operation-input schema checks, limits (steps, call depth, concurrency) | calling your Python handlers, JSON in/out |
| capability fingerprint, docs, generated skill | policy gate (`Policy.allow`), timeouts/cancellation, I/O |
| | host authorization and filesystem/path security |

Authorization that depends on the host (who may call what, which paths a
handler may touch) is enforced in Python, in your handlers and your `Policy`.
Nothing about paths or files is validated or sandboxed by the WASM engine:
the guest has no filesystem at all, but your handlers do, with your privileges.
If a handler takes a path, validate it in the handler (resolve it, check it
stays under an allowed directory) and deny the operation in `Policy` when in doubt.

## Develop: build and test

```bash
# from the repository root; needs Go (to build the engine) and Python >= 3.9 (tested 3.9-3.15)
python/scripts/build_wasm.sh                 # Go -> python/src/clicodemode/_codemode.wasm (git-ignored)
cd python
python3.13 -m venv .venv && .venv/bin/pip install -e ".[dev]"
.venv/bin/python -m pytest                   # unit tests + examples + the conformance suite
.venv/bin/python tests/conformance_runner.py # conformance report only
.venv/bin/python -m mypy                     # strict
python -m build                              # wheel + sdist in dist/ (contains the wasm)
scripts/check_python.sh                      # the whole clean sequence in one go
```

The wasm is a build output (about 7.1 MB, about 2.1 MB inside the wheel). It is
never committed: `build_wasm.sh` produces it and `python -m build` packages it.
Building a wheel without running the script first fails the wasm check in
`check_python.sh` and raises a clear error at first use.

Runtime dependency: `wasmtime~=51.0` (Apache-2.0 WITH LLVM-exception). Test extras: `click`, `typer`, `pytest`.

## API tour

```python
from clicodemode import CodeMode, Limits, Policy

cm = CodeMode("1.0.0",
              limits=Limits(max_steps=1_000_000, timeout_ms=2000),
              policy=Policy(allow=lambda op: op.side_effects != "external"))

@cm.operation("health.check", description="Report health.", side_effects="none", idempotent=True,
              input_schema={"type": "object", "properties": {}, "additionalProperties": False})
def health(args):
    return {"ok": True}

r = cm.run("export default async () => await ops.health.check({})", timeout=5)
r.ok, r.value, r.error, r.diagnostics, r.stdout, r.ms
```

`cm.run(src, input=..., file=..., timeout=<seconds>, cancel=<threading.Event>)`,
`cm.eval(expr)`, `cm.check(src)`, `cm.compile(src)`, `cm.capabilities()`,
`cm.operations()`, `cm.describe(name)`, `cm.docs()`, `cm.skill()`, `cm.cancel()`, `cm.close()`.
A program that throws, is rejected or times out is a `Result(ok=False)`, not an exception.

### Existing functions and CLIs

```python
cm.register_function("util.greet", greet)          # schema from the signature and type hints
cm.register_click(group, "inv", exclude=["code"])  # every leaf command becomes inv.<cmd> (sub-groups: inv.<sub>.<cmd>)
cm.register_typer(app, exclude=["code"])           # via the command Typer builds
cm.register_argparse(parser, exclude=["code"])     # sub-parsers with set_defaults(func=handler)
```

The wrapped command runs in-process, with its own argument parsing, so Click,
Typer and argparse validation and errors apply. Its return value is the result;
if it returns `None`, what it printed is the result (JSON-decoded when it is
JSON). Click/Typer errors and argparse usage errors become rejected calls with
the original message. Only leaf commands run: group callbacks and group-level
options do not (use `obj=` for `ctx.obj`). Stdout capture is process-wide, so
wrapped commands are serialised by a lock.

Complete, tested examples, each a real CLI with an extra `code run` command:
[`examples/click_cli.py`](examples/click_cli.py),
[`examples/typer_cli.py`](examples/typer_cli.py),
[`examples/argparse_cli.py`](examples/argparse_cli.py).

```bash
python examples/click_cli.py code run -e 'await call("inventory.add", {sku: "apple", qty: 5})'
```

(The examples only read a program file the user names on the command line; they expose no file operations as operations.)

## Behaviour, honestly

* **Handlers are synchronous** and run in-process on the thread that called `run`, when the engine emits a call. Calls from `Promise.all` are all issued by the program but **answered one after another** by the host.
* **Handler errors** reject the call inside the program (catchable; message preserved). **Policy denial** rejects it with `"<op>: denied by policy"`; the handler never runs. A raising policy denies, with its message.
* **Result conversion**: JSON both ways, `None` is `null`, Unicode is preserved. A handler result that is not JSON-serialisable (dataclass, set, NaN) rejects that call with a message naming the operation.
* **Input checks**: arguments are validated against `input_schema` by the engine before your handler is reached (the schemas derived for functions/Click/Typer/argparse use `additionalProperties: false`).
* **Timeout and cancellation** really stop runaway code: the guest is interrupted (wasmtime epoch interruption) and **restarted**; the next call transparently starts a fresh engine (about 6 ms, program state is per run anyway). Engine-side limits (`max_steps`, `max_call_depth`) end the run without a restart. `Limits.timeout_ms` is enforced by the engine; the host adds a 1 s backstop. A Python handler that is *executing* cannot be pre-empted: timeout/cancel apply when it returns.
* **Thread-safety**: one `CodeMode` serialises its calls with a lock, so sharing it is safe but one run at a time; `cancel()` is the only call meant for another thread. Use one instance per thread for parallel runs. A handler cannot re-enter its own engine (raises `UsageError`).
* **Isolation**: every `CodeMode` has its own WebAssembly instance and its own operations. The compiled module is shared per process.
* **exec / execStream / shell are denied** (the guest has no processes); programs get a "not permitted" diagnostic or error.

### Differences from native Go

No claim of parity. Known differences: no module imports (no filesystem in the guest); CPU-bound code is slower (a 1M-iteration loop took ~0.5 s here); handlers are sync Python, so a blocked handler cannot be cancelled mid-call; `exec`/`shell` are denied; key order of operation results and `input` is not preserved by the Go engine (fixture `lang6-*`); the engine's own known deviations (conformance `known_deviation`) apply as they do in Go; only one engine per WASM instance, started lazily and restarted after registration changes or interruption.

## Measured (Apple M-series, Python 3.12, wasmtime 51)

| | |
|---|---|
| first engine in a process (Cranelift compile of the 7.1 MB module + run) | ~300 ms |
| new engine afterwards (module deserialized from the process cache) + first run | ~6 ms |
| steady `run` with one host call | ~0.3 ms |
| 500 parallel calls in one `Promise.all` | ~50 ms |
| interrupted run + restart + next run (200 ms budget) | ~230 ms |

## Conformance

`tests/conformance_runner.py` drives `../conformance/cases/*.json` (250 cases)
through this API. Cases flagged `known_deviation` are reported as skipped, never
as passed; cases the wrapper cannot express are listed as unsupported
(currently one: an operation that never settles, which a synchronous handler
cannot model). `Policy` maps the fixtures' `deny_side_effects`.

## Security

The engine is an interpreter, **not a sandbox for hostile code**. Programs can
only reach what you register, but registered handlers run with the host
process's full privileges, and a program can call them in any order and
quantity. `Policy.allow` is the gate: deny what an agent must not do, validate
every argument that reaches files, processes or the network inside the
handler, and keep `side_effects` honest. Resource limits (`Limits`, timeouts)
bound compute, not what handlers do.

## License

MIT (see `LICENSE`).
