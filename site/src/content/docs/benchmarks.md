---
title: Benchmarks and methodology
description: "Every number this site quotes, how it was measured, and what is not claimed."
---

All numbers come from the project README. Method for all of them: same work both ways, fresh process per run, medians, Apple Silicon.

"Separate processes" means one CLI process per command, which is how an agent's generated code (for example a Claude Code Code Mode snippet) calls a CLI today.

## ctx-optimize: a 7-call chain

Query, then `card` on the top 5 hits, then `change_plan`.

| | wall | CPU | bytes back |
|---|---|---|---|
| 7 processes, sequential | 68 ms | 167 ms | 28,273 |
| 7 processes, cards in parallel | 47 ms | 164 ms | 28,273 |
| **`ctx-optimize code run`, 1 process** | **33 ms** | **137 ms** | **169** |

## aos: 24 read-only calls and a snapshot

Each `aos` process takes about 66 ms to start.

| | wall | CPU | bytes back |
|---|---|---|---|
| 24 calls, 24 processes sequential | 1,644 ms | 1,228 ms | |
| 24 calls, 24 processes in parallel | 217 ms | 1,493 ms | |
| **24 calls, `aos code run`, 1 process** | **217 ms** | **1,012 ms** | |
| machine snapshot (4 calls), 4 processes parallel | 196 ms | 280 ms | 8,612 |
| **machine snapshot, `aos code run`** | 220 ms | **260 ms** | **158** |

Read this honestly: against sequential processes, one `code run` is faster: about 2x on the ctx-optimize chain (68 ms to 33 ms) and about 7.5x on the 24 aos calls (1,644 ms to 217 ms). Against the same processes spawned in parallel, wall time is about even (the 4-call snapshot is 24 ms slower); the gains are less CPU and a much smaller answer.

## MCP: repeated calls

Calling the demo CLI repeatedly through its persistent MCP server: 0.12 ms per call vs 4.68 ms for a new process.

## Interpreter

| | YAML (`aos batch`) | CodeMode JS | Node.js |
|---|---|---|---|
| 20-step workflow | 9 ms | **3 ms** | 48 ms |
| 1M-iteration loop | n/a | 55 ms | **25 ms** |

Workflows of operation calls start fast because there is no runtime to boot. Tight compute loops are about 2x slower than Node's JIT; put heavy compute in a Go operation.

## YAML streams

12.2 MB, 20,000 Kubernetes-style documents (`js/yaml_bench_test.go`; Node 24, js-yaml 4.1.0, yaml 2.6.1):

| parser | time |
|---|---|
| Node `js-yaml` `loadAll` (warmed) | 213 ms |
| Go `yaml.v3` raw decode | 480 ms |
| CodeMode `stream(...).yaml().count()` (documents decoded in parallel, in order) | 219 ms |
| Node `yaml` `parseAllDocuments().toJS()` | 2,924 ms |

Small input, fresh process: Node + js-yaml 30 ms, `democli code run` with `.yaml()` 4 ms.

## Size

+1.7 MB to a stripped binary (2.5 MB plain Go CLI to 4.2 MB). The optional MCP adapter adds 0.02 MB. One dependency: `gopkg.in/yaml.v3`.

## Not claimed

- **Token or turn savings with a real agent.** Not measured yet; it is the next benchmark on the roadmap. Model round trips are one either way in the comparisons above.
- **Faster than Node in general.** Node wins tight loops.
- **Sandboxing.** See [Security and policy](/clicodemode/security/).

## Reproduce

The interpreter and YAML benchmarks live in `js/perf_bench_test.go` and `js/yaml_bench_test.go` (run with `go test -bench`). The aos and ctx-optimize numbers were taken on their integration branches; wrap each variant in `hyperfine` or a loop of fresh runs and take medians.
