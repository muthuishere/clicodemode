# MCP adapter (optional)

**MCP is optional.** The `codemode` package never imports `adapters/mcp`; a
host CLI that does not want MCP never links it, and everything Code Mode does
(`code operations`, `code run`, ...) works without it. The adapter is a thin
layer for agents that only speak the Model Context Protocol.

It is pure Go with no dependencies: newline-delimited JSON-RPC 2.0 over stdio,
MCP revision `2025-06-18`.

## Embedding

```go
eng := codemode.NewEngine(reg)
eng.Stdout = os.Stderr // stdout carries JSON-RPC only
err := mcp.Serve(ctx, eng, os.Stdin, os.Stdout, mcp.Options{Name: "mycli"})
```

`Serve` returns on EOF (nil) or when `ctx` is done. Requests are handled
concurrently with serialized writes; `notifications/cancelled` (or `ctx`)
cancels an in-flight tool call's run context. The engine's `Policy` (allow
hook, limits, exec policy) applies to every call.

## Tools

Code Mode style: a few tools, not one per operation.

| tool | arguments | returns |
|---|---|---|
| `code_capabilities` | none | operations, language, fingerprint |
| `code_describe` | `{operation}` | one operation's contract |
| `code_check` | `{program}` | `{ok, diagnostics, operations, hash}`; `isError` when not ok |
| `code_run` | `{program}` | the `Engine.Run` result `{ok, value, error, diagnostics, ms}`; `isError` when not ok |
| `code_docs` | none | language and operation reference (text) |

Results come back as a text content block holding JSON, plus
`structuredContent` when the JSON is an object. Programs run under the file
name `<mcp>`, so relative imports are not available; registered modules (`import ... from "scheme:path"`) are.

`Options{ExposeOperations: true}` also lists each operation as its own tool,
named with dots replaced by underscores (`health.check` -> `health_check`) and
using the operation's input schema. Those calls still go through the engine.

## Register with Claude Code

```sh
go build -o ~/bin/democli-mcp ./cmd/democli-mcp
claude mcp add democli -- ~/bin/democli-mcp
# or, with one tool per operation as well:
claude mcp add democli -- ~/bin/democli-mcp --expose-operations
```

Then ask Claude to use `code_capabilities` and `code_run`, e.g. a program like
`const h = await ops.health.check({verbose: true}); h.os`.

Try it by hand:

```sh
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"code_run","arguments":{"program":"await ops.text.upper({text:\"hi\"})"}}}' \
  | go run ./cmd/democli-mcp
```
