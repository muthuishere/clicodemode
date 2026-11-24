package codemode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/clicodemode/js"
)

// SES-6: Define adds host globals; ops follows Register/Unregister between runs.
func TestSessionDefineAndOpsRefresh(t *testing.T) {
	e, _ := sessionEngine(t)
	s := e.NewSession(SessionConfig{})
	defer s.Close()
	ctx := context.Background()
	s.Run(ctx, "1", RunOptions{})
	e.Registry.MustRegister(Operation{Name: "late.op", Description: "added later", Handler: func(context.Context, map[string]any) (any, error) { return "late", nil }})
	if r := s.Run(ctx, "await ops.late.op()", RunOptions{}); r.Value != "late" {
		t.Fatalf("new op not on ops: %+v", r)
	}
	if !e.Registry.Unregister("late.op") || e.Registry.Unregister("late.op") {
		t.Fatal("Unregister should report true once")
	}
	if r := s.Run(ctx, "typeof ops.late", RunOptions{}); r.Value != "undefined" {
		t.Fatalf("removed op still on ops: %+v", r)
	}
	if r := s.Run(ctx, "await ops.late.op()", RunOptions{}); r.OK || !r.NotRun {
		t.Fatalf("removed op must be a check error: %+v", r)
	}
	s.Define("gh", js.FromGo(map[string]any{"org": "acme"}))
	if r := s.Run(ctx, "gh.org", RunOptions{}); r.Value != "acme" {
		t.Fatalf("Define: %+v", r)
	}
}

type ctxKey struct{}

// SES-7: handlers get a ctx that descends from Run's ctx: its values, and its
// cancellation even after the run detached.
func TestSessionHandlerContext(t *testing.T) {
	reg := NewRegistry("t")
	got := make(chan any, 1)
	reg.MustRegister(Operation{Name: "who", Description: "d", Handler: func(ctx context.Context, _ map[string]any) (any, error) {
		got <- ctx.Value(ctxKey{})
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	s := NewEngine(reg).NewSession(SessionConfig{DetachAfter: 10 * time.Millisecond})
	defer s.Close()
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "alice"))
	r := s.Run(ctx, "await ops.who()", RunOptions{})
	if !r.Detached {
		t.Fatalf("%+v", r)
	}
	if v := <-got; v != "alice" {
		t.Fatalf("handler ctx value %v", v)
	}
	cancel() // after Run returned: still stops the run
	evs := pollUntilDone(t, s, r.Handle)
	if evs[0].Kind != "error" {
		t.Fatalf("%+v", evs)
	}
}

// SES-8: PollWait blocks until an event, the end of the run, or ctx.
func TestSessionPollWait(t *testing.T) {
	e, _ := sessionEngine(t)
	s := e.NewSession(SessionConfig{DetachAfter: 5 * time.Millisecond})
	defer s.Close()
	r := s.Run(context.Background(), "await ops.slow.wait({ms: 40}); print('hi'); 7", RunOptions{})
	start := time.Now()
	evs, done, err := s.PollWait(context.Background(), r.Handle)
	if err != nil || len(evs) == 0 || evs[0].Text != "hi" || time.Since(start) < 20*time.Millisecond {
		t.Fatalf("first wait: %+v %v %v after %v", evs, done, err, time.Since(start))
	}
	for !done {
		var more []Event
		more, done, err = s.PollWait(context.Background(), r.Handle)
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, more...)
	}
	if evs[len(evs)-1].Kind != "done" {
		t.Fatalf("%+v", evs)
	}
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r2 := s.Run(context.Background(), "await ops.slow.wait({ms: 200})", RunOptions{})
	if _, _, err := s.PollWait(short, r2.Handle); err == nil {
		t.Fatal("PollWait should end with ctx")
	}
}

// SES-9: PauseClock stops the run's timeout and detach clock while a handler
// waits on a human.
func TestSessionPauseClock(t *testing.T) {
	reg := NewRegistry("t")
	reg.MustRegister(Operation{Name: "approve", Description: "d", Handler: func(ctx context.Context, _ map[string]any) (any, error) {
		resume := PauseClock(ctx)
		defer resume()
		time.Sleep(150 * time.Millisecond) // a human thinking
		return "yes", nil
	}})
	e := NewEngine(reg)
	e.Policy.Limits.MaxSteps = 1 << 40 // let the timeout, not the step budget, stop the loop
	s := e.NewSession(SessionConfig{Timeout: 80 * time.Millisecond, DetachAfter: 80 * time.Millisecond})
	defer s.Close()
	r := s.Run(context.Background(), "await ops.approve()", RunOptions{})
	if !r.OK || r.Value != "yes" || r.Detached {
		t.Fatalf("paused time must not count: %+v", r)
	}
	r = s.Run(context.Background(), "while (true) {}", RunOptions{DetachAfter: -1})
	if r.OK || !strings.Contains(r.Error, "timeout") {
		t.Fatalf("unpaused time still counts: %+v", r)
	}
	if resume := PauseClock(context.Background()); resume == nil {
		t.Fatal("outside a session PauseClock returns a no-op")
	}
}

// OP-7: Program.Calls lists static call sites with literal arguments, and
// ValidateArgs checks them against the schema.
func TestProgramCallsAndValidateArgs(t *testing.T) {
	reg := NewRegistry("t")
	reg.MustRegister(Operation{Name: "repo.status", Description: "d",
		InputSchema: []byte(`{"type":"object","required":["path"],"properties":{"path":{"type":"string"},"n":{"type":"number"}}}`),
		Handler:     func(context.Context, map[string]any) (any, error) { return nil, nil }})
	e := NewEngine(reg)
	p, diags := e.Compile("const x = input.n\nawait gh.repos.list({org: 'acme', n: x})\nawait ops.repo.status({path: 1})\ncall('repo.status', {})", "p.js")
	if p == nil {
		t.Fatalf("%v", diags)
	}
	calls := p.Calls()
	b, _ := json.Marshal(calls)
	want := `[{"callee":"gh.repos.list","args":{"n":"\u003cunknown\u003e","org":"acme"},"file":"p.js","line":2,"col":7},{"callee":"ops.repo.status","operation":"repo.status","args":{"path":1},"file":"p.js","line":3,"col":7},{"callee":"call","operation":"repo.status","args":{},"file":"p.js","line":4,"col":1}]`
	if string(b) != want {
		t.Fatalf("calls\n got  %s\n want %s", b, want)
	}
	if errs := e.ValidateArgs("repo.status", calls[1].Args); len(errs) != 1 || !strings.Contains(errs[0], "path") {
		t.Fatalf("%v", errs)
	}
	if errs := e.ValidateArgs("repo.status", map[string]any{"path": "x", "n": Unknown{}}); len(errs) != 0 {
		t.Fatalf("unknown parts are skipped: %v", errs)
	}
	if errs := e.ValidateArgs("repo.statsu", nil); len(errs) != 1 || !strings.Contains(errs[0], "did you mean") {
		t.Fatalf("%v", errs)
	}
	_ = fmt.Sprint
}
