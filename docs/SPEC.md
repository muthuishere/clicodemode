# CLI CodeMode specification

Spec version `codemode/v0` (the `spec` field of `code capabilities`).
Implementation: this repository, v0.5.x. Keywords MUST, MUST NOT, SHOULD and
MAY are normative. Every requirement has an ID; `codemode/spec_test.go`
checks the CLI-level ones end to end and names the test covering each of the
others. A change that breaks a requirement is a breaking change.

## 1. Purpose

An agent sends one small program to a CLI instead of many separate CLI or
tool calls. The CLI runs it in-process against its own registered operations
and returns only the result.

## 2. CLI contract (`<cli> code <subcommand>`)

| ID | Requirement |
|---|---|
| CLI-1 | Subcommands: `operations`, `describe <op>`, `capabilities`, `docs`, `check`, `compile`, `run`, `eval`, `skill`, `run-step`. `help`/no args prints usage, exit 0. |
| CLI-2 | `check`, `compile`, `run` take a file path or `-` (stdin). Programs from stdin or eval MUST NOT import modules. |
| CLI-3 | Exit codes: 0 success; 1 the program ran and failed (uncaught error, failed operation, unhandled rejection, limit hit); 2 usage error or diagnostics, in which case **nothing ran**. |
| CLI-4 | `run --json` / `eval --json` print exactly one JSON object `{ok, value, error?, diagnostics?, ms}`. Without `--json`, a string result prints raw, other results as JSON, nothing for null/undefined. |
| CLI-5 | Diagnostics have `severity, file, line, col, message, hint?`; text form `file:line:col: severity: message (hint)`. Line and column are 1-based. |
| CLI-6 | `check --json` prints a JSON array of diagnostics (`[]` when clean). |
| CLI-7 | `describe` of an unknown operation exits 2 and suggests the closest name when one is within edit distance 3. |
| CLI-8 | `run-step <mod.js>#<export> [--input JSON]` calls the export with `{input, steps: {}}`, prints its JSON result. Exit 0 ok, 1 failure, 2 usage. With `--outcomes a,b,...` the step MUST return `{outcome}` from that list and the process exits `10 + index`; it MUST NOT exit 0. |
| CLI-10 | `run` and `eval` accept `--input JSON` (or `--input=JSON`); the value is the program's global `input` (null when absent). Invalid JSON exits 2 and runs nothing. This is how an agent's own code mode passes data in. |
| CLI-11 | `version` prints `<cli version> (clicodemode <library version>, spec <spec version>)`, so a runner can verify which build is on its PATH. |
| CLI-9 | Program output from `print`/`console.log` goes to the engine's stdout writer, never mixed into `--json` result framing by the library itself. |

## 3. Capabilities document

| ID | Requirement |
|---|---|
| CAP-1 | `capabilities --json` returns `{spec, version, fingerprint, language, operations}`. |
| CAP-2 | `fingerprint` = `sha256:` + 32 hex chars over spec version, CLI version and every operation's public contract. It MUST be stable for identical registries and MUST change when any operation is added, removed or its contract changes. |
| CAP-3 | Each operation: `name` (dotted lower-case segments `[a-z][a-z0-9_-]*`), `description`, `input_schema?`, `output_schema?`, `side_effects` (`none|local|external`, default `local`), `idempotent`, `authorization?`, `examples?`. Registration MUST reject bad names, duplicates, missing handlers and invalid JSON schemas. |
| CAP-4 | `language` lists `features`, `unsupported`, `globals`; it MUST match what the interpreter accepts. |
| CAP-5 | Caches keyed on capabilities MUST include version, fingerprint and authorization context, and MUST NOT store secret values or approvals. |

## 4. Operations and calls

| ID | Requirement |
|---|---|
| OP-1 | `call(name, args)` and `ops.<path>(args)` are equivalent and return a Promise. |
| OP-2 | Arguments MUST be an object (or omitted) and are validated against `input_schema` (type, properties, required, enum, items, additionalProperties:false) before the handler runs. |
| OP-3 | A literal operation name that is not registered is a `check` error (with did-you-mean); a computed one fails at run time. Either way the handler never runs. Literal parts of the arguments are validated against `input_schema` by `check` too; `ops.a.b-c(...)` (which parses as a subtraction) is a `check` error pointing to `ops.a["b-c"](...)`. |
| OP-4 | `Policy.Allow` is consulted before every call; a denial rejects the call. |
| OP-5 | Handler errors reject the promise and are catchable; a handler panic MUST become a rejection, not crash the host. |
| OP-6 | Operations in flight are bounded by `Limits.MaxConcurrency` (default 32). |

## 5. Language

| ID | Requirement |
|---|---|
| LANG-1 | Supported constructs follow normal JavaScript semantics: const/let, literals, template literals, objects/arrays, destructuring, spread/rest, all operators incl. `??` `?.` `**`, if/else, ternary, for, for-of, for-in, while, do-while, break/continue, functions, arrows, closures, default params, try/catch/finally/throw, async/await, Promises (`resolve reject all allSettled race any`, `.then .catch .finally`). |
| LANG-2 | Unsupported constructs (`class new this` generators `switch delete` regex, dynamic `import()`, `eval`) are **parse errors** with a message naming the construct, never silent misbehaviour. |
| LANG-3 | Static imports: only relative `./` or `../` paths ending `.js` (code) or `.json` (data, default export), confined to the module root; cycles are an error. No npm, no Node or browser APIs, no ambient globals beyond those listed in CAP-4. |
| LANG-4 | Result of a run: the default export (a function is called and awaited), otherwise the last top-level expression statement's value; a promise result is awaited. |
| LANG-5 | A rejected promise nobody awaited or caught fails the run ("unhandled promise rejection"). |
| LANG-6 | Object key order follows JS rules (integer-like keys ascending, then insertion order) in iteration and JSON output. |

## 6. Limits and cancellation

| ID | Requirement |
|---|---|
| LIM-1 | Each run is bounded by `MaxSteps` (default 10M), `MaxCallDepth` (512), `MaxConcurrency` (32) and optional `Timeout`, plus the caller's context. |
| LIM-2 | Exceeding a limit or cancellation stops the whole run and MUST NOT be catchable, including when it surfaces as a rejection inside async code, `sleep`, an operation or `.catch`. |
| LIM-3 | On cancellation, pending operations receive a cancelled context, child processes are killed and streams closed. |

## 7. Processes and streams

| ID | Requirement |
|---|---|
| EXE-1 | `exec`, `execStream` are denied unless the host lists the program in `ExecPolicy.Programs` (or `*`); argv is an array and is never shell-interpreted. Literal disallowed programs are `check` errors. |
| EXE-2 | `shell(cmd)` requires `ExecPolicy.Shell`, independent of `Programs`. |
| EXE-3 | Children get only `PATH HOME LANG TMPDIR` plus names in `ExecPolicy.Env`; `cwd` MUST resolve inside `ExecPolicy.Dir` (symlinks resolved). |
| EXE-4 | `exec` returns `{ok, exitCode, stdout, stderr, truncated?}`; a non-zero exit is a result, not an exception. Output is capped (`MaxOutput`, default 8 MiB). |
| STR-1 | Streams are lazy: nothing runs until a terminal (`toArray count first reduce forEach json writeJSONL writeLines`). Transforms: `lines jsonl csv yaml map filter take`. |
| STR-2 | Streams apply backpressure (bounded buffers), stop early on `take` or error and kill the source process; a non-zero exit after a full read rejects with the stderr tail. |
| STR-3 | `.yaml()` emits documents in input order with mapping key order preserved, even when decoded in parallel; the first failing document (in order) reports the error. |

## 8. Workflows

| ID | Requirement |
|---|---|
| WF-1 | `workflow(name, steps, opts)` validates unknown `needs`, self-dependency and cycles (reported as `a → b → a`) before running. |
| WF-2 | `.run(input)` runs steps by DAG (sequential when no step has `needs`), up to `maxParallel` (default 4); a failure stops dependents unless `continueOnError`. |
| WF-3 | Guards `{path, equals|exists}` resolve WFNexus-style: longest step-id prefix, JSON-byte equality, unresolved = false (step skipped with a reason). |
| WF-4 | `while`/`until` require `maxIterations` (1..999); exhausting it fails the step. |
| WF-5 | `.toYAML()` emits only fields WFNexus accepts; steps run `<cli> code run-step <module>#<export>`. Guards MUST read only `<id>.ok`, `<id>.exitCode`, `<id>.outcome` or `input.*`; anything else is an export error. `outcomes` are emitted as `exit-codes` `10 + index`. Step context reaches `run-step` as inline assignments in the `run:` line (`CODEMODE_INPUT`, `CODEMODE_STEP_<ID>` per upstream step from `{{ .Steps.<id>.stdout }}`, `CODEMODE_STEP_IDS`, and `CODEMODE_ITEM`/`CODEMODE_INDEX` for foreach), because WFNexus passes a run step only its `run:` text. An explicit `--input` wins over `CODEMODE_INPUT`. |
| WF-6 | Declarative plans require step functions imported from modules; inline closures are an export error. |

## 9. Optional MCP adapter

| ID | Requirement |
|---|---|
| MCP-1 | The core packages MUST NOT import `adapters/mcp`. |
| MCP-2 | It serves JSON-RPC 2.0 over stdio (protocol 2025-06-18): `initialize`, `ping`, `tools/list`, `tools/call`, `notifications/cancelled`; tools `code_capabilities code_describe code_check code_run code_docs`; `code_run` takes an optional `input` (any JSON). A long-lived adapter process lets an agent call `code_run` repeatedly with no new process per call. |

## 10. Security statement

The interpreter is **not a sandbox for hostile code**. It bounds steps, time,
depth and concurrency and exposes only registered operations and
policy-permitted processes, but operations run with the host CLI's
privileges. Hosts MUST register only what they are willing to let a program
do.

## 11. Compatibility

Legacy AOS batch YAML and WFNexus workflow YAML are not reinterpreted by this
library; see `COMPATIBILITY.md`. WFNexus export (WF-5) was validated against
WFNexus's own parser and dry run.
