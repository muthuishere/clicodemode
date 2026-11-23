package codemode

// Runs the language-neutral conformance suite in ../conformance against the
// Go engine. The same case files are run against the JS port by
// conformance/run-js.mjs. Fixtures are written from docs/SPEC.md and real
// JavaScript semantics; a case the Go engine fails on purpose is marked
// "known_deviation" and recorded in conformance/DEVIATIONS.md.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muthuishere/clicodemode/js"
)

type confOp struct {
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	InputSchema       json.RawMessage `json:"input_schema"`
	SideEffects       string          `json:"side_effects"`
	Idempotent        bool            `json:"idempotent"`
	Returns           json.RawMessage `json:"returns"`
	Throws            string          `json:"throws"`
	Panics            bool            `json:"panics"`
	DelayMS           int             `json:"delay_ms"`
	NoHandler         bool            `json:"no_handler"`
	BlocksUntilCancel bool            `json:"blocks_until_cancel"`
}

type confDiag struct {
	Severity        string `json:"severity"`
	File            string `json:"file"`
	Line            int    `json:"line"`
	Col             int    `json:"col"`
	MessageContains string `json:"messageContains"`
	HintContains    string `json:"hintContains"`
}

type confExpect struct {
	Result      json.RawMessage `json:"result"`
	Diagnostics *[]confDiag     `json:"diagnostics"`
	Error       *struct {
		MessageContains string `json:"messageContains"`
		Line            int    `json:"line"`
	} `json:"error"`
	Exit          *int `json:"exit"`
	RegisterError *struct {
		Op string `json:"op"`
	} `json:"register_error"`
	Calls             map[string]int `json:"calls"`
	Stdout            *string        `json:"stdout"`
	MaxInFlightAtMost int            `json:"max_in_flight_at_most"`
	Fingerprint       string         `json:"fingerprint"` // equal | different
	Operations        []string       `json:"operations"`  // compile: statically referenced ops
}

type confCase struct {
	ID             string          `json:"id"`
	Spec           []string        `json:"spec"`
	Description    string          `json:"description"`
	KnownDeviation bool            `json:"known_deviation"`
	Mode           string          `json:"mode"` // run (default) | eval | check | register | fingerprint
	Ops            []confOp        `json:"ops"`
	OpsB           []confOp        `json:"ops_b"`
	Program        string          `json:"program"`
	File           string          `json:"file"`
	Input          json.RawMessage `json:"input"`
	Limits         *struct {
		MaxSteps       int64 `json:"max_steps"`
		MaxCallDepth   int   `json:"max_call_depth"`
		MaxConcurrency int   `json:"max_concurrency"`
		TimeoutMS      int   `json:"timeout_ms"`
	} `json:"limits"`
	Policy *struct {
		DenySideEffects []string `json:"deny_side_effects"`
	} `json:"policy"`
	Expect confExpect `json:"expect"`
}

func confRegistry(ops []confOp, calls map[string]*atomic.Int32, inFlight, peak *atomic.Int32) (*Registry, error) {
	reg := NewRegistry("conformance 1.0")
	for _, o := range ops {
		o := o
		if calls[o.Name] == nil {
			calls[o.Name] = &atomic.Int32{}
		}
		cnt := calls[o.Name]
		op := Operation{
			Name: o.Name, Description: o.Description, InputSchema: o.InputSchema,
			SideEffects: SideEffect(o.SideEffects), Idempotent: o.Idempotent,
		}
		if !o.NoHandler {
			op.Handler = func(ctx context.Context, args map[string]any) (any, error) {
				cnt.Add(1)
				c := inFlight.Add(1)
				for {
					p := peak.Load()
					if c <= p || peak.CompareAndSwap(p, c) {
						break
					}
				}
				defer inFlight.Add(-1)
				if o.Panics {
					panic("stub panic")
				}
				if o.BlocksUntilCancel {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				if o.DelayMS > 0 {
					time.Sleep(time.Duration(o.DelayMS) * time.Millisecond)
				}
				if o.Throws != "" {
					return nil, errors.New(o.Throws)
				}
				if len(o.Returns) == 0 {
					return nil, nil
				}
				var v any
				if err := json.Unmarshal(o.Returns, &v); err != nil {
					return nil, err
				}
				return v, nil
			}
		}
		if err := reg.Register(op); err != nil {
			return reg, fmt.Errorf("%s: %w", o.Name, err)
		}
	}
	return reg, nil
}

func canon(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestConformance(t *testing.T) {
	files, _ := filepath.Glob("../conformance/cases/*.json")
	if len(files) == 0 {
		t.Fatal("no conformance cases found")
	}
	seen := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var cases []confCase
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cases); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, c := range cases {
			if seen[c.ID] {
				t.Fatalf("duplicate case id %q", c.ID)
			}
			seen[c.ID] = true
			c := c
			t.Run(c.ID, func(t *testing.T) { runConfCase(t, c) })
		}
	}
}

func runConfCase(t *testing.T, c confCase) {
	if c.KnownDeviation && os.Getenv("CONFORMANCE_RUN_DEVIATIONS") == "" {
		t.Skip("known deviation, see conformance/DEVIATIONS.md")
	}
	calls := map[string]*atomic.Int32{}
	var inFlight, peak atomic.Int32
	mode := c.Mode
	if mode == "" {
		mode = "run"
	}
	reg, regErr := confRegistry(c.Ops, calls, &inFlight, &peak)
	switch mode {
	case "register":
		if c.Expect.RegisterError == nil {
			if regErr != nil {
				t.Fatalf("unexpected registration error: %v", regErr)
			}
			return
		}
		if regErr == nil {
			t.Fatalf("registration must fail for %q", c.Expect.RegisterError.Op)
		}
		if !strings.HasPrefix(regErr.Error(), c.Expect.RegisterError.Op+":") {
			t.Fatalf("registration failed on the wrong op: %v", regErr)
		}
		return
	case "fingerprint":
		if regErr != nil {
			t.Fatal(regErr)
		}
		reg2, err := confRegistry(c.OpsB, map[string]*atomic.Int32{}, &inFlight, &peak)
		if err != nil {
			t.Fatal(err)
		}
		f1, f2 := reg.Capabilities().Fingerprint, reg2.Capabilities().Fingerprint
		re := regexp.MustCompile(`^sha256:[0-9a-f]{32}$`)
		if !re.MatchString(f1) || !re.MatchString(f2) {
			t.Fatalf("fingerprint format: %q %q", f1, f2)
		}
		if (f1 == f2) != (c.Expect.Fingerprint == "equal") {
			t.Fatalf("fingerprints %q vs %q, want %s", f1, f2, c.Expect.Fingerprint)
		}
		return
	}
	if regErr != nil {
		t.Fatalf("registering stub ops: %v", regErr)
	}

	var out bytes.Buffer
	e := NewEngine(reg)
	e.Stdout = &out
	if l := c.Limits; l != nil {
		e.Policy.Limits = js.Limits{MaxSteps: l.MaxSteps, MaxCallDepth: l.MaxCallDepth, MaxConcurrency: l.MaxConcurrency, Timeout: time.Duration(l.TimeoutMS) * time.Millisecond}
	}
	if p := c.Policy; p != nil {
		deny := p.DenySideEffects
		e.Policy.Allow = func(_ context.Context, op *Operation) error {
			for _, d := range deny {
				if string(op.SideEffects) == d {
					return errors.New("side effect " + d + " not allowed")
				}
			}
			return nil
		}
	}
	file := c.File
	if file == "" {
		file = "program.js"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var diags []Diagnostic
	var res Result
	var prog *Program
	switch mode {
	case "check":
		prog, diags = e.Compile(c.Program, file)
	case "eval":
		res = e.EvalWithInput(ctx, c.Program, c.Input)
		diags = res.Diagnostics
	default:
		res = e.RunWithInput(ctx, c.Program, file, c.Input)
		diags = res.Diagnostics
	}
	x := c.Expect

	for name, n := range x.Calls {
		got := 0
		if cnt := calls[name]; cnt != nil {
			got = int(cnt.Load())
		}
		if got != n {
			t.Errorf("op %s called %d times, want %d", name, got, n)
		}
	}
	if x.Stdout != nil && out.String() != *x.Stdout {
		t.Errorf("stdout %q, want %q", out.String(), *x.Stdout)
	}
	if x.MaxInFlightAtMost > 0 && int(peak.Load()) > x.MaxInFlightAtMost {
		t.Errorf("peak in-flight operations %d > %d", peak.Load(), x.MaxInFlightAtMost)
	}
	if mode == "check" && x.Operations != nil {
		got := prog.Operations
		if got == nil {
			got = []string{}
		}
		if !reflect.DeepEqual(got, x.Operations) {
			t.Errorf("operations %v, want %v", got, x.Operations)
		}
	}

	if x.Exit != nil {
		exit := 0
		switch {
		case mode == "check":
			if hasErrors(diags) {
				exit = 2
			}
		case res.NotRun:
			exit = 2
		case !res.OK:
			exit = 1
		}
		if exit != *x.Exit {
			t.Errorf("exit class %d, want %d (%+v)", exit, *x.Exit, res)
		}
	}

	switch {
	case x.Diagnostics != nil:
		want := *x.Diagnostics
		if mode != "check" && (res.OK || !res.NotRun) {
			t.Fatalf("expected diagnostics and nothing run, got %+v", res)
		}
		if len(diags) != len(want) {
			t.Fatalf("got %d diagnostics %v, want %d", len(diags), diags, len(want))
		}
		for i, w := range want {
			g := diags[i]
			if w.File != "" && g.File != w.File {
				t.Errorf("diag %d file %q want %q", i, g.File, w.File)
			}
			if w.Severity != "" && g.Severity != w.Severity {
				t.Errorf("diag %d severity %q want %q", i, g.Severity, w.Severity)
			}
			if w.Line != 0 && g.Line != w.Line || w.Col != 0 && g.Col != w.Col {
				t.Errorf("diag %d at %d:%d want %d:%d (%s)", i, g.Line, g.Col, w.Line, w.Col, g.Message)
			}
			if !strings.Contains(g.Message, w.MessageContains) {
				t.Errorf("diag %d message %q lacks %q", i, g.Message, w.MessageContains)
			}
			if !strings.Contains(g.Hint, w.HintContains) {
				t.Errorf("diag %d hint %q lacks %q", i, g.Hint, w.HintContains)
			}
		}
	case x.Error != nil:
		if res.OK {
			t.Fatalf("expected failure containing %q, got value %v", x.Error.MessageContains, res.Value)
		}
		if !strings.Contains(res.Error, x.Error.MessageContains) {
			t.Fatalf("error %q lacks %q", res.Error, x.Error.MessageContains)
		}
		if x.Error.Line != 0 {
			if n := len(res.Diagnostics); n == 0 || res.Diagnostics[n-1].Line != x.Error.Line {
				t.Errorf("error line: got diagnostics %v, want line %d", res.Diagnostics, x.Error.Line)
			}
		}
	case x.Result != nil:
		if !res.OK {
			t.Fatalf("run failed: %s %v", res.Error, res.Diagnostics)
		}
		var want any
		if err := json.Unmarshal(x.Result, &want); err != nil {
			t.Fatal(err)
		}
		if got := canon(t, res.Value); !reflect.DeepEqual(got, want) {
			gb, _ := json.Marshal(got)
			t.Fatalf("result %s, want %s", gb, x.Result)
		}
	}
}
