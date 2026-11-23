---
title: Security and policy
description: "What the interpreter bounds, what it does not, and the host controls: Policy.Allow, limits, ExecPolicy."
---

## Is it a sandbox?

**No.** The interpreter is not a sandbox for hostile code. It bounds steps, time, call depth and operation concurrency, and a program can reach only registered operations and policy-permitted processes. But operations run with your CLI's privileges. Register only what you would let a program do.

## What a program can reach

- Registered operations, through `call` and `ops`. Nothing else in your process.
- Relative `.js`/`.json` imports confined to the module root. No imports from stdin or `eval`.
- Processes only if `ExecPolicy` allows them. Denied by default.
- No filesystem, network, environment or clock APIs beyond the listed globals.

## Host controls

### `Policy.Allow`

Called before every operation call. Return an error to reject the call; the program sees a rejected promise.

```go
eng.Policy.Allow = func(ctx context.Context, op *codemode.Operation) error {
	if op.SideEffects != codemode.SideEffectNone && !approved(ctx, op.Name) {
		return fmt.Errorf("%s needs approval", op.Name)
	}
	return nil
}
```

### Limits

`js.Limits{MaxSteps, MaxCallDepth, MaxConcurrency, Timeout}` (defaults 10M, 512, 32, none) plus the caller's context. Hitting a limit or cancelling stops the run; it cannot be caught. On cancellation, handlers get a cancelled context, child processes are killed and streams closed.

### `ExecPolicy`

```go
eng.Policy.Exec = codemode.ExecPolicy{
	Programs: []string{"git"},          // base name or absolute path; "*" = any on PATH
	Allow:    func(ctx context.Context, argv []string) error { return nil }, // per-argv check
	Shell:    false,                     // shell("...") needs its own switch
	Env:      []string{"GIT_DIR"},       // passed through on top of PATH HOME LANG TMPDIR
	Dir:      "/srv/repo",               // children's cwd must resolve inside (symlinks resolved)
}
```

argv is always an array and never shell-interpreted. Literal disallowed programs are `check` errors. Output is capped (8 MiB default).

## Practical rules

- Mark side effects honestly; agents read `side_effects` and `authorization` before running.
- Register read-only verbs first. ctx-optimize registers only verbs that never write its store.
- Leave `ExecPolicy` empty unless you need it. aos leaves it empty.
- Keep secrets out of operation results. Caches never store secret values or approvals.
- Programs are text from an agent. Review them as you would a shell command it proposed: `code check` shows the operations a program references before it runs.
