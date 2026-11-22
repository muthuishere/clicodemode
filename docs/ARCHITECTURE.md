# Architecture

## Packages

| package | role |
|---|---|
| `js/` | The language: lexer, Pratt-style parser, AST, tree-walking evaluator, builtins. Knows nothing about CLIs. |
| `codemode/` | Registry of operations, engine (binds `call`/`ops` into the interpreter, policy, module loader, static checks, compile plan), CLI subcommands, docs text. |
| `adapters/cmdtree/` | Wraps existing commands (`func(ctx, args, stdout, stderr) error`, stdlib `flag.FlagSet`) as operations; imports `codemode`, never the reverse. |
| `cmd/democli/` | Reference host showing how an existing CLI embeds it. |

Planned: `codemode/exec` (processes + streams), `codemode/workflow`
(workflow primitives and portable plans), `adapters/aosbatch`,
`adapters/wfnexus`, `adapters/mcp` (optional).

## Operations

An `Operation` is name (dotted lower-case), description, input/output JSON
Schema, side effects (`none|local|external`, default `local` when unset),
idempotency, authorization scope, examples, handler
`func(ctx, map[string]any) (any, error)`. Handlers wrap a CLI's existing
functions; there is never a second implementation of a verb.

`Capabilities()` returns every operation plus `fingerprint` =
sha256(spec version, CLI version, every operation's public contract). Agents
cache capability metadata keyed by (CLI version, fingerprint, authorization
context). The compiled-program cache key is sha256(program + import sources,
compiler version, capability fingerprint). No secret values are ever part of
either.

### Caches (`codemode/cache.go`)

`Cache{Dir}` (default `os.UserCacheDir()/codemode`, `nil` = off) holds two
kinds of JSON file, written atomically with mode 0600:

- `capabilities/`: `Load/SaveCapabilities(CapabilityCacheKey(version,
  fingerprint, authContext))`. The auth context is hashed into the key and
  never written.
- `programs/`: `Engine.CompilePlan` (used by `code check`/`compile`) stores the
  plan (hash, imports, operations, cache key) and diagnostics under
  `Program.CacheKey` + the exec/module policy that affects diagnostics. An
  index entry (entry path + source) lists the imports; a lookup re-reads them
  and recomputes the key, so editing any import is a miss. Parsing and the
  operation/policy walk are skipped on a hit. Results with failed imports or
  syntax errors, approvals and run results are never cached. `run` always
  compiles fresh (it needs the AST).

The CLI turns the plan cache on from `CODEMODE_CACHE_DIR` when the host left
`Engine.Cache` nil.

### Command-tree adapters

`cmdtree.FromRunner(name, desc, run, opts...)` runs the existing command with
captured stdout/stderr and returns `{ok, exit, stdout, stderr, json?, error?}`
(`json` when stdout is valid JSON; errors with `ExitCode() int` set `exit`).
Input is `{args: [strings]}` plus flags declared with `WithFlagSet`, passed as
`--name=value` before positionals. `FromFlagSet` derives the schema from a
`flag.FlagSet` (bool, int/uint, float, string, duration) with defaults and
usage text; the FlagSet is only read, never parsed, so parallel calls are safe
as long as the command builds its own FlagSet per call (the usual pattern).
Cobra hosts adapt `cmd.SetArgs/SetOut/SetErr/ExecuteContext` into a runner
(see the package doc).

## Language

A deliberately limited JavaScript subset with normal JS semantics for what it
supports (see `codemode docs`). Unsupported constructs (`class`, `new`,
`this`, generators, `switch`, `delete`, dynamic `import()`, `eval`, regex)
are rejected at parse time with a pointed message rather than misbehaving.

Program result: the default export (a function is called and awaited), else
the last expression statement's value (so `code eval` and short scripts just
work).

Modules: static `import`/`export` only; relative `./` paths ending `.js`
(code) or `.json` (data as default export); confined to the module root
(default: the entry file's directory); cycles are an error; bindings are
copied at import time (not live).

### Concurrency model

Each async function call and each promise callback runs on its own
goroutine; a single interpreter lock means only one runs JS at a time. JS code
runs to completion between `await`s, exactly like JS. `await` (always, even on
plain values) and host operations release the lock, so operation I/O really
overlaps: `Promise.all` over four 300 ms operations takes ~300 ms. Operations
in flight are bounded by `Limits.MaxConcurrency` (default 8).

Deviation from browsers/Node: relative ordering of callbacks between
*independent* promises is unspecified (there is no microtask queue). Code that
depends on that ordering is fragile in JS too.

### Limits and cancellation

`Limits{MaxSteps, MaxCallDepth, MaxConcurrency, Timeout}` plus the caller's
`context.Context`. Exceeding a limit or cancellation stops the whole run and is
**not catchable** by the program, including when it surfaces as a rejection
from `sleep`, an operation or an async callee (`catch` and `.catch` refuse to
run once the run is stopped). Every pending await, sleep and operation
selects on the context; operations receive it and must honour it. A
rejected promise nobody awaited or `.catch`ed fails the run ("unhandled
promise rejection ... did you forget `await`?"). A panicking handler becomes
a rejection, not a host crash.

### Diagnostics

Everything carries `file:line:col`. `check` reports parse errors and static
problems (unknown operation names in `call("literal")` and `ops.a.b(...)`,
with did-you-mean). Run-time errors carry the throw site. Exit codes: 0 ok,
1 program failed, 2 usage error / diagnostics (nothing ran).

## Security stance

In-process interpreter, not a hostile-code sandbox. Programs reach the
outside world only through registered operations (and, later, explicitly
permitted executables), gated by `Policy.Allow`. File/env/secret/process
access is host policy, never ambient.

## Planned layers (design carried from the spec)

**Execution** (`codemode/exec`): permitted executables with argv arrays (no
shell unless separately authorized), `execStream`, streaming JSONL/JSON/text/
YAML/CSV transforms with backpressure and early cancellation:
`exec(["kubectl","logs","-f",pod]).jsonl().filter(x => x.level === "ERROR").take(100).writeJSONL()`.
Processes and streams close on cancellation. `pmap(items, fn, {concurrency})`.

**Workflows as JS primitives** (implemented in `js/workflow.go`, installed
by `codemode.Engine`; `Engine.WorkflowCLI` names the executable in emitted
YAML):

```js
import { inspect, report } from "./steps/health.js"
export default workflow("health-check", [
  step("inspect", inspect, { retry: { attempts: 3, backoff: "2s" } }),
  step("report", report, { needs: ["inspect"], when: { path: "Steps.inspect.ok", equals: true } }),
]).run()
```

Two forms, kept distinct: *imperative* (step bodies are JS callbacks, runs
in-process, not durable) and *declarative plans* (serializable: step refs are
static module refs `./steps/health.js#inspect`, guards are data
`{path, equals|exists}`, loops are bounded `foreach/while/until` with
`max_iterations`). Declarative plans are what WFNexus ingests; WFNexus remains
the sole owner of durable scheduling, approvals, recovery, run history,
triggers, agents, memory, secrets and policy.

How it is built: `step()` validates options eagerly; `workflow()` validates
ids, self-dependencies and cycles (reported as `a → b → a`). `.run()` is a
Go scheduler on its own goroutine: ready steps start up to `maxParallel`
(default 4), each invocation takes the interpreter lock only to call the
step fn and waits for its promise unlocked (bounded by `timeout`), retry
backoff waits end on cancellation. Guards are evaluated on JSON copies of
step outputs, WFNexus-style (longest step id, `json.Marshal` equality,
unresolved = false). `.plan()`/`.toYAML()` resolve step fns to
`module#export` using `Function.ExportModule/ExportName`, which the
interpreter records the first time a function value is exported; refs are
relative to the entry program's directory. `toYAML` emits only fields
WFNexus accepts (`maxParallel`, which has no field in that subset, is not
emitted).

**Discovery**: one global `code/SKILL.md` for every compatible CLI; generated
per-CLI skill (`code skill`); optional MCP adapter (never required).
`code run-step <module.js>#<export> [--input JSON]` calls one exported step
with `{input, steps: {}}` through the normal loader rules; it is the entry
point declarative plans exported to WFNexus invoke.
