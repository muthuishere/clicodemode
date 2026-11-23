---
title: Get started
description: "Try the demo CLI in 30 seconds, then add a code subcommand to your own Go CLI."
---

CLI CodeMode is a Go library. You import it into a CLI you already have, register the commands you want agents to call, and mount a `code` subcommand. An agent then sends one small JavaScript-subset program to `<cli> code run` instead of starting your CLI once per command.

It needs Go and nothing else at run time: no Node.js, V8, Goja, cgo or external interpreter. MCP is optional.

## Try the demo in 30 seconds

The repository ships a reference host, `cmd/democli`, with four operations.

```sh
git clone https://github.com/muthuishere/clicodemode && cd clicodemode
go run ./cmd/democli code help
go run ./cmd/democli code operations
```

```txt
health.check  none  Report whether this machine is healthy.
text.echo     none  Run `democli echo`: join args with --sep, optionally upper-cased and repeated.
text.upper    none  Upper-case a string.
time.sleep    none  Wait for ms milliseconds (cancellable). Returns the ms waited.
```

Run a program that calls two operations in parallel:

```sh
echo 'const [h, u] = await Promise.all([ops.health.check({verbose: false}), ops.text.upper({text: "ok"})]); ({os: h.os, u})' \
  | go run ./cmd/democli code run - --json
```

```json
{
  "ok": true,
  "value": {
    "os": "darwin",
    "u": "OK"
  },
  "ms": 0
}
```

Make a typo and `check` catches it before anything runs (exit code 2):

```sh
echo 'await ops.helth.check({})' | go run ./cmd/democli code check -
```

```txt
<stdin>:1:16: error: unknown operation "helth.check" (did you mean "health.check"?)
1 error(s)
```

## Add it to your CLI

### 1. Install

```sh
go get github.com/muthuishere/clicodemode@latest && go mod tidy
```

### 2. Register an operation

An operation wraps a function your CLI already has. Do not write a second implementation.

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
```

Already have command bodies that write to stdout? Wrap them with [`adapters/cmdtree`](/clicodemode/integrate/) instead of writing handlers.

### 3. Mount `code`

```go
// mycli code <subcommand>
os.Exit(codemode.NewCLI(codemode.NewEngine(reg), "mycli code").Main(ctx, os.Args[2:]))
```

### 4. Check it

```sh
mycli code version                  # mycli 1.4.0 (clicodemode 0.5.2, spec codemode/v0)
mycli code capabilities --json      # what an agent reads first
mycli code eval 'await ops.health.check({verbose: true})'
mycli code skill > SKILL.md         # a generated skill for agents driving this CLI
```

## Next

- [How it works](/clicodemode/how-it-works/): what happens inside one `code run`.
- [Integrate your CLI](/clicodemode/integrate/): handlers, `cmdtree`, JSON-first results, policy.
- [Use from an agent or skill](/clicodemode/agents/): discover, check, run.
