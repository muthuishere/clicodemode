---
title: JS subset and limitations
description: "The JavaScript subset programs are written in, what it leaves out, and how results and limits work."
---

Programs are small JavaScript-like modules run by a pure-Go tree-walking interpreter embedded in the host CLI. There is no Node.js, no npm and no browser API. Supported constructs follow normal JavaScript semantics; unsupported ones are **parse errors that name the construct**, never silent misbehaviour.

## Supported

const/let, numbers, strings, template literals, booleans, null/undefined, arrays, objects, destructuring, spread and rest, all arithmetic, comparison and logical operators including `??`, `?.` and `**`, if/else, ternary, for, for-of, for-in, while, do-while, break/continue, functions, arrow functions, closures, default and rest parameters, try/catch/finally/throw, async/await, `Promise.resolve/reject/all/allSettled/race/any`, `.then/.catch/.finally`, and static `import`/`export` of relative `.js` modules and `.json` data files.

## Not supported

`class`, `new`, `this`, generators, `switch`, `delete`, regular expressions, dynamic `import()`, `eval`, `require`, npm packages, Node or browser APIs. Also not yet: labelled statements, getters/setters, live module bindings, exact microtask ordering.

```sh
$ democli code eval 'new Error("x")'
<eval>:1:1: error: syntax error: `new` is not supported; use factory functions and literals
```

`Error(msg)` works without `new`.

## Globals

`call`, `ops`, `input`, `workflow`, `step`, `print`, `console.log`, `JSON`, `Math`, `Object`, `Array.isArray`, `Array.from`, `String`, `Number`, `Boolean`, `parseInt`, `parseFloat`, `isNaN`, `sleep(ms)`, `pmap(items, fn, {concurrency})`, `Error(msg)`. Where the host permits: `exec`, `execStream`, `shell`, `stream`.

## Calling operations

```js
const h = await call("health.check", { verbose: true })   // by name
const h2 = await ops.health.check({ verbose: true })       // same, as a namespace
const w = await ops["window-list"]()                       // names with hyphens
```

Arguments must be an object (or omitted) and are validated against the operation's input schema before the handler runs. A literal unknown name is a `check` error; a computed one fails at run time.

## Parallelism

`Promise.all` runs operations concurrently, up to `MaxConcurrency` (default 32). For a bounded parallel map, use `pmap`: results come back in input order, and the first rejection stops new items.

```js
const infos = await pmap(names, n => call("pkg.info", { name: n }), { concurrency: 8 })
```

## Result

The default export (a function is called and awaited), otherwise the last top-level expression statement. A promise result is awaited. When the last statement starts with `{`, wrap it in parentheses and lead with `;`:

```js
;({ ok: h.ok, u })
```

Assign values computed inside `try` or loops to a variable and end with that variable. A rejected promise nobody awaited fails the run ("unhandled promise rejection").

## Imports

Only relative `./` or `../` paths ending in `.js` (code) or `.json` (data, default export), confined to the module root. Cycles are an error. Programs from stdin (`run -`) or `eval` cannot import.

```js
import { summarize } from "./steps/report.js"
import thresholds from "./thresholds.json"
```

## Processes and streams

Only if the host's `ExecPolicy` allows them (denied by default).

```js
const r = await exec(["git", "status", "--short"])   // {ok, exitCode, stdout, stderr}

await execStream(["tail", "-n", "10000", "app.log"])
  .jsonl().filter(x => x.level === "ERROR").take(100).writeJSONL()
```

argv is an array and is never shell-interpreted; a non-zero exit is a result, not an exception. Streams are lazy and backpressured: transforms `lines jsonl csv yaml map filter take`; terminals `toArray count first reduce forEach json writeJSONL writeLines`. `take()` and errors stop early and kill the process.

## Limits

| Limit | Default |
|---|---|
| `MaxSteps` (statements, calls, loop iterations) | 10,000,000 |
| `MaxCallDepth` | 512 |
| `MaxConcurrency` (operations in flight) | 32 |
| `MaxStringBytes` (longest string a program builds) | 64 MiB |
| `MaxArrayLen` (longest array) | 10,000,000 |
| `MaxOps` (host operation calls per run) | unlimited |
| `Timeout` | none (use the caller's context) |

Exceeding a limit or cancellation stops the whole run and **cannot be caught**, including inside async code, `sleep`, an operation or `.catch`.

## Speed

A tree-walking interpreter: a 1M-iteration loop takes 55 ms (Node: 25 ms). It is fast to start, which is what matters for workflows of operation calls; heavy compute belongs in a Go operation.
