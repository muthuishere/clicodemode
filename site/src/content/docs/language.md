---
title: JS subset and limitations
description: "The JavaScript subset programs are written in, what it leaves out, and how results and limits work."
---

Programs are small JavaScript-like modules run by a pure-Go tree-walking interpreter embedded in the host CLI. There is no Node.js, no npm and no browser API. Supported constructs follow normal JavaScript semantics; unsupported ones are **parse errors that name the construct**, never silent misbehaviour.

## Supported

const/let, numbers, strings, template literals, booleans, null/undefined, arrays, objects, destructuring, spread and rest, all arithmetic, comparison and logical operators including `??`, `?.` and `**`, if/else, ternary, for, for-of, for-in, while, do-while, break/continue, functions, arrow functions, closures, default and rest parameters, try/catch/finally/throw, async/await, `Promise.resolve/reject/all/allSettled/race/any`, `.then/.catch/.finally`, `switch`, classes (constructor, methods, fields, `static`, `extends`, `super`), `new`, `instanceof`, `this` in methods, regex literals and `RegExp`, tagged templates, `Map`, `Set`, `Date`, and static `import`/`export` of relative `.js` modules and `.json` data files.

### Regular expressions

Regexes run on Go's RE2 engine, so matching time is linear in the input and a pattern cannot backtrack catastrophically. `test`, `exec`, `match`, `matchAll`, `replace`, `replaceAll`, `split` and `search` accept a regex, with flags `g i m s y`, numbered and named groups, `$1`/`$<name>`/`$&` replacements and function replacers. Lookahead, lookbehind and backreferences are check errors:

```sh
$ democli code check - <<<'const r = /a(?=b)/'
<stdin>:1:11: error: syntax error: invalid regex /a(?=b)/: lookahead and lookbehind are not supported (regexes run on RE2)
```

### Classes

```js
class Repo {
  branch = "main"
  constructor(path) { this.path = path }
  async status() { return ops.repo.status({ path: this.path }) }
  static at(path) { return new Repo(path) }
}
class NotFound extends Error {
  constructor(what) { super(`${what} not found`); this.name = "NotFound" }
}
```

Getters, setters and `#private` members are check errors; use methods and plain fields.

### Tagged templates

A tag is called with `(strings, ...values)`; values are passed separately and never spliced into the strings, so a host can define its own quoting tag:

```js
const q = (s, ...v) => s.reduce((out, part, i) => out + part + (i < v.length ? JSON.stringify(v[i]) : ""), "")
q`branch ${input.ref}`
```

### Results

A `Map` in a run result becomes an object, a `Set` an array and a `Date` an ISO string. Inside the program, `JSON.stringify` follows JavaScript (`{}` for a Map, ISO for a Date).

## Not supported

Generators, `delete`, class getters/setters and `#private` members, regex lookaround and backreferences, dynamic `import()`, `eval`, `require`, npm packages, Node or browser APIs. Also not yet: labelled statements, live module bindings, exact microtask ordering.

```sh
$ democli code eval 'delete input.x'
<eval>:1:1: error: syntax error: `delete` is not supported; build a new object instead
```

## Globals

`call`, `ops`, `input`, `workflow`, `step`, `print`, `console.log`, `JSON`, `Math`, `Object`, `Array`, `String`, `Number`, `Boolean`, `parseInt`, `parseFloat`, `isNaN`, `sleep(ms)`, `pmap(items, fn, {concurrency})`, `Promise`, `Map`, `Set`, `Date`, `RegExp`, `Error`/`TypeError`/`RangeError`/`SyntaxError`. Where the host permits: `exec`, `execStream`, `shell`, `stream`.

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
