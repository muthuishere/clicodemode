# Known deviations of the Go reference engine

Each entry is a conformance case whose fixture follows `docs/SPEC.md` and real
JavaScript (checked against Node 24), but which the Go engine fails. The case
carries `"known_deviation": true`; `go test ./codemode/ -run Conformance` skips
it. Run them anyway with `CONFORMANCE_RUN_DEVIATIONS=1`. A port should treat
the fixture as correct and pass these cases; `run-js.mjs` reports a pass as
`FIXED` and a failure as `DEVIATION` (non-fatal unless `--strict`).

Status of the fix is "open" for all entries below.

| case id | spec | fixture expects | Go actually | who is right |
|---|---|---|---|---|
| `async-microtask-order` | LANG-1 | `[1, 2]`: `Promise.resolve().then(f1)` queued before `(async () => { await null; f2() })()`, jobs run FIFO, as in JS | `[2, 1]` in about 14 of 15 runs, so it is also nondeterministic: the await continuation can overtake an earlier `.then` job | fixture. Promise jobs must run in FIFO order on one queue; the Go engine lets the await continuation race the `.then` job. The WASM build in `npm/dist` produced `[1, 2]` every time. |
| `lang1-number-small-decimal-string` | LANG-1 | `String(0.000001)` is `"0.000001"` (JS switches to exponent form only below 1e-6, so `String(1e-7)` is `"1e-7"`) | `"1e-6"` | fixture. Number-to-string uses a Go `%g`-style cutoff. |
| `lang1-number-large-decimal-string` | LANG-1 | `String(123456789.5)` is `"123456789.5"`, `String(1234567.25)` is `"1234567.25"` | `"1.234567895e+8"`, `"1.23456725e+6"` | fixture. Non-integers below 1e21 must print positionally. This one affects any program that builds strings or JSON text from fractional numbers over about 1e6 (`JSON.stringify`/template output). |
| `lang1-string-length-utf16` | LANG-1 | `"😀".length === 2`, `"a😀".length === 3` (UTF-16 code units) | `1` and `2` (counts code points) | fixture, as normal JavaScript semantics (LANG-1) require. Strings outside the BMP are the only case affected; the BMP cases (`"日本".length`) agree and are in `lang1-unicode-strings`. |
| `lang6-key-order-input` | LANG-6, CLI-10 | `input` of `{"b":1,"a":2,"1":3}` iterates as `1,b,a` (integer keys ascending, then insertion order = document order) | `1,a,b`: the `--input` JSON is decoded into a Go `map`, so insertion order is lost and keys come back sorted | fixture. Input must keep document order. |
| `lang6-key-order-op-result` | LANG-6, OP-1 | a handler result `{"b":1,"a":2,"1":3}` iterates as `1,b,a` | `1,a,b` (Go `map[string]any` handler results; also normalised through `json.Marshal`) | fixture for hosts that can express order (a JS handler returns an ordered object). For the Go host the order is not representable in `map[string]any`; if that is accepted, Go hosts could be exempted by weakening this case, but the JS port must preserve order. |
| `lang2-regex-literal` | LANG-2 | a parse error whose message names the construct (contains `regex`) | `syntax error: unexpected "/"` at the right line and column but without naming regex | fixture; spec requires the message to name the construct. |
| `lang2-eval` | LANG-2, CAP-4 | `eval("1 + 1")` is a parse error and nothing runs (`check` gives one diagnostic, exit 2) | `check` reports no diagnostics; `run` starts the program and fails at run time with `uncaught ReferenceError: eval is not defined` (exit 1) | fixture. LANG-2 lists `eval` among constructs that are parse errors, and `language.unsupported` advertises it. |

## Notes on things that are not deviations

- Diagnostic columns for unsupported constructs: `function* g() {}` is
  reported by Go at the `*` (col 9), `new`/`this`/`yield` at the keyword. The
  spec fixes only the line and the construct name for generators, so
  `lang2-generator-function` asserts the line and message only.
- `Result.value` objects are compared as data, so key order is verified only
  through programs that expose it (`Object.keys`, `JSON.stringify`).
