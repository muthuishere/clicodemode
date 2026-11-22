// democli-mcp is the democli registry served over MCP (stdio). The
// registry is copied from cmd/democli so the two examples stay independent.
//
//	claude mcp add democli -- /path/to/democli-mcp [--expose-operations]
package main

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/muthuishere/clicodemode/adapters/mcp"
	"github.com/muthuishere/clicodemode/codemode"
)

const version = "0.1.0"

// Existing handlers a CLI already has. Code Mode wraps them; it does not
// re-implement them.
func healthCheck(verbose bool) map[string]any {
	out := map[string]any{"ok": true, "os": runtime.GOOS, "time": time.Now().UTC().Format(time.RFC3339)}
	if verbose {
		out["go"] = runtime.Version()
		out["cpus"] = runtime.NumCPU()
	}
	return out
}

func registry() *codemode.Registry {
	reg := codemode.NewRegistry("democli " + version)
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
	reg.MustRegister(codemode.Operation{
		Name:        "text.upper",
		Description: "Upper-case a string.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		SideEffects: codemode.SideEffectNone,
		Idempotent:  true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return strings.ToUpper(args["text"].(string)), nil
		},
	})
	reg.MustRegister(codemode.Operation{
		Name:        "time.sleep",
		Description: "Wait for ms milliseconds (cancellable). Returns the ms waited.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"ms":{"type":"integer"}},"required":["ms"]}`),
		SideEffects: codemode.SideEffectNone,
		Idempotent:  true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			ms := args["ms"].(float64)
			select {
			case <-time.After(time.Duration(ms) * time.Millisecond):
				return ms, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})
	return reg
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	eng := codemode.NewEngine(registry())
	eng.Stdout = os.Stderr // stdout carries JSON-RPC only
	opts := mcp.Options{Name: "democli", ExposeOperations: slices.Contains(os.Args[1:], "--expose-operations")}
	if err := mcp.Serve(ctx, eng, os.Stdin, os.Stdout, opts); err != nil && ctx.Err() == nil {
		os.Stderr.WriteString("democli-mcp: " + err.Error() + "\n")
		os.Exit(1)
	}
}
