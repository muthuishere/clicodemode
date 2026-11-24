# CLI CodeMode

**Your agent has Code Mode. Does your CLI?**

Claude Code and OpenAI Codex can compose tool calls through generated programs.
But if that program runs a CLI three times, the CLI still starts three times.
CLI CodeMode lets the CLI itself run the program: register the commands you
already have, expose a `code` command, and an agent sends one small JavaScript
program that calls them inside one process. One JSON value comes back.

Docs and examples: **https://muthuishere.github.io/clicodemode/**

## Three ways to do the same three things

Move a window to another monitor, set the brightness, open YouTube, with
[aos](https://github.com/muthuishere/aos):

| | Agent sends | aos processes | Result |
|---|---|---|---|
| Normal mode | 3 tool calls (`aos window move …`, `aos display brightness …`, `aos open …`) | 3, one after another | 3 text outputs |
| Agent Code Mode (Claude Code) | 1 JS program with three `` $`aos …` `` calls | 3, in parallel | 3 text outputs |
| **CLI CodeMode** | 1 program to `aos code run` | **1** | **1 JSON value** |

```js
// aos code run -
await Promise.all([
  ops["window-move"]({ args: ["Safari"], zone: "1A", monitor: 2 }),
  ops["display-brightness"]({ args: ["70"] }),
  ops.open({ args: ["https://youtube.com"] }),
])
```

Observed with Claude Code 2.1.330 and a wrapper logging every aos start. Agent
Code Mode and CLI CodeMode work together: an agent-side program can call
`aos code run` once instead of aos three times.

## Install

```sh
go get github.com/muthuishere/clicodemode@latest   # Go (the native library)
pip install clicodemode                            # Python: Click, Typer, argparse
npm install clicodemode                            # TypeScript / Node
cargo add clicodemode                              # Rust
```

Python, TypeScript and Rust run the same Go engine compiled to WASM and are
experimental. Your host program still owns authorization and file/process
security.

## Add it to a Go CLI

Wrap the commands you already have; their bodies don't change:

```go
reg := codemode.NewRegistry("mycli 1.4.0")
reg.MustRegister(cmdtree.FromRunner("repo.status", "Show repo status.", runStatus,
    cmdtree.WithJSONResult("--json"),            // return parsed JSON, throw on failure
    cmdtree.WithSideEffects(codemode.SideEffectNone)))

// under your own `code` subcommand:
os.Exit(codemode.NewCLI(codemode.NewEngine(reg), "mycli code").Main(ctx, os.Args[2:]))
```

Then an agent (or you) can:

```sh
mycli code capabilities --json            # every operation + a fingerprint
mycli code check prog.js                  # file:line:col diagnostics, nothing runs
mycli code run - --input '{"repo":"."}'   # run a program from stdin
mycli code skill                          # generated SKILL.md for agents
```

Try the demo: `go run ./cmd/democli code help`.

### Ship your own JS modules

Keep operations small, and put the recipes built on them in JavaScript files
baked into your binary. Agents import them by name, and `code skill` lists
them first:

```go
//go:embed js
var jsFiles embed.FS
reg.MustRegisterModules("mycli", jsFiles, "js") // js/layout.js -> "mycli:layout"
```

```js
import { sideBySide } from "mycli:layout"
await sideBySide("Code", "Safari")
```

Python, TypeScript and Rust take a folder, one file or plain content the same
way: [modules guide](https://muthuishere.github.io/clicodemode/modules/).

## Shipped in real CLIs

- **aos** ([v0.15.2](https://github.com/muthuishere/aos/releases/tag/v0.15.2)):
  `aos code` exposes 254 operations, one per batch verb, through the same path
  as the CLI.
- **ctx-optimize** ([v0.16.1](https://github.com/muthuishere/ctx-optimize/releases/tag/v0.16.1)):
  `ctx-optimize code` over its read-only verbs.

Measured, medians on Apple Silicon:

| | wall | CPU | bytes back |
|---|---|---|---|
| ctx-optimize, 7 calls as 7 processes (sequential) | 68 ms | 167 ms | 28,273 |
| ctx-optimize, 7 calls as 7 processes (parallel) | 47 ms | 164 ms | 28,273 |
| **ctx-optimize, 1 `code run`** | **33 ms** | **137 ms** | **169** |
| aos, 24 calls as 24 processes (sequential) | 1,644 ms | 1,228 ms | |
| aos, 24 calls as 24 processes (parallel) | 217 ms | 1,493 ms | |
| **aos, 1 `code run`** | **217 ms** | **1,012 ms** | |

Against processes spawned in parallel the wall time can be equal; what drops is
process starts, CPU and the output sent back to the model. Adding it to a Go
CLI costs about 1.7 MB of binary.

## What a program can do

A JavaScript subset: `const`/`let`, functions and arrows, destructuring,
spread, `async`/`await`, `Promise.all`, `try`/`catch`, loops, and static
relative imports. Programs are checked before they run: unknown operations,
arguments that break an operation's schema and unsupported syntax are reported
with `file:line:col`, and nothing runs. Limits on steps, time, call depth,
string and array size and operation calls stop a run, and a program cannot
catch them. Running other processes is denied unless the host allows them.

It is **not a sandbox for hostile code**: operations run with your CLI's
privileges, so register only what an agent may do.

## Docs

- Site: https://muthuishere.github.io/clicodemode/
- [docs/SPEC.md](docs/SPEC.md): the normative spec, every requirement tested
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) ·
  [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md) ·
  [CONTRIBUTING.md](CONTRIBUTING.md)

MIT licensed.
