package codemode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testRegistry(t *testing.T) (*Registry, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	reg := NewRegistry("test 1.0")
	reg.MustRegister(Operation{
		Name:        "health.check",
		Description: "health",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"verbose":{"type":"boolean"}},"additionalProperties":false}`),
		SideEffects: SideEffectNone,
		Idempotent:  true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			calls.Add(1)
			return map[string]any{"ok": true, "verbose": args["verbose"] == true}, nil
		},
	})
	reg.MustRegister(Operation{
		Name:        "math.add",
		Description: "add",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"}},"required":["a","b"]}`),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return args["a"].(float64) + args["b"].(float64), nil
		},
	})
	reg.MustRegister(Operation{
		Name:        "fail.always",
		Description: "fails",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return nil, errors.New("disk on fire")
		},
	})
	type out struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	reg.MustRegister(Operation{
		Name:        "struct.result",
		Description: "returns a Go struct",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return out{"x", 3}, nil
		},
	})
	return reg, calls
}

func TestRegistryValidation(t *testing.T) {
	reg := NewRegistry("v")
	h := func(context.Context, map[string]any) (any, error) { return nil, nil }
	for _, bad := range []Operation{
		{Name: "Bad.Name", Handler: h},
		{Name: "x..y", Handler: h},
		{Name: "ok.name"},
		{Name: "ok.schema", Handler: h, InputSchema: json.RawMessage(`{nope`)},
	} {
		if err := reg.Register(bad); err == nil {
			t.Errorf("Register(%q) should fail", bad.Name)
		}
	}
	if err := reg.Register(Operation{Name: "a.b", Handler: h}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(Operation{Name: "a.b", Handler: h}); err == nil {
		t.Fatal("duplicate should fail")
	}
	op, _ := reg.Lookup("a.b")
	if op.SideEffects != SideEffectLocal {
		t.Fatalf("default side effect = %q, want conservative %q", op.SideEffects, SideEffectLocal)
	}
}

func TestFingerprintChangesWithContract(t *testing.T) {
	reg, _ := testRegistry(t)
	f1 := reg.Capabilities().Fingerprint
	if f1 != reg.Capabilities().Fingerprint {
		t.Fatal("fingerprint not stable")
	}
	reg.MustRegister(Operation{Name: "new.op", Handler: func(context.Context, map[string]any) (any, error) { return nil, nil }})
	if reg.Capabilities().Fingerprint == f1 {
		t.Fatal("fingerprint did not change after adding an operation")
	}
}

func TestRunCallsOperations(t *testing.T) {
	reg, calls := testRegistry(t)
	e := NewEngine(reg)
	r := e.Run(context.Background(), `
		const h = await call("health.check", {verbose: true})
		const sum = await ops.math.add({a: 2, b: 3})
		const s = await call("struct.result", {})
		;({healthy: h.ok, verbose: h.verbose, sum, s})`, "p.js")
	if !r.OK {
		t.Fatalf("run failed: %s %v", r.Error, r.Diagnostics)
	}
	got, _ := json.Marshal(r.Value)
	if string(got) != `{"healthy":true,"s":{"count":3,"name":"x"},"sum":5,"verbose":true}` {
		t.Fatalf("got %s", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("health.check called %d times", calls.Load())
	}
}

func TestDefaultExportFunctionIsCalled(t *testing.T) {
	reg, _ := testRegistry(t)
	r := NewEngine(reg).Run(context.Background(), `
		export default async function main() {
			const n = await ops.math.add({a: 40, b: 2})
			return {n}
		}`, "p.js")
	if !r.OK || r.Value.(map[string]any)["n"] != 42.0 {
		t.Fatalf("got %+v", r)
	}
}

func TestCheckReportsUnknownOperationWithSpan(t *testing.T) {
	reg, calls := testRegistry(t)
	e := NewEngine(reg)
	_, diags := e.Compile("const x = 1\nawait call(\"health.chek\", {})\nawait ops.math.ad({})", "p.js")
	if len(diags) != 2 {
		t.Fatalf("want 2 diagnostics, got %v", diags)
	}
	if s := diags[0].String(); s != `p.js:2:12: error: unknown operation "health.chek" (did you mean "health.check"?)` {
		t.Fatalf("got %s", s)
	}
	if diags[1].Line != 3 || !strings.Contains(diags[1].Hint, "math.add") {
		t.Fatalf("got %v", diags[1])
	}
	// Run refuses and nothing executes.
	r := e.Run(context.Background(), `await call("health.check", {}); await call("nope.x", {})`, "p.js")
	if r.OK || calls.Load() != 0 {
		t.Fatalf("program with errors must not run (calls=%d)", calls.Load())
	}
}

func TestArgumentValidationAndErrors(t *testing.T) {
	reg, _ := testRegistry(t)
	e := NewEngine(reg)
	cases := map[string]string{
		`const x = {a: 1}; await call("math.add", x)`:             "math.add: invalid arguments: b is required",
		`const x = {a: "1", b: 2}; await call("math.add", x)`:     "a must be number, got string",
		`const x = {verbos: true}; await call("health.check", x)`: "verbos is not an allowed property",
		`const x = 5; await call("math.add", x)`:                  "arguments must be an object",
		`await call("fail.always", {})`:                           "fail.always: disk on fire",
		`const n = "health." + "nope"; await call(n)`:             `unknown operation "health.nope"`,
	}
	for src, want := range cases {
		r := e.Run(context.Background(), src, "p.js")
		if r.OK || !strings.Contains(r.Error, want) {
			t.Errorf("%s\n got  %q\n want %q", src, r.Error, want)
		}
	}
	// literal arguments are already rejected by check (#6), so the cases
	// above go through variables to exercise run-time validation.
	// handler errors are catchable in the program
	r := e.Run(context.Background(), `let m; try { await call("fail.always") } catch (e) { m = e.message } m`, "p.js")
	if !r.OK || r.Value != "fail.always: disk on fire" {
		t.Fatalf("got %+v", r)
	}
}

func TestPolicyDenies(t *testing.T) {
	reg, calls := testRegistry(t)
	e := NewEngine(reg)
	e.Policy.Allow = func(ctx context.Context, op *Operation) error {
		if op.SideEffects != SideEffectNone {
			return errors.New("read-only session")
		}
		return nil
	}
	if r := e.Run(context.Background(), `await call("health.check")`, "p.js"); !r.OK {
		t.Fatalf("read op should pass: %s", r.Error)
	}
	r := e.Run(context.Background(), `await call("math.add", {a: 1, b: 2})`, "p.js")
	if r.OK || !strings.Contains(r.Error, "denied by policy: read-only session") {
		t.Fatalf("got %+v", r)
	}
	_ = calls
}

func TestCancellationStopsRun(t *testing.T) {
	reg := NewRegistry("v")
	reg.MustRegister(Operation{Name: "slow.op", Handler: func(ctx context.Context, _ map[string]any) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	r := NewEngine(reg).Run(ctx, `await Promise.all([call("slow.op"), call("slow.op")])`, "p.js")
	if r.OK || time.Since(start) > 2*time.Second {
		t.Fatalf("cancel did not stop the run: %+v after %v", r, time.Since(start))
	}
}

func TestImportsAndJSONData(t *testing.T) {
	dir := t.TempDir()
	write := func(p, s string) {
		full := filepath.Join(dir, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(s), 0o644)
	}
	write("steps/health.js", `export async function inspect() { return await call("health.check") }
export function report(h, cfg) { return cfg.prefix + (h.ok ? "up" : "down") }`)
	write("config.json", `{"prefix": "status: "}`)
	write("main.js", `import { inspect, report } from "./steps/health.js"
import cfg from "./config.json"
report(await inspect(), cfg)`)
	write("escape.js", `import x from "../outside.js"`)
	reg, _ := testRegistry(t)
	e := NewEngine(reg)
	src, _ := os.ReadFile(filepath.Join(dir, "main.js"))
	p, diags := e.Compile(string(src), filepath.Join(dir, "main.js"))
	if len(diags) != 0 {
		t.Fatalf("diags: %v", diags)
	}
	if len(p.Imports) != 2 || len(p.Operations) != 1 || p.Operations[0] != "health.check" {
		t.Fatalf("plan: %+v", p)
	}
	r := e.Run(context.Background(), string(src), filepath.Join(dir, "main.js"))
	if !r.OK || r.Value != "status: up" {
		t.Fatalf("got %+v", r)
	}
	src, _ = os.ReadFile(filepath.Join(dir, "escape.js"))
	_, diags = e.Compile(string(src), filepath.Join(dir, "escape.js"))
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "outside the module root") {
		t.Fatalf("want escape denied, got %v", diags)
	}
	_, diags = e.Compile(`import x from "lodash"`, "<stdin>")
	if len(diags) != 1 {
		t.Fatalf("got %v", diags)
	}
}

func TestCacheKeyChangesWithCapabilities(t *testing.T) {
	reg, _ := testRegistry(t)
	e := NewEngine(reg)
	p1, _ := e.Compile(`await call("health.check")`, "p.js")
	p2, _ := e.Compile(`await call("health.check")`, "p.js")
	if p1.CacheKey != p2.CacheKey {
		t.Fatal("cache key not deterministic")
	}
	reg.MustRegister(Operation{Name: "another.op", Handler: func(context.Context, map[string]any) (any, error) { return nil, nil }})
	p3, _ := e.Compile(`await call("health.check")`, "p.js")
	if p3.CacheKey == p1.CacheKey || p3.Hash != p1.Hash {
		t.Fatal("cache key must change with capabilities while the source hash stays")
	}
}

func cli(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	reg, _ := testRegistry(t)
	var out, errb bytes.Buffer
	c := &CLI{Engine: NewEngine(reg), Name: "mycli code", Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb}
	c.Engine.Stdout = &out
	code := c.Main(context.Background(), args)
	return code, out.String(), errb.String()
}

func TestCLI(t *testing.T) {
	code, out, _ := cli(t, "", "operations")
	if code != 0 || !strings.Contains(out, "health.check") || !strings.Contains(out, "math.add") {
		t.Fatalf("operations: %d %s", code, out)
	}
	code, out, _ = cli(t, "", "describe", "math.add", "--json")
	var op map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &op) != nil || op["name"] != "math.add" {
		t.Fatalf("describe: %d %s", code, out)
	}
	code, _, errOut := cli(t, "", "describe", "math.ad")
	if code != 2 || !strings.Contains(errOut, `did you mean "math.add"`) {
		t.Fatalf("describe typo: %d %s", code, errOut)
	}
	code, out, _ = cli(t, "", "capabilities", "--json")
	var caps Capabilities
	if code != 0 || json.Unmarshal([]byte(out), &caps) != nil || caps.Spec != SpecVersion || len(caps.Operations) != 4 {
		t.Fatalf("capabilities: %d %s", code, out)
	}
	code, out, _ = cli(t, `await ops.math.add({a: 1, b: 2})`, "run", "-")
	if code != 0 || strings.TrimSpace(out) != "3" {
		t.Fatalf("run: %d %q", code, out)
	}
	code, out, _ = cli(t, "", "eval", `(await call("text.nope", {}))`)
	if code != 2 {
		t.Fatalf("eval unknown op should be exit 2, got %d %s", code, out)
	}
	code, out, _ = cli(t, "", "eval", "[1,2,3].map(x => x * 2)", "--json")
	if code != 0 || !strings.Contains(out, `"ok": true`) {
		t.Fatalf("eval: %d %s", code, out)
	}
	code, _, errOut = cli(t, `await call("fail.always")`, "run", "-")
	if code != 1 || !strings.Contains(errOut, "disk on fire") {
		t.Fatalf("failing run: %d %s", code, errOut)
	}
	code, _, errOut = cli(t, "let x = ;", "check", "-")
	if code != 2 || !strings.Contains(errOut, "<stdin>:1:9: error: syntax error") {
		t.Fatalf("check: %d %s", code, errOut)
	}
	code, out, _ = cli(t, `await call("health.check")`, "check", "-", "--json")
	if code != 0 || strings.TrimSpace(out) != "[]" {
		t.Fatalf("check json: %d %s", code, out)
	}
	code, out, _ = cli(t, `await call("health.check")`, "compile", "-")
	if code != 0 || !strings.Contains(out, `"cache_key"`) {
		t.Fatalf("compile: %d %s", code, out)
	}
	code, out, _ = cli(t, "", "docs")
	if code != 0 || !strings.Contains(out, "## Operations (4)") {
		t.Fatalf("docs: %d", code)
	}
	code, out, _ = cli(t, `print("from program"); 1`, "run", "-")
	if code != 0 || out != "from program\n1\n" {
		t.Fatalf("print: %q", out)
	}
}

func TestForgottenAwaitOnFailingCallFails(t *testing.T) {
	code, _, errOut := cli(t, `call("fail.always"); 1`, "run", "-")
	if code != 1 || !strings.Contains(errOut, "unhandled promise rejection") || !strings.Contains(errOut, "disk on fire") {
		t.Fatalf("got exit %d: %s", code, errOut)
	}
}

func TestOperationLimitsCannotBeSwallowed(t *testing.T) {
	reg := NewRegistry("v")
	reg.MustRegister(Operation{Name: "slow.op", Handler: func(ctx context.Context, _ map[string]any) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r := NewEngine(reg).Run(ctx, `try { await call("slow.op") } catch (e) {} "done"`, "p.js")
	if r.OK {
		t.Fatalf("cancellation was swallowed: %+v", r)
	}
}

func TestPanickingHandlerDoesNotCrashHost(t *testing.T) {
	reg := NewRegistry("v")
	reg.MustRegister(Operation{Name: "bad.op", Handler: func(context.Context, map[string]any) (any, error) {
		var m map[string]int
		m["x"] = 1 // nil map write panics
		return nil, nil
	}})
	r := NewEngine(reg).Run(context.Background(), `await call("bad.op")`, "p.js")
	if r.OK || !strings.Contains(r.Error, "panic") {
		t.Fatalf("got %+v", r)
	}
}
