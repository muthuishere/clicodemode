---
title: Sessions (persistent REPL)
description: "Keep one JavaScript scope alive across many programs: per-run timeouts, detach and poll, captured output, Vars and Reset."
---

A one-shot `code run` starts with an empty scope. A **session** keeps one top-level scope alive across many programs, the way a REPL does, for hosts that run an agent over many turns. Sessions are in the Go library (`codemode.Engine.NewSession`), new in v0.7.0.

```go
s := engine.NewSession(codemode.SessionConfig{DetachAfter: 30 * time.Second})
defer s.Close()

s.Run(ctx, `const repos = await ops.repo.list({})`, codemode.RunOptions{})
r := s.Run(ctx, `repos.filter(r => r.dirty).map(r => r.name)`, codemode.RunOptions{})
// r.OK, r.Value, r.Events (console output, in order)
```

## What carries over

- Top-level `const`, `let`, functions and classes stay for later runs. A later run may declare the same name again (REPL rule); one run may not declare a name twice, and a `const` from an earlier run cannot be assigned.
- `input` is per run (`RunOptions.Input`).
- Every run is checked before it runs, exactly like `code run`: unknown operations and bad literal arguments are reported and nothing runs. Diagnostics carry a `snippet` with the source line and a caret.

## Per-run limits and cancel

Each run gets its own context and limits. `RunOptions.Timeout` (or `SessionConfig.Timeout`, or the engine's `Policy.Limits.Timeout`) bounds that run. The `ctx` given to `Run` is the parent of the run's context: operation handlers see its values, and cancelling it stops the run, even after `Run` returned with the run detached. Pass `context.WithoutCancel(ctx)` to let a detached run outlive the caller, and use `Cancel(handle)` to stop one by handle. A stopped run never stops the session.

### Waiting on a human

The timeout and the detach deadline count active time. A handler that waits for an approval pauses the clock:

```go
Handler: func(ctx context.Context, args map[string]any) (any, error) {
    resume := codemode.PauseClock(ctx)
    defer resume()
    return askHuman(ctx, args) // honour ctx: the run can still be cancelled
},
```

## Detach and poll

`Run` returns when the program's top level finishes. If the top level is still running at the detach deadline, `Run` returns `{detached: true, handle}` and the program keeps going. Work a program starts without `await` also keeps going after `Run` returns (`pending: true`).

```go
r := s.Run(ctx, `print("start"); await ops.build.run({}); print("built")`, codemode.RunOptions{DetachAfter: time.Second})
for {
    events, done := s.Poll(r.Handle) // output, result, error, done; each delivered once
    // ...
    if done { break }
}
```

`print` and `console.*` are captured as ordered `output` events and are never written to the host process. An unhandled rejection after `Run` returned arrives as an `error` event. `PollWait(ctx, handle)` blocks until there is something to return.

## Host globals and changing operations

`s.Define("gh", value)` adds a global for later runs, for example a namespace of wrappers named the way your tools are. Operations registered or removed (`Registry.Unregister`) between runs show up on `ops` in the next run. For names a host maps itself, `Program.Calls()` lists the static call sites with their literal arguments, and `Engine.ValidateArgs(name, args)` checks them against an operation's schema the way `check` does for `ops.*`.

## Introspection

- `Vars()` lists the persistent bindings with name, kind, type and a short preview, so a host can show the model what is in scope after a context reset.
- `Reset()` clears the scope. `Close()` cancels every run and waits for them.

## Rules

Requirements SES-1 to SES-5 in the [specification](/clicodemode/reference/spec/) define this behaviour, and each one has a test. Approval, quoting and output caps stay with the host: an operation handler already runs outside the interpreter lock, so it can wait for a human approval and honour its `ctx`.
