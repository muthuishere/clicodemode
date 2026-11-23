# clicodemode

Code Mode for JavaScript and TypeScript CLIs. Register the commands your CLI
already has as operations; an agent then writes one small JavaScript-like
program that calls and composes them, instead of many separate tool calls.
The engine is the Go interpreter from this repo compiled to WebAssembly.
Zero runtime dependencies, Node >= 20, ESM only.

```sh
npm install clicodemode
```

## TypeScript

```ts
import { createCodeMode } from "clicodemode";

const cm = createCodeMode({ version: "1.0.0" });
cm.register({
  name: "health.check",
  description: "Report service health",
  sideEffects: "none",
  idempotent: true,
  inputSchema: { type: "object", properties: { verbose: { type: "boolean" } } },
  handler: async ({ verbose }) => ({ ok: true, verbose: !!verbose }),
});

const caps = await cm.capabilities();      // cache by caps.fingerprint
const bad = await cm.check("class A {}");  // bad.diagnostics[0] has file:line:col
const r = await cm.run(
  "const h = await ops.health.check({verbose: input.v}); h.ok",
  { input: { v: true } },
);
console.log(r.ok, r.value);
```

## JavaScript

```js
import { createCodeMode } from "clicodemode";
const cm = createCodeMode({ version: "1.0.0" });
cm.register({ name: "demo.add", description: "add", handler: async ({ a, b }) => a + b });
console.log((await cm.run("await ops.demo.add({a: 1, b: 2})")).value); // 3
```

Handlers that throw reject the program's call; the program can `try/catch` it.
Operations may be registered before the WASM loads; it loads lazily on the
first call (one WASM module per process, one engine per `createCodeMode`).
Registering after the first call rebuilds the engine with the full op set.

## API

`register`, `operations`, `describe`, `capabilities`, `docs`, `skill`,
`check`, `compile`, `run(src, {file, input})`, `eval(expr, {input})`.
`run`/`eval` return `{ok, value, error, diagnostics, ms}`.

## Not supported

The program language is a JavaScript subset: no `class`, `new`, `this`,
generators, `switch`, regex, dynamic `import()`. `exec` and `shell` do not run
inside WASM. See [docs/SPEC.md](../docs/SPEC.md) for the full language and
semantics.

## Other runtimes

The Node loader lives in `src/wasm.ts`; `loadWasm(bytes)` boots the engine from
bytes once `wasm_exec.js` has defined `globalThis.Go`. A browser or bundler
entry can fetch the wasm and call it; not shipped yet.
