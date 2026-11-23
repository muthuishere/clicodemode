---
title: Integrate your CLI
description: "Register handlers, wrap existing commands with cmdtree, return JSON, and mount the code subcommand."
---

There are two ways to turn what your CLI already does into operations. Both wrap existing code; neither asks you to reimplement a verb.

## Option A: write a handler

Use this when you have a Go function that returns a value.

```go
reg := codemode.NewRegistry("mycli " + version)
reg.MustRegister(codemode.Operation{
	Name:         "health.check",
	Description:  "Report whether this machine is healthy.",
	InputSchema:  json.RawMessage(`{"type":"object","properties":{"verbose":{"type":"boolean"}},"additionalProperties":false}`),
	OutputSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"},"os":{"type":"string"}},"required":["ok"]}`),
	SideEffects:  codemode.SideEffectNone,
	Idempotent:   true,
	Examples:     []codemode.Example{{Description: "quick check", Args: map[string]any{"verbose": false}}},
	Handler: func(ctx context.Context, args map[string]any) (any, error) {
		v, _ := args["verbose"].(bool)
		return healthCheck(v), nil
	},
})
```

(From `cmd/democli/main.go`.) Return any JSON-encodable value. Return an error to reject the promise; programs catch it with `try/catch`. A handler panic becomes a rejection, not a crash. Honour `ctx`: it is cancelled when the run is cancelled or hits a limit.

Names are dotted lower-case segments, `[a-z][a-z0-9_-]*`. Prefer underscores over hyphens in segments you expect people to write as `ops.a.b`: `ops.ctx.change-plan` parses as subtraction, while `ops["window-move"]` works.

## Option B: wrap an existing command with `cmdtree`

Use this when your command body writes to stdout. `adapters/cmdtree` is standard library only.

### A runner: `func(ctx, args, stdout, stderr) error`

```go
reg.MustRegister(cmdtree.FromRunner("ctx.query", "Find symbols", runQuery,
	cmdtree.WithJSONResult("--json"), // append --json, return the parsed value, throw on failure
	cmdtree.WithFlagsLast(),           // for commands that read positionals first
	cmdtree.WithSideEffects(codemode.SideEffectNone)))
```

Input is `{args: [strings]}`. Without `WithJSONResult` a call resolves to `{ok, exit, stdout, stderr, json?, error?}` and a failing command returns `ok: false` rather than throwing.

### A `flag.FlagSet`

```go
reg.MustRegister(cmdtree.FromFlagSet("text.echo", "Run `democli echo`.",
	echoFlags(), echoCmd,
	cmdtree.WithSideEffects(codemode.SideEffectNone), cmdtree.WithIdempotent()))
```

The input schema is derived from the FlagSet (bool, int/uint, float, string, duration) with defaults and usage text. The FlagSet is only read, never parsed, so parallel calls are safe as long as the command builds its own FlagSet per call.

### Options

| Option | Effect |
|---|---|
| `WithJSONResult(flag)` | Append `flag` (empty if the command always prints JSON); return the parsed value; reject on non-zero exit with the stderr tail. Recommended. |
| `WithFlagsLast()` | Pass flags after positionals (`query "terms" --json`). Default is flags first, as stdlib `flag.Parse` requires. |
| `WithFlagSet(fs)` | Declare flags for a runner. |
| `WithSideEffects(s)` | `SideEffectNone`, `SideEffectLocal` (default), `SideEffectExternal`. |
| `WithIdempotent()` | Mark the operation idempotent. |
| `WithAuthorization(scope)` | Scope the host must grant; shown in `describe`. |
| `WithExamples(...)` | Examples shown in `describe` and `docs`. |
| `WithMaxOutput(n)` | Cap captured stdout/stderr each (default 8 MiB). |

## Mount the `code` subcommand

```go
eng := codemode.NewEngine(reg)
cli := codemode.NewCLI(eng, "mycli code")
cli.Stdin, cli.Stdout, cli.Stderr = os.Stdin, os.Stdout, os.Stderr
os.Exit(cli.Main(ctx, args)) // args after "code"
```

If your framework passes its own writers (cobra, a custom `cli.Ctx`), set `cli.Stdin/Stdout/Stderr` from them, as [aos does](/clicodemode/examples/aos/). Send program output (`print`, `console.log`) away from the result by setting `eng.Stdout` to stderr.

## Policy and limits

```go
eng.Policy = codemode.Policy{
	Allow: func(ctx context.Context, op *codemode.Operation) error {
		if op.SideEffects == codemode.SideEffectExternal {
			return errors.New("external side effects need approval")
		}
		return nil
	},
	Limits: js.Limits{MaxSteps: 1_000_000, MaxConcurrency: 8, Timeout: 30 * time.Second},
}
```

`Exec` (an `ExecPolicy`) is empty by default, so programs cannot start processes. See [Security and policy](/clicodemode/security/).

## Caches

- `CODEMODE_CACHE_DIR=<dir>` (or `Engine.Cache`) caches compiled plans for `check` and `compile`. Off by default.
- `Cache.LoadCapabilities` / `SaveCapabilities` with `CapabilityCacheKey(version, fingerprint, authContext)` cache capability documents.
- Neither stores secret values, approvals or run results.

## Checklist

- [ ] Every operation wraps an existing function or command body.
- [ ] `side_effects` is set honestly; read-only verbs are `SideEffectNone`.
- [ ] Results are JSON values (`WithJSONResult` for wrapped commands).
- [ ] Only operations you would let a program run are registered.
- [ ] `mycli code version` and `mycli code capabilities --json` work.
- [ ] You ship `mycli code skill` output, or a hand-written skill, with your CLI.
