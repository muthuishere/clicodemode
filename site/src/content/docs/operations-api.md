---
title: Operations API
description: "The Go types you use to register operations and run programs: Operation, Registry, Engine, Policy, CLI, Result."
---

Package `github.com/muthuishere/clicodemode/codemode`. Reference docs: [pkg.go.dev](https://pkg.go.dev/github.com/muthuishere/clicodemode/codemode).

## Operation

```go
type Handler func(ctx context.Context, args map[string]any) (any, error)

type Operation struct {
	Name          string          // dotted lower-case: [a-z][a-z0-9_-]* segments
	Description   string
	InputSchema   json.RawMessage // JSON Schema; validated before the handler runs
	OutputSchema  json.RawMessage
	SideEffects   SideEffect      // none | local (default) | external
	Idempotent    bool
	Authorization string          // scope the host must grant; empty = none
	Examples      []Example
	Handler       Handler
}

const (
	SideEffectNone     SideEffect = "none"     // pure read
	SideEffectLocal    SideEffect = "local"    // changes this machine
	SideEffectExternal SideEffect = "external" // changes something remote
)
```

Input validation covers `type`, `properties`, `required`, `enum`, `items` and `additionalProperties: false`.

## Registry

```go
reg := codemode.NewRegistry("mycli 1.4.0") // version shows in capabilities and fingerprint
err := reg.Register(op)                    // rejects bad names, duplicates, nil handlers, invalid schemas
reg.MustRegister(op)                       // panics instead
op, ok := reg.Lookup("health.check")
all := reg.Operations()
```

## Engine

```go
type Engine struct {
	Registry    *Registry
	Policy      Policy
	Stdout      io.Writer // print/console.log output (default os.Stderr)
	Cache       *Cache    // compiled-plan cache for check/compile (nil = off)
	WorkflowCLI string    // command prefix used by workflow YAML export
}

eng := codemode.NewEngine(reg)
prog, diags := eng.Compile(src, "prog.js")             // check without running
res := eng.Run(ctx, src, "prog.js")
res = eng.RunWithInput(ctx, src, "prog.js", json.RawMessage(`{"x":1}`))
res = eng.Eval(ctx, "1 + 2")
```

```go
type Result struct {
	OK          bool         `json:"ok"`
	Value       any          `json:"value"`
	Error       string       `json:"error,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
	MS          int64        `json:"ms"`
}

type Diagnostic struct {
	Severity, File string
	Line, Col      int    // 1-based
	Message, Hint  string
}
```

`Program` (from `Compile`) carries `Hash`, `Imports`, the statically referenced `Operations` and a `CacheKey`.

## Policy

```go
type Policy struct {
	Allow      func(ctx context.Context, op *Operation) error // consulted before every call
	Limits     js.Limits                                      // MaxSteps, MaxCallDepth, MaxConcurrency, Timeout
	ModuleRoot string                                         // imports are confined here
	Exec       ExecPolicy                                     // processes; empty = denied
}
```

See [Security and policy](/clicodemode/security/) for `ExecPolicy`.

## CLI

```go
cli := codemode.NewCLI(eng, "mycli code")      // the prefix appears in help and skill output
cli.Stdin, cli.Stdout, cli.Stderr = in, out, errw
code := cli.Main(ctx, args)                    // exit code: 0, 1 or 2 (10+i for run-step outcomes)
```

## Adapters

- `adapters/cmdtree`: `FromRunner`, `FromFlagSet` and options. See [Integrate your CLI](/clicodemode/integrate/#option-b-wrap-an-existing-command-with-cmdtree).
- `adapters/mcp`: `mcp.Serve(ctx, eng, in, out, mcp.Options{...})`. See [MCP adapter](/clicodemode/mcp/).
