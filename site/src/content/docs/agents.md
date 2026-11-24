---
title: Use from an agent or skill
description: "How an agent discovers a Code Mode CLI, writes one program, checks it and runs it, including inside Claude Code's Code Mode."
---

Any agent that can run a shell command can use a Code Mode CLI: Claude Code with or without its Code Mode, Codex, Cursor, or a script. The loop is discover, write, check, run.

## Is this CLI Code Mode capable?

```sh
<cli> code capabilities --json
```

If it errors, the CLI does not support Code Mode; use it normally. If it works, you get `{spec, version, fingerprint, language, operations}`. Cache it keyed by `version` + `fingerprint` (and the authorization context you ran under), and refetch when either changes.

```sh
<cli> code describe <op> --json   # one contract: input_schema, output_schema, side_effects
<cli> code docs                   # language reference plus every operation
<cli> code skill                  # a SKILL.md for this CLI, to save next to your own skill
```

## Write one program

```js
const [h, u] = await Promise.all([
  call("health.check", { verbose: true }),
  ops.text.upper({ text: "ok" }),
])
;({ ok: h.ok, u })     // last expression (or `export default`) is the result
```

- Every operation returns a Promise. `await` it; use `Promise.all` for independent calls.
- Operations return JSON values. A failing operation throws, so use `try/catch`, not an `ok` check. (Hosts that wrap commands without JSON-first mode return `{ok, exit, stdout, stderr, json?}`; `describe --json` shows which via `output_schema`.)
- Filter inside the program. Return only what you need; that is what keeps the answer small.

## Check, then run

```sh
<cli> code check prog.js                                  # exit 2 + file:line:col diagnostics; nothing runs
<cli> code run - --input '{"labels":["a","b"]}' --json < prog.js
```

`--json` prints exactly one object:

```json
{ "ok": true, "value": { "os": "darwin", "u": "OK" }, "ms": 0 }
```

Exit codes: **0** ok, **1** the program ran and failed, **2** diagnostics or usage error (nothing ran). Always `check` before `run` for anything with side effects, and read `side_effects` and `authorization` in `describe` first.

## Inside Claude Code Code Mode

[Claude Code's Code Mode](https://code.claude.com/docs/en/code-mode) puts the code loop in the agent: one snippet makes many tool calls. CLI CodeMode puts the loop in the CLI. They compose: from your snippet, send each CLI **one** program instead of many commands.

```js
// not: one process per command, each starting the CLI and printing text
// for (const l of labels) await $`ctx-optimize card ${l} --json`

// yes: one process, JSON operations, only the result comes back
const r = await $`ctx-optimize code run - --input ${JSON.stringify({ labels })} --json < prog.js`.json()
// prog.js: await Promise.all(input.labels.map(l => ops.ctx.card({ args: [l] })))
```

Your code joins results **across** CLIs; each CLI's program does the work **within** that CLI. Model round trips are the same either way. What changes is processes, CPU and how much raw output comes back.

## Calling the same CLI many times in a session

Use its [MCP adapter](/clicodemode/mcp/) if it ships one: `code_run` on a long-lived process means no new process per call (0.12 ms per call vs 4.68 ms for a new process on the demo CLI).

## For skill authors

- Put reusable recipes in [modules](/clicodemode/modules/) baked into your CLI, not in skill prose: `code skill` lists them first, and agents import them by name (`import { x } from "mycli:recipes"`) instead of re-deriving them.
- Ship the generic skill ([SKILL.md](/clicodemode/reference/skill/)) and the CLI-specific one from `<cli> code skill`.
- Tell the agent when to use a program: three or more dependent calls, many independent reads, a loop, or data shaping. A single command stays a single command.
- Show one real program per common task, verified with `code check`.
- Verify the build on PATH with `<cli> code version`.
