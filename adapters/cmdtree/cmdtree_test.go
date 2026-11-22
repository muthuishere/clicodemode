package cmdtree

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/clicodemode/codemode"
)

// greet is an "existing" flag-based command: it builds and parses its own
// FlagSet per call, as CLIs normally do.
func greetFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("greet", flag.ContinueOnError)
	fs.Bool("loud", false, "shout")
	fs.Int("times", 1, "repeat count")
	fs.Float64("ratio", 0.5, "a ratio")
	fs.String("name", "world", "who to greet")
	fs.Duration("wait", 2*time.Second, "pause")
	fs.Uint("n", 3, "unsigned")
	return fs
}

func greet(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := greetFlags()
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	loud, _ := fs.Lookup("loud").Value.(flag.Getter).Get().(bool)
	times, _ := fs.Lookup("times").Value.(flag.Getter).Get().(int)
	name := fs.Lookup("name").Value.String()
	out := make([]string, 0, times)
	for range times {
		s := "hello " + name
		if loud {
			s = strings.ToUpper(s)
		}
		out = append(out, s)
	}
	return json.NewEncoder(stdout).Encode(map[string]any{"lines": out, "rest": fs.Args(), "wait": fs.Lookup("wait").Value.String()})
}

type exitErr int

func (e exitErr) Error() string { return fmt.Sprintf("exit %d", int(e)) }
func (e exitErr) ExitCode() int { return int(e) }

func engine(t *testing.T, ops ...codemode.Operation) *codemode.Engine {
	t.Helper()
	reg := codemode.NewRegistry("t 1")
	for _, op := range ops {
		reg.MustRegister(op)
	}
	return codemode.NewEngine(reg)
}

func run(t *testing.T, e *codemode.Engine, src string) map[string]any {
	t.Helper()
	r := e.Run(context.Background(), src, "<t>")
	if !r.OK {
		t.Fatalf("run failed: %s %v", r.Error, r.Diagnostics)
	}
	m, _ := r.Value.(map[string]any)
	return m
}

func TestFlagSetSchema(t *testing.T) {
	var s struct {
		Properties map[string]map[string]any `json:"properties"`
		Additional bool                      `json:"additionalProperties"`
	}
	if err := json.Unmarshal(SchemaFromFlagSet(greetFlags()), &s); err != nil {
		t.Fatal(err)
	}
	want := map[string][2]any{
		"loud": {"boolean", false}, "times": {"integer", 1.0}, "ratio": {"number", 0.5},
		"name": {"string", "world"}, "wait": {"string", "2s"}, "n": {"integer", 3.0},
	}
	for k, w := range want {
		p := s.Properties[k]
		if p["type"] != w[0] || p["default"] != w[1] {
			t.Errorf("%s: got %v", k, p)
		}
	}
	if s.Properties["name"]["description"] != "who to greet" {
		t.Errorf("usage not used as description: %v", s.Properties["name"])
	}
	if s.Properties["args"]["type"] != "array" || s.Additional {
		t.Errorf("bad args/additionalProperties: %v", s)
	}
}

func TestFromFlagSetRun(t *testing.T) {
	e := engine(t, FromFlagSet("greet", "Greet.", greetFlags(), greet, WithSideEffects(codemode.SideEffectNone)))
	m := run(t, e, `await ops.greet({loud: true, times: 2, name: "go", wait: "1m", args: ["-x", "y"]})`)
	if m["ok"] != true || m["exit"] != 0.0 {
		t.Fatalf("bad result %v", m)
	}
	j := m["json"].(map[string]any)
	if fmt.Sprint(j["lines"]) != "[HELLO GO HELLO GO]" || fmt.Sprint(j["rest"]) != "[-x y]" || j["wait"] != "1m0s" {
		t.Fatalf("bad json %v", j)
	}
	// Unknown flag keys are rejected by the schema before anything runs.
	r := e.Run(context.Background(), `await ops.greet({bogus: 1})`, "<t>")
	if r.OK || !strings.Contains(r.Error, "bogus") {
		t.Fatalf("want schema error, got %+v", r)
	}
	// Bad duration is an error.
	r = e.Run(context.Background(), `await ops.greet({wait: "soon"})`, "<t>")
	if r.OK {
		t.Fatal("want duration error")
	}
}

func TestFromRunnerFailureAndOutput(t *testing.T) {
	op := FromRunner("cmd.fail", "Fails.", func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		fmt.Fprint(stdout, "not json")
		fmt.Fprint(stderr, "boom: "+strings.Join(args, ","))
		return exitErr(3)
	})
	panics := FromRunner("cmd.panic", "Panics.", func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		panic("bad")
	})
	plain := FromRunner("cmd.plain", "Errors.", func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		return errors.New("nope")
	})
	e := engine(t, op, panics, plain)
	m := run(t, e, `await call("cmd.fail", {args: ["a", "b"]})`)
	if m["ok"] != false || m["exit"] != 3.0 || m["stdout"] != "not json" || m["stderr"] != "boom: a,b" {
		t.Fatalf("bad %v", m)
	}
	if _, has := m["json"]; has {
		t.Fatal("non-JSON stdout must not set json")
	}
	if m := run(t, e, `await call("cmd.panic")`); m["ok"] != false || !strings.Contains(m["error"].(string), "panicked") {
		t.Fatalf("bad %v", m)
	}
	if m := run(t, e, `await call("cmd.plain", {})`); m["exit"] != 1.0 {
		t.Fatalf("bad %v", m)
	}
}

func TestParallelCalls(t *testing.T) {
	e := engine(t, FromFlagSet("greet", "Greet.", greetFlags(), greet))
	m := run(t, e, `
const rs = await Promise.all([1,2,3,4,5,6,7,8].map(n => ops.greet({times: n, name: "p" + n})))
;({ counts: rs.map(r => r.json.lines.length) })`)
	if fmt.Sprint(m["counts"]) != "[1 2 3 4 5 6 7 8]" {
		t.Fatalf("bad %v", m)
	}
}

func TestOutputCap(t *testing.T) {
	op := FromRunner("big", "Big.", func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		_, err := stdout.Write([]byte(strings.Repeat("x", 100)))
		return err
	}, WithMaxOutput(10))
	m := run(t, engine(t, op), `await call("big")`)
	if m["stdout"] != "xxxxxxxxxx" || m["ok"] != true {
		t.Fatalf("bad %v", m)
	}
}

func TestJSONResultReturnsObjectsAndThrowsOnFailure(t *testing.T) {
	list := func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "")
		fs.SetOutput(stderr)
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() > 0 && fs.Arg(0) == "boom" {
			fmt.Fprintln(stderr, "no such thing: boom")
			return errors.New("failed")
		}
		if !*asJSON {
			fmt.Fprintln(stdout, "a\nb")
			return nil
		}
		fmt.Fprintln(stdout, `[{"name":"a"},{"name":"b"}]`)
		return nil
	}
	reg := codemode.NewRegistry("t")
	reg.MustRegister(FromRunner("items.list", "list", list, WithJSONResult("--json")))
	e := codemode.NewEngine(reg)
	r := e.Run(context.Background(), `const items = await ops.items.list(); items.map(i => i.name).join(",") + ":" + Array.isArray(items)`, "p.js")
	if !r.OK || r.Value != "a,b:true" {
		t.Fatalf("want the parsed array directly, got %+v", r)
	}
	r = e.Run(context.Background(), `let m; try { await ops.items.list({args: ["boom"]}) } catch (e) { m = e.message } m`, "p.js")
	if !r.OK || !strings.Contains(r.Value.(string), "exit 1: no such thing: boom") {
		t.Fatalf("want a rejection with the stderr tail, got %+v", r)
	}
}

func TestFlagsLastForPositionalFirstCommands(t *testing.T) {
	var got []string
	query := func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		got = append([]string{}, args...)
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			return errors.New("usage: query \"<terms>\" [--json]")
		}
		fmt.Fprintln(stdout, `{"hits":[1,2]}`)
		return nil
	}
	reg := codemode.NewRegistry("t")
	reg.MustRegister(FromRunner("ctx.query", "q", query, WithJSONResult("--json"), WithFlagsLast()))
	r := codemode.NewEngine(reg).Run(context.Background(), `(await ops.ctx.query({args: ["store engine"]})).hits.length`, "p.js")
	if !r.OK || r.Value != 2.0 {
		t.Fatalf("got %+v (argv %q)", r, got)
	}
	if strings.Join(got, "|") != "store engine|--json" {
		t.Fatalf("argv %q, want positionals then --json", got)
	}
}
