---
title: CLI reference
description: "Every `<cli> code` subcommand, its flags, output and exit codes."
---

Every host mounts the same subcommands under its own prefix (`mycli code`, `aos code`, `ctx-optimize code`). Output below is from the demo CLI, `democli code`.

```txt
democli code operations [--json]            list operations
democli code describe <operation> [--json]  show one operation's contract
democli code capabilities --json            full machine-readable capability document
democli code docs                           language and API reference for writing programs
democli code check <program.js|->           parse and validate; nothing runs
democli code compile <program.js|->         check, then print the compiled plan and cache key
democli code run <program.js|-> [--json] [--input JSON]
                                     run a program (--input becomes the global "input")
democli code eval <expression> [--json]     evaluate one expression
democli code version                        CLI, library and spec version
democli code skill                          print a SKILL.md for agents driving this CLI
democli code run-step <module.js>#<export> [--input JSON] [--outcomes a,b]
                                     call one exported function with {input, steps:{}}
```

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. |
| 1 | The program ran and failed: uncaught error, failed operation, unhandled rejection, limit hit. |
| 2 | Usage error or diagnostics. **Nothing ran.** |
| 10 + i | `run-step --outcomes`: index of the returned outcome. Never 0 when `--outcomes` is set. |

## Subcommands

### `operations [--json]`

```txt
health.check  none  Report whether this machine is healthy.
text.echo     none  Run `democli echo`: join args with --sep, optionally upper-cased and repeated.
text.upper    none  Upper-case a string.
time.sleep    none  Wait for ms milliseconds (cancellable). Returns the ms waited.
```

Columns: name, side effects, description. Alias: `ops`.

### `describe <op> [--json]`

One operation's contract: description, side effects, idempotency, input and output schema, examples. An unknown name exits 2 and suggests the closest name within edit distance 3.

### `capabilities --json`

`{spec, version, fingerprint, language, operations}`. The fingerprint (`sha256:` + 32 hex) changes when any operation is added, removed or its contract changes. Alias: `caps`.

### `docs`

Language reference plus every operation with schemas and examples. Paste it into a prompt, or let the agent run it.

### `check <file|->` and `compile <file|->`

`check` prints `file:line:col: severity: message (hint)` diagnostics, or `ok: <file> (N operation(s), M import(s))`. `check --json` prints a JSON array (`[]` when clean). `compile` also prints the plan: hash, imports, referenced operations, cache key. Set `CODEMODE_CACHE_DIR` to cache plans.

### `run <file|-> [--json] [--input JSON]`

Runs the program. `--input` (or `--input=JSON`) becomes the global `input`; invalid JSON exits 2. With `--json`, exactly one object:

```json
{ "ok": true, "value": { "os": "darwin", "u": "OK" }, "ms": 0 }
```

Without `--json`, a string result prints raw, other results as JSON, nothing for null/undefined. `print`/`console.log` go to the engine's stdout writer, never into the `--json` framing.

### `eval <expression> [--json]`

```sh
$ democli code eval '1 + 2'
3
```

### `version`

```txt
democli 0.1.0 (clicodemode 0.5.2, spec codemode/v0)
```

Use it to confirm which build is on PATH.

### `skill`

Prints a SKILL.md for this CLI: the generic Code Mode instructions plus this CLI's operations with one-line contracts, grouped for large CLIs.

### `run-step <module.js>#<export> [--input JSON] [--outcomes a,b]`

Calls one exported function with `{input, steps: {}}` and prints its JSON result. Used by [WFNexus export](/clicodemode/workflows/#export-to-wfnexus).

## Environment

| Variable | Effect |
|---|---|
| `CODEMODE_CACHE_DIR` | Cache compiled plans for `check` and `compile` (off by default). |
| `CODEMODE_INPUT`, `CODEMODE_STEP_<ID>`, `CODEMODE_STEP_IDS`, `CODEMODE_ITEM`, `CODEMODE_INDEX` | Step context for `run-step` in exported workflows. |
