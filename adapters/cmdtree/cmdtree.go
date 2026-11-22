// Package cmdtree turns commands a CLI already has into codemode operations
// without duplicating their handlers. It depends only on the standard
// library: there is no cobra or urfave import.
//
// The common shape of a CLI command is "parse argv, write to stdout/stderr,
// return an error". FromRunner wraps exactly that:
//
//	op := cmdtree.FromRunner("repo.status", "Show repo status.",
//		func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
//			return runStatus(ctx, args, stdout) // the existing command body
//		})
//	reg.MustRegister(op)
//
// A program then calls it with {args: ["--short"]} and gets back
// {ok, exit, stdout, stderr} plus a parsed `json` field when stdout is JSON.
//
// # Cobra hosts
//
// A cobra command can be wrapped without this package importing cobra; the
// host does the wiring, reusing the command's own RunE and flag parsing:
//
//	func cobraRunner(newCmd func() *cobra.Command) cmdtree.Runner {
//		return func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
//			cmd := newCmd() // fresh command per call: flag state is not shared
//			cmd.SetArgs(args)
//			cmd.SetOut(stdout)
//			cmd.SetErr(stderr)
//			cmd.SilenceUsage = true
//			return cmd.ExecuteContext(ctx)
//		}
//	}
//
//	reg.MustRegister(cmdtree.FromRunner("repo.status", "Show repo status.",
//		cobraRunner(newStatusCmd), cmdtree.WithFlagSet(newStatusCmd().Flags())))
//
// (cobra's *pflag.FlagSet is not a *flag.FlagSet; such a host passes a stdlib
// FlagSet mirroring the flags, or omits WithFlagSet and uses {args: [...]}.)
package cmdtree

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/muthuishere/clicodemode/codemode"
)

// Runner is an existing command body: it parses args itself and writes to
// stdout and stderr. A non-nil error means a non-zero exit; implement
// ExitCode() int on the error to choose the code (default 1).
type Runner func(ctx context.Context, args []string, stdout, stderr io.Writer) error

// Option customises the generated operation.
type Option func(*config)

type config struct {
	fs          *flag.FlagSet
	sideEffects codemode.SideEffect
	idempotent  bool
	auth        string
	examples    []codemode.Example
	maxOutput   int
}

// WithFlagSet declares the command's flags: each becomes an optional input
// property (typed, with its default and usage), and is passed to the runner
// as --name=value ahead of the positional args. The FlagSet is only read for
// its definitions; it is never parsed, so it is safe to share.
func WithFlagSet(fs *flag.FlagSet) Option { return func(c *config) { c.fs = fs } }

// WithSideEffects sets the operation's side-effect class (default local).
func WithSideEffects(s codemode.SideEffect) Option { return func(c *config) { c.sideEffects = s } }

// WithIdempotent marks the operation idempotent.
func WithIdempotent() Option { return func(c *config) { c.idempotent = true } }

// WithAuthorization sets the scope the host must grant.
func WithAuthorization(scope string) Option { return func(c *config) { c.auth = scope } }

// WithExamples adds worked calls shown by describe.
func WithExamples(ex ...codemode.Example) Option {
	return func(c *config) { c.examples = append(c.examples, ex...) }
}

// WithMaxOutput caps captured stdout and stderr each (default 8 MiB).
func WithMaxOutput(n int) Option { return func(c *config) { c.maxOutput = n } }

const defaultMaxOutput = 8 << 20

var outputSchema = json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"},"exit":{"type":"integer"},"stdout":{"type":"string"},"stderr":{"type":"string"},"json":{"description":"stdout parsed as JSON, when it is valid JSON"},"error":{"type":"string"}},"required":["ok","exit","stdout","stderr"]}`)

// FromRunner wraps an existing command body as an operation. Input is
// {args: [strings]} plus any flags declared with WithFlagSet. The result is
// {ok, exit, stdout, stderr, json?, error?}; a failing command does not
// throw, it returns ok:false with its exit code (like exec()).
func FromRunner(name, desc string, run Runner, opts ...Option) codemode.Operation {
	cfg := config{sideEffects: codemode.SideEffectLocal, maxOutput: defaultMaxOutput}
	for _, o := range opts {
		o(&cfg)
	}
	flags := flagSpecs(cfg.fs)
	return codemode.Operation{
		Name:          name,
		Description:   desc,
		InputSchema:   inputSchema(flags),
		OutputSchema:  outputSchema,
		SideEffects:   cfg.sideEffects,
		Idempotent:    cfg.idempotent,
		Authorization: cfg.auth,
		Examples:      cfg.examples,
		Handler: func(ctx context.Context, in map[string]any) (any, error) {
			argv, err := buildArgv(flags, in)
			if err != nil {
				return nil, err
			}
			return invoke(ctx, run, argv, cfg.maxOutput), nil
		},
	}
}

// FromFlagSet wraps a flag-based command: the input schema is derived from
// fs (bool, int, uint, float, string and duration flags, with defaults and
// usage), and run receives argv built from the object keys. run typically
// creates and parses its own FlagSet, exactly as the CLI already does.
func FromFlagSet(name, desc string, fs *flag.FlagSet, run Runner, opts ...Option) codemode.Operation {
	return FromRunner(name, desc, run, append([]Option{WithFlagSet(fs)}, opts...)...)
}

// SchemaFromFlagSet returns the JSON Schema FromFlagSet would use.
func SchemaFromFlagSet(fs *flag.FlagSet) json.RawMessage { return inputSchema(flagSpecs(fs)) }

type flagSpec struct {
	name, typ, usage string
	def              any
	duration         bool
}

func flagSpecs(fs *flag.FlagSet) []flagSpec {
	if fs == nil {
		return nil
	}
	var out []flagSpec
	fs.VisitAll(func(f *flag.Flag) {
		s := flagSpec{name: f.Name, usage: f.Usage, typ: "string", def: f.DefValue}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			s.typ = "boolean"
			s.def, _ = strconv.ParseBool(f.DefValue)
		} else if g, ok := f.Value.(flag.Getter); ok {
			switch v := g.Get().(type) {
			case int, int64, uint, uint64:
				s.typ = "integer"
				n, _ := strconv.ParseFloat(fmt.Sprint(v), 64)
				s.def = n
			case float64:
				s.typ, s.def = "number", v
			case time.Duration:
				s.duration = true
				s.def = f.DefValue
			case string:
				s.def = v
			}
		}
		out = append(out, s)
	})
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func inputSchema(flags []flagSpec) json.RawMessage {
	props := map[string]any{
		"args": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "positional arguments"},
	}
	for _, f := range flags {
		p := map[string]any{"type": f.typ, "default": f.def}
		desc := f.usage
		if f.duration {
			desc = strings.TrimSpace(desc + " (Go duration, e.g. \"1m30s\")")
		}
		if desc != "" {
			p["description"] = desc
		}
		props[f.name] = p
	}
	b, _ := json.Marshal(map[string]any{"type": "object", "properties": props, "additionalProperties": false})
	return b
}

func buildArgv(flags []flagSpec, in map[string]any) ([]string, error) {
	var argv []string
	for _, f := range flags {
		v, ok := in[f.name]
		if !ok || v == nil {
			continue
		}
		var s string
		switch x := v.(type) {
		case bool:
			s = strconv.FormatBool(x)
		case float64:
			if f.typ == "integer" {
				s = strconv.FormatInt(int64(x), 10)
			} else {
				s = strconv.FormatFloat(x, 'f', -1, 64)
			}
		case string:
			if f.duration {
				if _, err := time.ParseDuration(x); err != nil {
					return nil, fmt.Errorf("flag %s: %v", f.name, err)
				}
			}
			s = x
		default:
			return nil, fmt.Errorf("flag %s: unsupported value %T", f.name, v)
		}
		argv = append(argv, "--"+f.name+"="+s)
	}
	var pos []string
	if raw, ok := in["args"]; ok && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return nil, errors.New("args must be an array of strings")
		}
		for _, a := range list {
			s, ok := a.(string)
			if !ok {
				return nil, errors.New("args must be an array of strings")
			}
			pos = append(pos, s)
		}
	}
	// Keep a positional that looks like a flag from being parsed as one.
	if len(flags) > 0 && len(pos) > 0 && strings.HasPrefix(pos[0], "-") {
		argv = append(argv, "--")
	}
	return append(argv, pos...), nil
}

func invoke(ctx context.Context, run Runner, argv []string, max int) (res map[string]any) {
	stdout := &capWriter{max: max}
	stderr := &capWriter{max: max}
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("command panicked: %v", r)
			}
		}()
		return run(ctx, argv, stdout, stderr)
	}()
	exit := 0
	if err != nil {
		exit = 1
		var ec interface{ ExitCode() int }
		if errors.As(err, &ec) && ec.ExitCode() != 0 {
			exit = ec.ExitCode()
		}
	}
	res = map[string]any{"ok": err == nil, "exit": exit, "stdout": stdout.String(), "stderr": stderr.String()}
	if err != nil {
		res["error"] = err.Error()
	}
	if t := bytes.TrimSpace(stdout.Bytes()); len(t) > 0 && json.Valid(t) {
		var v any
		if json.Unmarshal(t, &v) == nil {
			res["json"] = v
		}
	}
	return res
}

// capWriter is a bounded buffer: writes beyond max are dropped (but reported
// as written so the command does not fail on a short write).
type capWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (w *capWriter) Bytes() []byte  { w.mu.Lock(); defer w.mu.Unlock(); return w.buf.Bytes() }
func (w *capWriter) String() string { return string(w.Bytes()) }

func (w *capWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if room := w.max - w.buf.Len(); room < len(p) {
		if room > 0 {
			w.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return w.buf.Write(p)
}
