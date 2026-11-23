# clicodemode

**Your agent has Code Mode. Does your CLI?**

Give your Node CLI a `code` command. An agent sends it one small JavaScript
program; the program calls your CLI's own commands inside one process and
returns one JSON value. No extra Node process per call, no `child_process`,
no raw output flooding the model.

```sh
npm install clicodemode
```

Docs: https://muthuishere.github.io/clicodemode/

## How it works

1. You **register** the functions behind your commands as operations.
2. An agent (or you) sends a program:

   ```js
   const [status, issues] = await Promise.all([
     ops.repo.status({ path: input.path }),
     ops.issues.list({ state: "open" }),
   ]);
   ({ branch: status.branch, open: issues.length })
   ```

3. clicodemode checks it (unknown operations and bad arguments are reported
   with `file:line:col` and nothing runs), runs it against your handlers, and
   returns `{ ok, value }`.

## Quickstart

Save as `mycli.mjs`. `node mycli.mjs` runs a demo program; `echo '<program>' |
node mycli.mjs code run -` runs whatever an agent sends.

<!-- quickstart-js -->
```js
import { createCodeMode } from "clicodemode";

// The functions your CLI already has.
async function repoStatus({ path }) { return { path, branch: "main", dirty: false }; }
async function listIssues({ state }) { return [{ id: 1, state }, { id: 2, state }]; }

const cm = createCodeMode({ version: "mycli 1.0.0" });
cm.register({
  name: "repo.status",
  description: "Show repo status.",
  sideEffects: "none",
  inputSchema: { type: "object", required: ["path"], properties: { path: { type: "string" } } },
  handler: repoStatus,
});
cm.register({
  name: "issues.list",
  description: "List issues by state.",
  sideEffects: "none",
  inputSchema: { type: "object", properties: { state: { enum: ["open", "closed"] } } },
  handler: listIssues,
});

// `mycli code run -` : read a program from stdin, print one JSON result.
async function codeRun(src, input) {
  const checked = await cm.check(src);
  if (!checked.ok) return { ok: false, diagnostics: checked.diagnostics };
  const r = await cm.run(src, { input, timeoutMs: 10_000 });
  return { ok: r.ok, value: r.value, error: r.error };
}

const demo = `
  const [status, issues] = await Promise.all([
    ops.repo.status({ path: input.path }),
    ops.issues.list({ state: "open" }),
  ]);
  ({ branch: status.branch, open: issues.length })
`;

const [cmd, sub] = process.argv.slice(2);
const src = cmd === "code" && sub === "run"
  ? await new Promise((res) => { let s = ""; process.stdin.on("data", (d) => (s += d)).on("end", () => res(s)); })
  : demo;
console.log(JSON.stringify(await codeRun(src, { path: "." })));
// {"ok":true,"value":{"branch":"main","open":2}}
```

A wrong program never reaches your handlers:

```js
await cm.check('ops.issues.list({ state: "all" })');
// { ok: false, diagnostics: [{ severity: "error", line: 1, col: 17,
//   message: 'issues.list: state must be one of ["open","closed"]',
//   hint: "see `code describe issues.list`" }] }
```

## TypeScript

Types ship with the package. The same CLI, typed:

<!-- quickstart-ts -->
```ts
import { createCodeMode, type Result } from "clicodemode";

type Issue = { id: number; state: string };

const cm = createCodeMode({ version: "mycli 1.0.0" });
cm.register({
  name: "repo.status",
  description: "Show repo status.",
  sideEffects: "none",
  inputSchema: { type: "object", required: ["path"], properties: { path: { type: "string" } } },
  handler: async ({ path }) => ({ path, branch: "main", dirty: false }),
});
cm.register({
  name: "issues.list",
  description: "List issues by state.",
  sideEffects: "none",
  inputSchema: { type: "object", properties: { state: { enum: ["open", "closed"] } } },
  handler: async ({ state }): Promise<Issue[]> => [{ id: 1, state }, { id: 2, state }],
});

const r: Result = await cm.run(`
  const [status, issues] = await Promise.all([
    ops.repo.status({ path: input.path }),
    ops.issues.list({ state: "open" }),
  ]);
  ({ branch: status.branch, open: issues.length })
`, { input: { path: "." }, timeoutMs: 10_000 });
console.log(JSON.stringify({ ok: r.ok, value: r.value, error: r.error }));
```

## Tell the agent what it can call

```js
const caps = await cm.capabilities(); // every operation + a fingerprint to cache on
const skill = await cm.skill();       // a SKILL.md describing your CLI's operations
const docs = await cm.docs();         // the language reference
```

Print `skill` from a `mycli code skill` command and agents learn your CLI in
one read.

## API

| method | returns |
|---|---|
| `register(op)` | adds an operation: `name` (dotted, lower case), `description`, `handler`, optional `inputSchema`, `sideEffects` (`none` / `local` / `external`), `idempotent` |
| `check(src)` / `compile(src)` | `{ ok, diagnostics }`, nothing runs |
| `run(src, { input, timeoutMs, signal, file })` | `{ ok, value, error, diagnostics, ms, stdout }` |
| `eval(expr, opts)` | same as `run`, for one expression |
| `operations()` / `describe(name)` / `capabilities()` | the registered contract |
| `docs()` / `skill()` | text for humans and agents |

- A handler that throws rejects the program's call; the program can
  `try/catch` it.
- `timeoutMs` defaults to 30,000 (0 = none); `signal` is an `AbortSignal`.
  A pure CPU loop that never awaits is stopped by the engine's step limit
  (10M steps), not by the timeout.
- Create one engine per process and reuse it.

## The language

A JavaScript subset: `const`/`let`, functions and arrows, destructuring,
spread, template literals, `async`/`await`, `Promise.all`, `try`/`catch`,
loops, `switch`, classes with `new`/`this`/`extends`, regexes (RE2, so no
catastrophic backtracking), tagged templates, `Map`/`Set`/`Date`, `pmap` for
bounded parallel maps. Not supported (a check error, never silent):
generators, `delete`, getters/setters, regex lookaround and backreferences,
`eval`, dynamic `import()`. Full spec: [SPEC.md](https://github.com/muthuishere/clicodemode/blob/main/docs/SPEC.md).

## Security

- A program can call only the operations you registered. Validate arguments
  and check permissions inside each handler: **your handlers own
  authorization and file/process security.**
- `exec` and `shell` are denied in this package.
- It is not a sandbox for hostile code, and WebAssembly does not make it one.

## About this package

The engine is the Go library [clicodemode](https://github.com/muthuishere/clicodemode)
compiled to WebAssembly; this package loads it and passes your handlers in.
Go is the primary implementation; the Node package is **experimental** and
follows the same version (0.7.0). The `.wasm` is about 2.1 MB gzipped; a cold
start (load plus one run) takes about 55 ms on Apple Silicon with Node 24.
Node 20 or newer.

Also for [Go](https://pkg.go.dev/github.com/muthuishere/clicodemode),
[Python](https://pypi.org/project/clicodemode/) and
[Rust](https://crates.io/crates/clicodemode). MIT licensed.
