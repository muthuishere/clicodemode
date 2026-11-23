---
title: How it works
description: "What happens between an agent sending a program and getting one JSON value back."
---

## What problem does it solve?

An agent that needs five things from a CLI usually runs it five times. Each run starts the binary, parses arguments, does one thing and prints text the agent has to parse back. Agent-side Code Mode (Claude Code, Codex) cuts the model round trips, but the code it writes still starts the CLI once per command.

CLI CodeMode moves the loop into the CLI. The agent sends one program; the CLI runs it in-process against its own registered operations and returns only the program's result.

## The pieces

| Piece | Package | What it does |
|---|---|---|
| Registry | `codemode` | Holds your operations: name, description, input/output JSON Schema, side effects, idempotency, handler. Rejects bad names, duplicates and invalid schemas. |
| Engine | `codemode` | Binds `call` and `ops` into the interpreter, validates arguments, consults `Policy.Allow`, enforces limits, loads relative modules, runs static checks. |
| Interpreter | `js` | A pure-Go lexer, Pratt parser and tree-walking evaluator for a JavaScript subset. Knows nothing about CLIs. |
| CLI | `codemode` | The `code` subcommands: `operations`, `describe`, `capabilities`, `docs`, `check`, `compile`, `run`, `eval`, `version`, `skill`, `run-step`. |
| Adapters | `adapters/cmdtree`, `adapters/mcp` | Wrap existing command bodies; optionally serve the engine over MCP stdio. The core never imports them. |

## One `code run`, step by step

1. **Parse and check.** The program is parsed. Unsupported syntax (`class`, `new`, regex, `switch`) is a parse error naming the construct. Every literal operation name is looked up; an unknown one is an error with a did-you-mean hint. If there are diagnostics, the CLI exits 2 and **nothing runs**.
2. **Bind input.** `--input JSON` becomes the global `input` (null when absent).
3. **Evaluate.** Each `ops.x.y(args)` or `call("x.y", args)` returns a Promise. Before a handler runs, its arguments are validated against `input_schema` and `Policy.Allow` is consulted. `Promise.all` really runs operations in parallel, bounded by `MaxConcurrency` (default 32).
4. **Bound.** Steps (default 10M), call depth (512), concurrency and an optional timeout are hard limits. Hitting one stops the whole run and cannot be caught.
5. **Return.** The result is the default export (a function is called and awaited) or the last top-level expression. With `--json` the CLI prints exactly one object: `{ok, value, error?, diagnostics?, ms}`. Exit 0 on success, 1 if the program ran and failed.

## Self-describing

`code capabilities --json` returns `{spec, version, fingerprint, language, operations}`. The fingerprint is a hash over the spec version, CLI version and every operation's public contract, so an agent can cache the document and refetch only when the fingerprint changes. `code docs` prints a language and operation reference; `code skill` prints a SKILL.md for this CLI.

## What it is not

- Not a new agent, and not a replacement for agent-side Code Mode. The two [compose](/clicodemode/agents/#inside-claude-code-code-mode).
- Not a sandbox for hostile code. Operations run with your CLI's privileges. See [Security and policy](/clicodemode/security/).
- Not Node.js. It is a [subset](/clicodemode/language/) with no npm and no Node or browser APIs.
