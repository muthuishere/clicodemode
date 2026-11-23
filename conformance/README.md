# CodeMode conformance suite

Language-neutral test cases for `docs/SPEC.md` (spec `codemode/v0`). A Go,
JS/TS-over-WASM or native port is conformant when it passes every case that is
not a documented deviation of the reference engine. The fixtures are written
from the spec and real JavaScript semantics, not copied from Go output.

```
conformance/
  cases/*.json         the cases (JSON arrays; schema: runner.schema.json)
  runner.schema.json   JSON Schema of a case file
  DEVIATIONS.md        cases where the Go engine disagrees with spec/JavaScript
  run-js.mjs           runner for the npm package (no dependencies)
codemode/conformance_test.go   runner for the Go engine
```

Out of scope here (they need processes, files, a CLI process or a protocol):
EXE-*, STR-2/3, WF-*, MCP-*, CAP-1/5, CLI-1/4/7/8/11, module imports.
The imports rule for stdin/eval (CLI-2, LANG-3) is covered.

## Case format

```json
{
  "id": "op3-literal-unknown-diagnostic",
  "spec": ["OP-3", "CLI-5"],
  "description": "what and why",
  "mode": "check",
  "file": "program.js",
  "ops": [{"name": "health.check", "description": "h", "side_effects": "none", "returns": {"ok": true}}],
  "program": "const a = 1\nawait call(\"health.chek\", {})",
  "input": {"any": "json"},
  "limits": {"max_steps": 10000, "max_call_depth": 50, "max_concurrency": 3, "timeout_ms": 50},
  "policy": {"deny_side_effects": ["local"]},
  "expect": {
    "diagnostics": [{"severity": "error", "line": 2, "col": 12, "messageContains": "unknown operation", "hintContains": "health.check"}],
    "calls": {"health.check": 0},
    "exit": 2
  }
}
```

Fields: `id` (unique across files), `spec` (requirement IDs), `description`,
`known_deviation?`, `mode?`, `file?`, `ops?`, `ops_b?`, `program`, `input?`,
`limits?`, `policy?`, `expect`. See `runner.schema.json` for every field.

Modes:

| mode | what the runner does |
|---|---|
| `run` (default) | register `ops`, `Run(program, file, input)` |
| `eval` | `Eval(program, input)`; the program is one expression |
| `check` | `Compile(program, file)`; compare diagnostics (and `operations`); nothing runs |
| `register` | register `ops` in order; the first failing registration must be `expect.register_error.op` |
| `fingerprint` | capabilities fingerprint of `ops` vs `ops_b` must be `equal` or `different` |

Stub operations: the handler counts the call, then crashes if `panics`, never
settles if `blocks_until_cancel`, waits `delay_ms`, fails with `throws`, else
resolves to `returns` (null when absent). `side_effects` defaults to the
library default (`local`) when absent. A host must reproduce this with its own
handler type; a handler failure is "the host function threw or returned an
error".

Expectations (one outcome plus optional extras):

- `result`: the run succeeded and the value equals this JSON (undefined = `null`).
  Objects compare as data, ignoring key order. Key order (LANG-6) is tested
  through programs that expose it (`Object.keys`, `JSON.stringify`).
- `error`: the program started and failed; `messageContains` is a substring of
  the failure message (`""` = any failure); `line` is the 1-based line it is
  reported on (the last diagnostic of the result).
- `diagnostics`: exactly these diagnostics in order, and for `run`/`eval` that
  nothing ran. `line`/`col` are 1-based and only checked when present.
  `messageContains` and `hintContains` are substrings, so ports may word the
  rest freely; for unsupported constructs (LANG-2) the message must name the
  construct (`class`, `new`, `this`, `generator`, `switch`, `delete`,
  `import`, `regex`, `eval`).
- `calls`: exact handler invocation counts. `0` proves "the handler never ran".
- `stdout`, `max_in_flight_at_most`, `operations`, and `exit`
  (CLI-3 class: 0 ok, 1 ran and failed, 2 diagnostics/nothing ran).

`limits` mirror the spec limits (`MaxSteps`, `MaxCallDepth`, `MaxConcurrency`,
`Timeout`); `policy.deny_side_effects` models `Policy.Allow` denying
operations with those side effects. A host that cannot express these must
skip, not fake, the case (`run-js.mjs` skips `policy` cases: the JS API has no
policy option yet).

## Running

Go engine (reference):

```
go test ./codemode/ -run Conformance -v
CONFORMANCE_RUN_DEVIATIONS=1 go test ./codemode/ -run Conformance   # include known deviations
```

npm package (build `npm/dist` first):

```
node conformance/run-js.mjs npm/dist/index.js [--filter substr] [--strict] [-v]
```

`run-js.mjs` prints `FAIL`, `SKIP`, `DEVIATION` (fails, and Go fails it too),
`FIXED` (passes although Go deviates) and a summary, and exits 1 on any `FAIL`.
`--strict` also fails on `DEVIATION`.

## Porting to another host

1. Parse each file as an array of cases; reject unknown fields.
2. Per case build a fresh engine, register `ops` with stub handlers.
3. Map `mode` to your API and compare as described above.
4. Do not skip `known_deviation` cases: they are the correct behaviour. Report
   them separately so that a fix of the reference engine is visible.

## Adding a case

1. Pick the requirement(s) in `docs/SPEC.md` and the file in `cases/` whose
   topic fits (`language`, `async`, `results`, `unsupported`, `diagnostics`,
   `operations`, `capabilities`, `limits`).
2. Write the expected value by reasoning from the spec and JavaScript, ideally
   checking it in Node. Never paste the Go engine's output as the expectation.
3. Keep programs short, module-free, deterministic and fast (no wall-clock
   assertions; only `sleep` of a few ms to order events).
4. Run the Go test. If Go disagrees and your fixture is right, set
   `"known_deviation": true` and add a row to `DEVIATIONS.md` (id, expected,
   Go actual, which is right). If Go is right and the spec/fixture was wrong,
   fix the fixture or the spec.
5. Validate the JSON against `runner.schema.json` (the Go test rejects
   unknown fields and duplicate ids).

Requirement coverage is by `spec` tag; `grep -ho '"[A-Z]*-[0-9]*"' conformance/cases/*.json | sort | uniq -c`
shows the counts.
