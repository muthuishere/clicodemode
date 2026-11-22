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

**Not yet** (see [docs/ROADMAP.md](docs/ROADMAP.md)): workflow primitives, AOS
batch adapter (proposal parked for owner approval) and WFNexus plan validation.

Try it: `go run ./cmd/democli code help`.

## With Claude Code's Code Mode

[Claude Code's Code Mode](https://code.claude.com/docs/en/code-mode) puts the
code loop in the **agent**: one JavaScript snippet makes many tool calls, so
model round trips drop. When that snippet calls a CLI many times
(`` $`mycli ...` `` x 25) it still starts 25 processes. CLI CodeMode puts the
loop in the **CLI**. The two compose:

```js
// inside Claude Code Code Mode: one process per CLI, not one per command
const r = await $`mycli code run - --input ${JSON.stringify({ ids })} --json < prog.js`.json()
```

| per call, same small operation (demo CLI, ~4 ms startup) | time |
|---|---|
| one process per call (`` $`mycli ...` ``) | 4.68 ms |
| one `code_run` call to a persistent `democli-mcp` session | **0.12 ms** |
| 25 parallel calls: 25 processes vs one `code run` | 85 ms vs **59 ms** |

Real CLIs that read config, authenticate or open connections on start gain
more; that is not measured here. Check the build on a runner's `PATH` with
`mycli code version`.

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
