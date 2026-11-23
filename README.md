# CLI CodeMode

**Code Mode for every CLI.** A pure-Go library: import it, register the
commands your CLI already has, and your CLI gains discoverable,
self-describing operations plus a small JavaScript-like language that agents
(and people) use to call and compose them.

No Node.js, V8, Goja, cgo or external interpreter. No mandatory MCP or LLM.
Optional MCP server (stdio) in `adapters/mcp`: see [docs/MCP.md](docs/MCP.md).

```sh
go get github.com/muthuishere/clicodemode@latest && go mod tidy
```

```go
reg := codemode.NewRegistry("mycli 1.4.0")
reg.MustRegister(codemode.Operation{
    Name:        "health.check",
    Description: "Report whether this machine is healthy.",
    InputSchema: json.RawMessage(`{"type":"object","properties":{"verbose":{"type":"boolean"}}}`),
    SideEffects: codemode.SideEffectNone,
    Idempotent:  true,
    Handler: func(ctx context.Context, args map[string]any) (any, error) {
        return existingHealthCheck(args["verbose"] == true), nil // wrap, don't rewrite
    },
})
// mount under your own `code` subcommand:
os.Exit(codemode.NewCLI(codemode.NewEngine(reg), "mycli code").Main(ctx, os.Args[2:]))
```

```sh
mycli code operations                 # list
mycli code describe health.check      # contract (--json for machines)
mycli code capabilities --json        # everything + a fingerprint to cache on
mycli code docs                       # language + operation reference
mycli code check prog.js              # diagnostics with file:line:col, nothing runs
mycli code compile prog.js            # checked plan + cache key
mycli code run prog.js                # run ('-' reads stdin)
mycli code eval '1 + 2'
mycli code skill                      # generated SKILL.md for this CLI
mycli code run-step steps.js#inspect --input '{"x":1}'   # one exported step
```

Already have commands? Wrap them instead of writing handlers
(`adapters/cmdtree`, stdlib only):

```go
fs := flag.NewFlagSet("status", flag.ContinueOnError) // or your existing one
fs.Bool("short", false, "short output")
reg.MustRegister(cmdtree.FromFlagSet("repo.status", "Show repo status.", fs,
    func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
        return runStatus(args, stdout) // existing command body, unchanged
    }))
// call: await ops.repo.status({short: true, args: ["."]})
//   ->  {ok, exit, stdout, stderr, json?}
```

Caches: set `CODEMODE_CACHE_DIR` to cache compiled plans for `check`/`compile`
(off by default), or set `Engine.Cache`. `Cache.Load/SaveCapabilities` with
`CapabilityCacheKey(version, fingerprint, authContext)` caches capability
documents. Neither stores secrets or approvals.

```js
// prog.js
import { summarize } from "./steps/report.js"
const [h, disk] = await Promise.all([
  call("health.check", { verbose: true }),
  ops.disk.usage({ path: "/" }),
])
export default summarize(h, disk)
```

## Status

v0.3. **Done**: operation registry, describe/operations/
capabilities with fingerprint, the interpreter (lexer, Pratt parser,
evaluator: const/let, objects/arrays, destructuring, spread, closures,
try/catch/finally, async/await, Promises incl. all/race/allSettled/any,
static relative imports of .js and .json), budgets/timeouts/bounded
concurrency/cancellation, input-schema validation, policy hook, CLI, demo.
`exec(argv)` / `execStream(argv)` / `shell()` behind an opt-in `ExecPolicy`
(deny by default, minimal child env), lazy backpressured streams
(`.jsonl() .csv() .yaml() .map .filter .take .toArray .reduce .writeJSONL()`) that kill
the process on early stop or cancel; `cwd` is confined to the policy directory.
`pmap(items, fn, {concurrency})` for bounded, order-preserving parallel maps.
Command-tree adapters (`cmdtree.FromRunner`, `cmdtree.FromFlagSet`),
capability and compiled-plan caches, `code skill` and `code run-step`.
Optional MCP server (`adapters/mcp`).

**In use**: [aos](https://github.com/muthuishere/aos) (`aos code`, 254
operations) and [ctx-optimize](https://github.com/muthuishere/ctx-optimize)
(`ctx-optimize code`) embed it; WFNexus runs exported plans (validated with real
runs). Roadmap: [docs/ROADMAP.md](docs/ROADMAP.md).

Try it: `go run ./cmd/democli code help`.

## With Claude Code's Code Mode

[Claude Code's Code Mode](https://code.claude.com/docs/en/code-mode) puts the
code loop in the **agent**: one JavaScript snippet makes many tool calls, so
model round trips drop. But each CLI command that snippet runs
(`` $`aos window list --json` ``) is still a separate process that starts the CLI,
parses its arguments and prints text the snippet has to parse back.

CLI CodeMode puts the loop in the **CLI**: one `code run`, one process, and the
operations return JSON objects in-process. The two compose:

```js
// inside Claude Code Code Mode: one process per CLI, not one per command
const r = await $`aos code run - --input ${JSON.stringify(input)} --json < prog.js`.json()
```

Model round trips are the same either way (one). What changes is processes,
CPU, and how much raw output flows back.

### Measured on real CLIs

Same task both ways, medians, Apple Silicon. "Code Mode style" = one process per
command, the way a Claude Code Code Mode snippet calls a CLI today.

**ctx-optimize**: query, then `card` on the top 5 hits, then `change_plan`
(7 calls):

| | wall | CPU | bytes back |
|---|---|---|---|
| 7 processes, sequential | 68 ms | 167 ms | 28,273 |
| 7 processes, cards in parallel | 47 ms | 164 ms | 28,273 |
| **`ctx-optimize code run`, 1 process** | **33 ms** | **137 ms** | **169** |

**aos** (each `aos` process takes ~66 ms to start):

| | wall | CPU | bytes back |
|---|---|---|---|
| 24 read-only calls, 24 processes sequential | 1,644 ms | 1,228 ms | |
| 24 calls, 24 processes in parallel | 217 ms | 1,493 ms | |
| **24 calls, `aos code run`, 1 process** | **217 ms** | **1,012 ms** | |
| machine snapshot (4 calls), 4 processes parallel | 196 ms | 280 ms | 8,612 |
| **machine snapshot, `aos code run`** | 220 ms | **260 ms** | **158** |

The program for the snapshot reads objects directly:

```js
const [wins, procs, disks, apps] = await Promise.all([
  ops["window-list"](), ops["process-list"]({ sort: "cpu", top: "5" }),
  ops["disk-list"](), ops["app-list"]({ running: true }),
])
;({ windows: wins.length, topCpu: procs.map(p => p.name), disks: disks.length, runningApps: apps.length })
```

So: against one process per command run sequentially, CLI CodeMode is several
times faster; against the same commands spawned in parallel the wall time is
about even, with less CPU and a tiny answer instead of the raw output. Calling
the same CLI repeatedly from one session through its persistent MCP server costs
0.12 ms per call vs 4.68 ms for a new process (demo CLI).

### JSON objects, not text

Operations return JSON values; failures throw. A host wrapping existing
commands gets this with one option:

```go
reg.MustRegister(cmdtree.FromRunner("ctx.query", "Find symbols", runQuery,
    cmdtree.WithJSONResult("--json"), // append --json, return the parsed value, throw on failure
    cmdtree.WithFlagsLast(),           // for commands that read positionals first
    cmdtree.WithSideEffects(codemode.SideEffectNone)))
```

Check which build a runner sees with `mycli code version`.

## Performance

Same work, fresh process each run, medians, Apple Silicon:

| | YAML (`aos batch`) | **CodeMode JS** | Node.js JS |
|---|---|---|---|
| 20-step workflow | 9 ms | **3 ms** | 48 ms |
| 1M-iteration loop (YAML can't express it) | n/a | **55 ms** | 25 ms |

Workflows (steps that call operations) are fastest on CodeMode: no runtime to
boot. Tight compute loops are within ~2x of Node's JIT; heavy compute belongs
in a Go operation, which runs at native speed.

Adding CodeMode to a CLI costs **+1.7 MB** (stripped binary: 2.5 MB plain Go
CLI to 4.2 MB); the optional MCP adapter adds 0.02 MB more. One dependency:
`gopkg.in/yaml.v3`.

### YAML streams vs Node.js

12.2 MB, 20,000 Kubernetes-style documents, Apple Silicon, medians
(`js/yaml_bench_test.go`; Node 24, js-yaml 4.1.0, yaml 2.6.1):

| parser | time |
|---|---|
| Node `js-yaml` `loadAll` (V8 JIT, warmed) | 213 ms |
| Go `yaml.v3` raw decode | 480 ms |
| **CodeMode** `stream(...).yaml().count()` (end to end, ordered JS objects, documents decoded in parallel) | 219 ms |
| Node `yaml` `parseAllDocuments().toJS()` | 2,924 ms |

Small input, fresh process each run (what a CLI call actually costs):
Node + js-yaml **30 ms**, `democli code run` with `.yaml()` **4 ms**.

So: for one-shot CLI and agent calls CodeMode is ~7x faster because there is
no runtime to boot. On big files CodeMode matches js-yaml by decoding
independent documents in parallel (in order) on all cores; no pure-Go YAML
library was faster single-threaded (yaml.v2 similar, goccy/go-yaml ~3x
slower). CodeMode is ~13x faster than the `yaml` package.

## Security

The interpreter bounds steps, wall time, call depth and operation
concurrency, and programs can only reach registered operations. It is **not a
sandbox for hostile code**: operations run with your CLI's privileges.
Register only what you would let a program do; use `Policy.Allow` to gate.

Docs: [docs/SPEC.md](docs/SPEC.md) (normative, every requirement tested) ·
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) ·
[docs/COMPATIBILITY.md](docs/COMPATIBILITY.md) · [code/SKILL.md](code/SKILL.md)
