---
title: FAQ
description: "How CLI CodeMode relates to Claude Code Code Mode, MCP and shell scripts, and what it does not do."
---

## Is this the same as Claude Code's Code Mode?

No, and they work together. [Claude Code's Code Mode](https://code.claude.com/docs/en/code-mode) is **agent-side**: Claude writes JavaScript that makes several tool calls in one step, so model round trips drop. But each CLI command that code runs (`` $`aos window list --json` ``) is still a separate process that starts the CLI, parses arguments and prints text.

CLI CodeMode is **CLI-side**: the CLI runs one program and calls its own operations in-process. From inside Claude Code Code Mode, you call `<cli> code run -` once per CLI instead of once per command. Model round trips are the same either way; processes, CPU and bytes back go down. See [Use from an agent or skill](/clicodemode/agents/#inside-claude-code-code-mode).

The same applies to OpenAI Codex or any agent that writes code: it composes calls; your CLI decides how cheaply it serves them.

## Is it a new agent?

No. It has no model, no prompt and no planner. It is a library your CLI imports so that whatever agent you already use can send it one program.

## Why not just use MCP?

MCP is a transport for tools; it does not make a CLI run several of its commands in one go. You can still use it: the optional [MCP adapter](/clicodemode/mcp/) serves `code_run` and friends over stdio, which suits agents that only speak MCP and sessions that call the same CLI many times. The core library never imports it, and a CLI that does not want MCP never links it.

Without MCP, everything works from a plain shell command, which every agent already has.

## Why not a shell script?

A shell script still starts the CLI once per command and passes text between them. A Code Mode program:

- calls operations in-process, in parallel with `Promise.all`, with JSON values instead of text to parse;
- is checked before it runs: unknown operations and unsupported syntax are reported with `file:line:col`, and nothing runs;
- is bounded by step, depth, concurrency and time limits;
- can reach only registered operations; processes are denied unless the host allows them.

## Is it a sandbox?

No. It bounds what a program can do and reach, but operations run with your CLI's privileges. Register only what you would let a program do, and gate the rest with `Policy.Allow`. See [Security and policy](/clicodemode/security/).

## Does it save tokens?

Not measured with a real agent yet. The measured gains are processes, CPU and bytes returned (28,273 to 169 bytes in the ctx-optimize chain). A model-turn and token benchmark is next on the roadmap.

## Is it faster?

Than one process per command run sequentially, yes (2x to 7.5x in our measurements). Than the same processes spawned in parallel, wall time is about even; CPU is lower. Than Node.js on tight loops, no (55 ms vs 25 ms for 1M iterations). Details: [Benchmarks](/clicodemode/benchmarks/).

## Why a JavaScript subset and not full JavaScript?

Agents already write JavaScript well, and a small tree-walking interpreter in pure Go keeps the CLI a single static binary (+1.7 MB) with no runtime to boot. Leaving out `class`, `new`, `this`, regex and dynamic imports keeps programs short and checkable. Unsupported syntax is a parse error that names the construct.

## Do I have to rewrite my commands?

No. Handlers wrap the functions you have, and `adapters/cmdtree` wraps command bodies that write to stdout, including `flag.FlagSet` commands. aos registered 254 operations from its existing batch table; ctx-optimize's integration is one 45-line file.

## Which CLIs use it?

aos, shipped in [aos v0.15.1](https://github.com/muthuishere/aos/releases/tag/v0.15.1), and ctx-optimize, shipped in [ctx-optimize v0.16.0](https://github.com/muthuishere/ctx-optimize/releases/tag/v0.16.0). See [Real examples](/clicodemode/examples/aos/).

## Which languages are supported?

Go is the primary library. Python (`pip install clicodemode`), TypeScript (`npm install clicodemode`) and Rust (`cargo add clicodemode`) wrappers run the same Go engine compiled to WASM and are experimental. The engine owns parsing, language semantics and the portable checks; each wrapper registers operations, calls your handlers and does I/O. Authorization and file or process security stay with your host program; WASM alone does not sandbox them. Known gaps: Python and Rust handlers are synchronous, and a TypeScript timeout cannot stop a CPU-bound loop. See the [python](https://github.com/muthuishere/clicodemode/tree/main/python), [npm](https://github.com/muthuishere/clicodemode/tree/main/npm) and [rust](https://github.com/muthuishere/clicodemode/tree/main/rust) READMEs.

## What does it cost?

About 1.7 MB in a stripped binary and one dependency, `gopkg.in/yaml.v3`. No cgo.
