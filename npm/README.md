# clicodemode (experimental)

Code Mode for JavaScript and TypeScript CLIs. Register the commands your CLI
already has as operations; an agent then writes one small JavaScript-like
program that calls and composes them, instead of many separate tool calls.

This package is a **thin wrapper around the Go engine compiled to WebAssembly**.
It is not a native TypeScript interpreter. The Go engine owns parsing,
language semantics and validation (diagnostics with file:line:col); this
package only loads it, passes your handlers in and returns its results.
Status: **experimental**, version 0.6.1. The Go project is the reference.

## Install (no npm registry needed)

The package is not published to the npm registry. Pick one:

**a) From a GitHub release asset** (the URL is valid once the v0.6.1 release
exists; the release workflow attaches the tarball produced by `npm pack`
after the WASM is built):

```sh
npm install https://github.com/muthuishere/clicodemode/releases/download/v0.6.1/clicodemode-0.6.1.tgz
```

**b) From source** (needs Go and Node >= 20):

```sh
git clone https://github.com/muthuishere/clicodemode && cd clicodemode
bash scripts/build-wasm.sh
cd npm && npm ci && npm run build
npm pack            # -> clicodemode-0.6.1.tgz, install it anywhere with npm install <path>
```

`npm run verify-pack` packs the tarball, installs it into a fresh directory
and runs the two quickstarts below (JS executed, TS type-checked and executed).

## Quickstart (TypeScript)

<!-- quickstart-ts -->
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

const caps = await cm.capabilities();            // cache by caps.fingerprint
const bad = await cm.check("class A {}");        // diagnostics carry file:line:col
console.log("fingerprint:", caps.fingerprint.length > 0, "check ok:", bad.ok);

const r = await cm.run(
  "const h = await ops.health.check({verbose: input.v}); h.ok",
  { input: { v: true }, timeoutMs: 5000 },
);
console.log("run:", r.ok, r.value);
```

## Quickstart (JavaScript, ESM)

<!-- quickstart-js -->
```js
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

const caps = await cm.capabilities();            // cache by caps.fingerprint
const bad = await cm.check("class A {}");        // diagnostics carry file:line:col
console.log("fingerprint:", caps.fingerprint.length > 0, "check ok:", bad.ok);

const r = await cm.run(
  "const h = await ops.health.check({verbose: input.v}); h.ok",
  { input: { v: true }, timeoutMs: 5000 },
);
console.log("run:", r.ok, r.value);
```

## API

`register`, `operations`, `describe`, `capabilities`, `docs`, `skill`,
`check`, `compile`, `run(src, {file, input, timeoutMs, signal})`,
`eval(expr, {input, timeoutMs, signal})`. `run`/`eval` return
`{ok, value, error, diagnostics, ms, stdout}`.

- Handlers that throw or reject make the program's call reject; the program can `try/catch` it.
- Object results from handlers keep key order as the program sees it.
- Operations may be registered before the WASM loads; it loads lazily on the
  first call. Registering after the first call rebuilds the engine with the full op set.

### Timeouts and cancellation

`timeoutMs` defaults to **30000** ms per call (`0` = no per-call timeout).
`signal` is an `AbortSignal`. On timeout or abort the call resolves with
`ok: false` and any host handler promise still pending is abandoned (a
handler that never resolves cannot hang the call or keep the process alive).
The engine stays usable afterwards, and other concurrent calls are unaffected.

Limit: WASM has no preemption. Timeout and abort take effect when the program
is waiting on a host operation (or between calls into one); they cannot
interrupt a pure CPU loop that never awaits. CPU-bound loops are stopped by
`limits.max_steps` (engine default 10,000,000 steps, an error, not a hang).
Set `limits.max_steps` for untrusted programs. `limits.timeout_ms` is also
available but, being a Go-side timer, can keep the Node process alive until it
expires.

## Security model

- The engine owns language validation: unsupported syntax, unknown operations
  and bad `exec` argv are rejected before anything runs.
- **The host (your handlers) owns authorization, path and exec security.**
  A program can call exactly the operations you registered, with arguments it
  chooses; validate arguments and enforce permissions inside each handler.
- `exec` / `shell` are **denied** in this build: the program gets a diagnostic
  such as `running "echo" is not permitted: this host allows no external
  programs`. Run commands, if you need them, from your own handlers.

## Size, startup, and reuse

- `dist/codemode.wasm` is about 7.8 MB raw, 2.1 MB gzipped (the npm tarball is
  compressed). Node memory after loading the engine is roughly 120 MB RSS.
- Cold start (load WASM and run one program) measured about 55 ms on an Apple
  Silicon laptop with Node 24. Treat this as indicative only.
- **Reuse one engine (`createCodeMode`) per process.** Each engine instance is
  cheap, but program runs are the unit of work; do not create one per call.
  The WASM module itself is loaded once per process.

## Not supported

The program language is a JavaScript subset: no `class`, `new`, `this`,
generators, `switch`, regex, dynamic `import()`. See
[docs/SPEC.md](../docs/SPEC.md) for the full language and semantics. The
`policy` option of the Go API is not exposed here.

## Other runtimes

The Node loader lives in `src/wasm.ts`; `loadWasm(bytes)` boots the engine from
bytes once `wasm_exec.js` has defined `globalThis.Go`. A browser or bundler
entry can fetch the wasm and call it; not shipped yet.
