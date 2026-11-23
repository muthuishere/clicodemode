# Known deviations of the Go reference engine

Each entry is a conformance case whose fixture follows `docs/SPEC.md` and real
JavaScript (checked against Node 24), but which the Go engine once failed. A
failing case would carry `"known_deviation": true`; `go test ./codemode/ -run
Conformance` skips such a case unless `CONFORMANCE_RUN_DEVIATIONS=1` is set.
`run-js.mjs` reports a pass of a flagged case as `FIXED` and a failure as
`DEVIATION` (non-fatal unless `--strict`).

All eight deviations are fixed in library version 0.6.0 (issues #1 to #5),
so no case carries `"known_deviation"` any more and
`CONFORMANCE_RUN_DEVIATIONS=1 go test ./codemode -run Conformance` has
nothing extra to run. The table is kept as the record of what each case
caught. A port should treat every fixture as correct.

| case id | issue | status | what was wrong, and the fix |
|---|---|---|---|
| `async-microtask-order` | #3 | Fixed | Await continuations and `.then` jobs each waited on their own goroutine for the interpreter lock, so an await could overtake an earlier `.then` job (`[2, 1]`, nondeterministic). The interpreter lock is now a FIFO queue (`fifoLock`) and a job takes its place in it when its promise settles (or at once if already settled), and a job settles its own result promise before releasing the lock. One deterministic job queue, as in JS; verified with `-count=300` and `-race`. |
| `lang1-number-small-decimal-string` | #1 | Fixed | Number-to-string used Go `%g` cutoffs. It now lays out the shortest round-trip digits as ECMAScript `Number::toString`: positional for `1e-6 <= abs(x) < 1e21`, exponent form otherwise (`1e-7`, `1e+21`, `-0` is `"0"`). |
| `lang1-number-large-decimal-string` | #1 | Fixed | Same fix: `String(123456789.5)` is `"123456789.5"`. |
| `lang1-string-length-utf16` | #4 | Fixed | Strings are still Go UTF-8 strings, but `length`, indexing, `slice`, `substring`, `charAt`, `at`, `indexOf`, `split("")`, `padStart` and `padEnd` count UTF-16 code units (pure-ASCII strings keep a byte-indexed fast path). `charCodeAt` and `codePointAt` were added. A lone surrogate (half an emoji) is kept as its WTF-8 bytes and rejoins when concatenated with `+`. `for...of`, spread and `Array.from` still iterate code points, as in JS. |
| `lang6-key-order-input` | #2 | Fixed | `--input` / `RunWithInput` JSON is decoded by an ordered token walk (`js.ParseJSON`) instead of into a Go `map`. |
| `lang6-key-order-op-result` | #2 | Fixed, with a Go-host limit that is intentional | A handler that returns JSON text as `codemode.OrderedJSON(text)` (or a `json.RawMessage`) keeps document order; structs keep field order. A Go `map[string]any` result has no order to keep, so its keys arrive sorted (integer-like keys first, then lexicographic), which is what inserting the sorted keys gives under the JS ordering rule. That is the documented Go-host behaviour, not a bug. Bridges (WASM, WASI) whose operations return JSON text must return `codemode.OrderedJSON(text)`. |
| `lang2-regex-literal` | #5 | Fixed | A regex literal is a parse error whose message says `regex literals are not supported`, at the position of the `/`. |
| `lang2-eval` | #5 | Fixed | `eval` is a parse error (`check` exits 2, nothing runs). |

## Notes on things that are not deviations

- Diagnostic columns for unsupported constructs: `function* g() {}` is
  reported by Go at the `*` (col 9), `new`/`this`/`yield` at the keyword. The
  spec fixes only the line and the construct name for generators, so
  `lang2-generator-function` asserts the line and message only.
- `Result.value` objects are compared as data, so key order is verified only
  through programs that expose it (`Object.keys`, `JSON.stringify`).
