---
title: "aos: 254 operations from a batch table"
description: "How aos added `aos code` by registering every batch verb as an operation through its existing invoke path. Open PR."
sidebar:
  label: aos
---

:::caution[Open PR, not released]
This integration is [muthuishere/aos#123](https://github.com/muthuishere/aos/pull/123) on branch `feat/aos-code-json`. The code below is copied from that branch; it may change before merge.
:::

aos drives the machine (windows, display, processes, files, capture). It already had `aos batch`, a YAML runner over a table of verbs (`batchVerbs`). Code Mode reuses that table.

## Registration

From `internal/groups/code.go`:

```go
func newCodeEngine(c *cli.Ctx) *codemode.Engine {
	reg := codemode.NewRegistry(c.Version)
	for _, op := range codeOperations(c) {
		reg.MustRegister(op)
	}
	eng := codemode.NewEngine(reg)
	eng.Stdout = c.Stderr // print() and console.log go to stderr, never into the result
	return eng
}

// codeOperations is one operation per batch verb, named as batch names it.
func codeOperations(c *cli.Ctx) []codemode.Operation {
	verbs := batchVerbs(c.Registry)
	// ... skip "do", which would bypass the exclusions
	for _, n := range names {
		v := verbs[n]
		ops = append(ops, codemode.Operation{
			Name:        n,
			Description: codeDescription(v),
			InputSchema: codeSchema(v), // args + every documented flag
			SideEffects: codemode.SideEffectLocal,
			Handler:     codeHandler(c, v),
		})
	}
	return ops
}
```

Each handler builds the same argv batch would and calls the same `cli.Invoke`, so batch and code cannot disagree about a command. A test (`TestCodeArgvMatchesBatch`) holds the two together.

## JSON-first handler

```go
if _, set := in["json"]; hasJSON && !set {
	argv = append(argv, "--json") // added when the command documents --json
}
out := cli.Invoke(c, argv, "")
if out.Exit != 0 {
	return nil, fmt.Errorf("exit %d: %s", out.Exit, codeTail(out.Stderr, out.Stdout))
}
var j any
if json.Unmarshal([]byte(out.Stdout), &j) == nil && strings.TrimSpace(out.Stdout) != "" {
	return j, nil
}
return strings.TrimSpace(out.Stdout), nil
```

## What is not exposed

Same as batch: the system groups (`power`, `system`, `pkg`, `service`, `network`), the `do` escape hatch, and `code` itself. External `exec` and `shell` are off (empty `ExecPolicy`): aos's own commands are the whole surface.

## Agent programs

Input is `{args: [words], <flag>: value}`. Operation names are the batch verb names, so most need bracket syntax.

```js
// three actions, one process (verified with `aos code check`: ok, 3 operations)
await Promise.all([
  ops["window-move"]({ args: ["Safari"], zone: "1A", monitor: 2 }),
  ops["display-brightness"]({ args: ["70"] }),
  ops.open({ args: ["https://youtube.com"] }),
])
```

```js
// machine snapshot: four reads, a small answer
const [wins, procs, disks, apps] = await Promise.all([
  ops["window-list"](), ops["process-list"]({ sort: "cpu", top: "5" }),
  ops["disk-list"](), ops["app-list"]({ running: true }),
])
;({ windows: wins.length, topCpu: procs.map(p => p.name), disks: disks.length, runningApps: apps.length })
```

```js
// failures throw
try { await ops["window-focus"]({ args: ["Notepad"] }); ({ focused: true }) }
catch (e) { ({ focused: false, why: e.message }) }
```

## Measured

Medians, Apple Silicon. Each `aos` process takes about 66 ms to start.

| | wall | CPU | bytes back |
|---|---|---|---|
| 24 read-only calls, 24 processes sequential | 1,644 ms | 1,228 ms | |
| 24 calls, 24 processes in parallel | 217 ms | 1,493 ms | |
| **24 calls, `aos code run`, 1 process** | **217 ms** | **1,012 ms** | |
| machine snapshot (4 calls), 4 processes parallel | 196 ms | 280 ms | 8,612 |
| **machine snapshot, `aos code run`** | 220 ms | **260 ms** | **158** |

Against parallel spawning the wall time is even (the snapshot is slightly slower). The gains are CPU and bytes returned.

## Sources

- PR: [muthuishere/aos#123](https://github.com/muthuishere/aos/pull/123)
- Registration: `internal/groups/code.go` on `feat/aos-code-json`
- Agent skill: `internal/skill/assets/aos/references/code.md` on the same branch
