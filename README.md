# CLI CodeMode

**Code Mode for every CLI.** A pure-Go library: import it, register the
commands your CLI already has, and your CLI gains discoverable,
self-describing operations plus a small JavaScript-like language that agents
(and people) use to call and compose them.

No Node.js, V8, Goja, cgo or external interpreter. No mandatory MCP or LLM.
Optional MCP server (stdio) in `adapters/mcp`: see [docs/MCP.md](docs/MCP.md).

```sh
go get github.com/muthuishere/clicodemode@latest
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

## Security

The interpreter bounds steps, wall time, call depth and operation
concurrency, and programs can only reach registered operations. It is **not a
sandbox for hostile code**: operations run with your CLI's privileges.
Register only what you would let a program do; use `Policy.Allow` to gate.

Docs: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) ·
[docs/COMPATIBILITY.md](docs/COMPATIBILITY.md) · [code/SKILL.md](code/SKILL.md)
