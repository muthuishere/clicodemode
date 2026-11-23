---
title: "ctx-optimize: six read-only verbs with cmdtree"
description: "How ctx-optimize added `ctx-optimize code` (v0.16.0) by wrapping its read-only verbs with cmdtree: WithJSONResult, WithFlagsLast, WithValidate and WithCheckResult."
sidebar:
  label: ctx-optimize
---

:::note[Shipped]
This integration shipped in [ctx-optimize v0.16.0](https://github.com/muthuishere/ctx-optimize/releases/tag/v0.16.0) (`npm i -g @muthuishere/ctx-optimize`), on clicodemode v0.6.1. The code below is copied from it; elisions are marked.
:::

ctx-optimize answers code questions from a prebuilt knowledge graph: `query`, `card`, `change-plan`, `affected`, `path`, `hubs`. Agents typically chain them: query, then a card on each top hit, then a change plan.

## Registration: the whole file

`internal/app/codemode.go` is 45 lines. Abridged (three of the six registrations shown):

```go
// cmdCode mounts CLI Code Mode (ADR 2026-11-22-code-mode): an agent sends ONE
// program that chains the read-only verbs in a single process. Only verbs that
// never write the store are registered, so a program cannot gather or mutate.
func cmdCode(args []string, stdout, stderr io.Writer) int {
	e := codemode.NewEngine(codeRegistry())
	cli := codemode.NewCLI(e, "ctx-optimize code")
	cli.Stdin, cli.Stdout, cli.Stderr = os.Stdin, stdout, stderr
	return cli.Main(context.Background(), args)
}

func codeRegistry() *codemode.Registry {
	reg := codemode.NewRegistry(version.Version)
	ro := func(name, desc string, run cmdtree.Runner) {
		reg.MustRegister(cmdtree.FromRunner(name, desc, run,
			cmdtree.WithJSONResult("--json"),
			cmdtree.WithFlagsLast(), // these verbs parse positionals first
			cmdtree.WithValidate(validateFlags(name)),
			cmdtree.WithCheckResult(rejectFuzzy()),
			cmdtree.WithSideEffects(codemode.SideEffectNone)))
	}
	ro("ctx.query", "Find symbols/docs by 2-4 terms. args: [\"<terms>\", \"--budget\", \"N\"].",
		func(_ context.Context, a []string, o, e io.Writer) error { return cmdQuery(a, o, e) })
	ro("ctx.card", "Inspect one symbol: signature, doc, callers. args: [\"<label>\"].",
		func(_ context.Context, a []string, o, _ io.Writer) error { return cmdCard(a, o) })
	ro("ctx.change_plan", "Before editing a symbol: callers, blast radius, tests to run. args: [\"<symbol>\"].",
		func(_ context.Context, a []string, o, _ io.Writer) error { return cmdChangePlan(a, o) })
	// ... ctx.affected, ctx.path, ctx.hubs
	return reg
}
```

Dispatch is one line in `internal/app/app.go`: `case "code":`.

Two details worth copying:

- **`WithFlagsLast`** exists because of this integration: these verbs read positionals before flags (`query "terms" --json`), unlike stdlib `flag.Parse`. It was added to the library in v0.5.2.
- **`WithValidate` / `WithCheckResult`** came from this integration too (clicodemode v0.6.1): the wrapped verbs skip the CLI's own front door, so `validateFlags` rejects unknown flags before the run and `rejectFuzzy` rejects a fuzzy symbol match that the CLI only reported on stderr.
- **`change_plan`, not `change-plan`**: `ops.ctx.change-plan` would parse as subtraction.

## Agent program

```js
const q = await ops.ctx.query({ args: [input.terms, "--budget", "1500"] })
const hits = q.hits.slice(0, 3)
const cards = await Promise.all(hits.map(h => ops.ctx.card({ args: [h.node.label] })))
cards.map(c => ({ symbol: c.node.label, at: c.node.source + ":" + c.node.location, sig: c.signature }))
```

`ctx-optimize code check` reports `ok (2 operation(s), 0 import(s))`. A real run against the ctx-optimize repository:

```sh
ctx-optimize code run - --input '{"terms":"change plan"}' --json < prog.js
```

```json
{
  "ok": true,
  "value": [
    { "at": "internal/app/changeplan.go:L34-L45", "sig": "changePlan struct", "symbol": "changePlan" },
    { "at": "internal/app/changeplan.go:L55-L198", "sig": "func cmdChangePlan(args []string, stdout io.Writer) error", "symbol": "cmdChangePlan" },
    { "at": "internal/app/changeplan.go:L47-L51", "sig": "planConfidence struct", "symbol": "planConfidence" }
  ],
  "ms": 15
}
```

## Measured

Query, then `card` on the top 5 hits, then `change_plan` (7 calls). Medians, Apple Silicon.

| | wall | CPU | bytes back |
|---|---|---|---|
| 7 processes, sequential | 68 ms | 167 ms | 28,273 |
| 7 processes, cards in parallel | 47 ms | 164 ms | 28,273 |
| **`ctx-optimize code run`, 1 process** | **33 ms** | **137 ms** | **169** |

## Sources

- Release: [ctx-optimize v0.16.0](https://github.com/muthuishere/ctx-optimize/releases/tag/v0.16.0)
- Registration: [`internal/app/codemode.go`](https://github.com/muthuishere/ctx-optimize/blob/main/internal/app/codemode.go)
- Agent skill: `internal/skills/bundled/ctx-optimize/references/code-mode.md` on the same branch
