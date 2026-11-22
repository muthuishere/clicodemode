---
name: code
description: Drive any CLI that supports Code Mode (`<cli> code ...`) by discovering its operations and writing a small JavaScript-like program that calls and composes them. Use when a task needs several calls to one CLI, parallel calls, loops or data shaping over its results, instead of running commands one at a time.
---

# Code Mode for any CLI

A Code Mode CLI answers `<cli> code capabilities --json`. If it errors, the
CLI does not support Code Mode; use it normally.

## 1. Discover (cache it)

```sh
<cli> code capabilities --json   # operations + schemas + fingerprint
<cli> code describe <op> --json  # one contract
<cli> code docs                  # language reference
```

Cache the capability document keyed by `version` + `fingerprint` (and the
authorization context you ran under). Re-fetch when either changes. Never put
secret values in a cache. `<cli> code skill` prints a CLI-specific skill
(its operations with one-line contracts) you can save next to this one.

Operations wrapped from existing commands return
`{ok, exit, stdout, stderr, json?}`: check `ok`, and read `json` when the
command printed JSON.

## 2. Write a program

```js
const [h, u] = await Promise.all([
  call("health.check", { verbose: true }),
  ops.text.upper({ text: "ok" }),
])
;({ ok: h.ok, u })     // last expression (or `export default`) is the result
```

- Every operation returns a Promise: `await` it; use `Promise.all` for
  independent calls (they really run in parallel, bounded by the host).
- Arguments must match the operation's input schema.
- Supported: const/let, objects/arrays, destructuring, spread, closures,
  arrow functions, if/for/for-of/while, try/catch/finally, async/await,
  Promises, static `import` of `./x.js` / `./x.json`.
- Not supported: class, new, this, switch, regex, eval, npm, Node/browser APIs.

Processes (only where the host permits): `await exec(["git","status"])` gives
`{ok, exitCode, stdout, stderr}`; streams:
`await execStream([...]).jsonl().filter(f).take(100).toArray()`.

## 3. Check, then run

```sh
<cli> code check prog.js        # exit 2 + file:line:col diagnostics; fix and repeat
<cli> code run prog.js --json   # {ok, value, error, diagnostics, ms}
echo '<program>' | <cli> code run -
<cli> code eval '<expression>'
<cli> code run-step steps.js#inspect --input '{"x":1}'  # call one export with {input, steps:{}}
```

`CODEMODE_CACHE_DIR=<dir>` caches compiled plans for check/compile (off by
default).

Exit codes: 0 ok, 1 the program ran and failed, 2 errors (nothing ran).

## Inside Claude Code's Code Mode (or any agent that writes code)

If your own environment already lets you write code that makes tool calls
(Claude Code Code Mode: `` $`...` `` runs a shell command), still send work for a
Code Mode CLI as **one** program per CLI, not many commands:

```js
// not: 25 separate processes, each re-reading config and re-authenticating
// for (const p of pods) await $`kubectl get pod ${p} -o json`

// yes: one process, typed operations, only the result comes back
const r = await $`kubectl code run - --input ${JSON.stringify({ pods })} --json < prog.js`.json()
```

- Pass data in with `--input JSON` (the program's global `input`); read the
  result from `--json` (`{ok, value, error, diagnostics}`).
- Your code joins results **across** CLIs; each CLI's program does the work
  **within** that CLI.
- Calling the same CLI many times in one session: use its MCP server
  (`code_run`, optional `input`) so there is no new process per call.
- Check the build on PATH with `<cli> code version`.

## Rules

- Always `check` before `run` for anything with side effects; read
  `side_effects` and `authorization` in `describe` first.
- Step budget, timeout and cancellation are hard stops; don't try to catch them.
- The interpreter is not a sandbox: operations act with the CLI's real
  permissions.
