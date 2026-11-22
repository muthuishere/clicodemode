// democli shows how a host CLI embeds Code Mode: register operations (here
// wrapping ordinary Go functions), then mount `democli code ...`.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"time"

	"github.com/muthuishere/clicodemode/adapters/cmdtree"
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

// echoFlags and echoCmd are an existing flag-based command (`democli echo`).
// Code Mode wraps it with the cmdtree flag adapter: the input schema comes
// from the FlagSet, and the same function body runs for both paths.
func echoFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("echo", flag.ContinueOnError)
	fs.Bool("upper", false, "upper-case the output")
	fs.Int("repeat", 1, "print the words this many times")
	fs.String("sep", " ", "separator between words")
	fs.Bool("json", false, "print a JSON object instead of text")
	return fs
}

func echoCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := echoFlags()
	fs.SetOutput(stderr)
	upper := fs.Lookup("upper")
	if err := fs.Parse(args); err != nil {
		return err
	}
	text := strings.Join(fs.Args(), fs.Lookup("sep").Value.String())
	if upper.Value.String() == "true" {
		text = strings.ToUpper(text)
	}
	n := fs.Lookup("repeat").Value.(flag.Getter).Get().(int)
	lines := make([]string, 0, max(n, 0))
	for range n {
		lines = append(lines, text)
	}
	if fs.Lookup("json").Value.String() == "true" {
		return json.NewEncoder(stdout).Encode(map[string]any{"lines": lines})
	}
	for _, l := range lines {
		fmt.Fprintln(stdout, l)
	}
	return nil
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
	reg.MustRegister(cmdtree.FromFlagSet("text.echo", "Run `democli echo`: join args with --sep, optionally upper-cased and repeated.",
		echoFlags(), echoCmd, cmdtree.WithSideEffects(codemode.SideEffectNone), cmdtree.WithIdempotent(),
		cmdtree.WithExamples(codemode.Example{Description: "JSON output", Args: map[string]any{"args": []any{"hi", "there"}, "json": true}})))
	return reg
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "echo" {
		if err := echoCmd(ctx, args[1:], os.Stdout, os.Stderr); err != nil {
			os.Exit(1)
		}
		return
	}
	if len(args) == 0 || args[0] != "code" {
		fmt.Fprintln(os.Stderr, "usage: democli echo [flags] words... | democli code <subcommand>   (try: democli code help)")
		os.Exit(2)
	}
	cli := codemode.NewCLI(codemode.NewEngine(registry()), "democli code")
	os.Exit(cli.Main(ctx, args[1:]))
}
