package codemode

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muthuishere/clicodemode/js"
)

func sessionEngine(t *testing.T) (*Engine, *atomic.Int64) {
	t.Helper()
	reg := NewRegistry("test 1.0")
	var calls atomic.Int64
	reg.MustRegister(Operation{Name: "slow.wait", Description: "Sleep ms.",
		InputSchema: []byte(`{"type":"object","required":["ms"],"properties":{"ms":{"type":"number"}}}`),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			calls.Add(1)
			select {
			case <-time.After(time.Duration(args["ms"].(float64)) * time.Millisecond):
				return "slept", nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}})
	return NewEngine(reg), &calls
}

// SES-1: top-level declarations persist across runs; a later run may declare
// a name again, a single run may not.
func TestSessionPersistentScope(t *testing.T) {
	e, _ := sessionEngine(t)
	s := e.NewSession(SessionConfig{})
	defer s.Close()
	ctx := context.Background()
	must := func(src string, want any) {
		t.Helper()
		r := s.Run(ctx, src, RunOptions{})
		if !r.OK {
			t.Fatalf("%q: %s", src, r.Error)
		}
		if want != nil && r.Value != want {
			t.Fatalf("%q = %v, want %v", src, r.Value, want)
		}
	}
	must("let x = 2; const k = 'a'; function sq(n) { return n * n }", nil)
	must("x + sq(3)", float64(11))
	must("let x = 40; x + 2", float64(42)) // REPL redeclare
	must("const k = 'b'; k", "b")
	if r := s.Run(ctx, "let y = 1; let y = 2", RunOptions{}); r.OK || !strings.Contains(r.Error, "already been declared") {
		t.Fatalf("same-run redeclare: %+v", r)
	}
	if r := s.Run(ctx, "k = 'c'", RunOptions{}); r.OK {
		t.Fatal("assigning a const from an earlier run must fail")
	}
	must("const p = Promise.resolve(5)", nil)
	must("await p", float64(5))
}

// SES-2: input is per run; checks run before anything runs.
func TestSessionInputAndCheck(t *testing.T) {
	e, calls := sessionEngine(t)
	s := e.NewSession(SessionConfig{})
	defer s.Close()
	r := s.Run(context.Background(), "input.a * 2", RunOptions{Input: []byte(`{"a":21}`)})
	if !r.OK || r.Value != float64(42) {
		t.Fatalf("%+v", r)
	}
	r = s.Run(context.Background(), "await ops.slow.wait({ms: 'x'})", RunOptions{})
	if r.OK || !r.NotRun || calls.Load() != 0 {
		t.Fatalf("bad literal arg must be a check error: %+v", r)
	}
	if r.Diagnostics[0].Snippet == "" || !strings.Contains(r.Diagnostics[0].Snippet, "^") {
		t.Fatalf("no snippet: %+v", r.Diagnostics)
	}
}

// SES-3: a per-run timeout or cancel stops that run only; the session and
// its scope survive.
func TestSessionPerRunTimeoutAndCancel(t *testing.T) {
	e, _ := sessionEngine(t)
	s := e.NewSession(SessionConfig{})
	defer s.Close()
	ctx := context.Background()
	s.Run(ctx, "let kept = 'yes'", RunOptions{})
	r := s.Run(ctx, "while (true) {}", RunOptions{Timeout: 50 * time.Millisecond})
	if r.OK || !strings.Contains(r.Error, "cancel") && !strings.Contains(r.Error, "deadline") {
		t.Fatalf("timeout: %+v", r)
	}
	cctx, cancel := context.WithCancel(ctx)
	time.AfterFunc(30*time.Millisecond, cancel)
	r = s.Run(cctx, "await ops.slow.wait({ms: 5000})", RunOptions{})
	if r.OK {
		t.Fatalf("cancel: %+v", r)
	}
	if r := s.Run(ctx, "kept", RunOptions{}); !r.OK || r.Value != "yes" {
		t.Fatalf("session did not survive: %+v", r)
	}
}

// SES-4: Run detaches at the deadline; Poll returns the outcome later.
// Background work started without await keeps running after Run returns.
func TestSessionDetachAndPoll(t *testing.T) {
	e, _ := sessionEngine(t)
	s := e.NewSession(SessionConfig{DetachAfter: 30 * time.Millisecond})
	defer s.Close()
	ctx := context.Background()
	r := s.Run(ctx, "print('start'); const v = await ops.slow.wait({ms: 150}); print('end'); v", RunOptions{})
	if !r.Detached || r.OK {
		t.Fatalf("want detached: %+v", r)
	}
	if len(r.Events) != 1 || r.Events[0].Text != "start" {
		t.Fatalf("events at return: %+v", r.Events)
	}
	evs := pollUntilDone(t, s, r.Handle)
	kinds := []string{}
	for _, ev := range evs {
		kinds = append(kinds, ev.Kind+":"+ev.Text)
	}
	if got := strings.Join(kinds, ","); got != "output:end,result:,done:" {
		t.Fatalf("events %s", got)
	}
	if evs[1].Value != "slept" {
		t.Fatalf("result %+v", evs[1])
	}

	// Top level returns at once; unawaited work continues (no deadline needed).
	r = s.Run(ctx, "let log = []; (async () => { await ops.slow.wait({ms: 60}); log.push('bg'); print('bg done') })(); 'top'", RunOptions{DetachAfter: -1})
	if !r.OK || r.Value != "top" || !r.Pending || r.Detached {
		t.Fatalf("background: %+v", r)
	}
	evs = pollUntilDone(t, s, r.Handle)
	if evs[0].Text != "bg done" {
		t.Fatalf("%+v", evs)
	}
	if r := s.Run(ctx, "log", RunOptions{}); r.Value.([]any)[0] != "bg" {
		t.Fatalf("%+v", r)
	}

	// A background rejection nobody handled is an error event.
	r = s.Run(ctx, "Promise.reject(new Error('late')); 1", RunOptions{})
	evs = pollUntilDone(t, s, r.Handle)
	if r.OK && (len(evs) < 1 || evs[0].Kind != "error" || !strings.Contains(evs[0].Error, "late")) {
		t.Fatalf("want error event: %+v %+v", r, evs)
	}

	// Cancel stops a detached run.
	r = s.Run(ctx, "await ops.slow.wait({ms: 5000})", RunOptions{})
	if !r.Detached || !s.Cancel(r.Handle) {
		t.Fatalf("%+v", r)
	}
	evs = pollUntilDone(t, s, r.Handle)
	if evs[0].Kind != "error" {
		t.Fatalf("%+v", evs)
	}
}

func pollUntilDone(t *testing.T, s *Session, h int64) []Event {
	t.Helper()
	var all []Event
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		evs, done := s.Poll(h)
		all = append(all, evs...)
		if done {
			evs, _ := s.Poll(h)
			return append(all, evs...)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run %d did not finish; events %+v", h, all)
	return nil
}

// SES-5: Vars lists persistent bindings with previews; Reset clears them;
// Close cancels everything.
func TestSessionVarsResetClose(t *testing.T) {
	e, _ := sessionEngine(t)
	s := e.NewSession(SessionConfig{})
	ctx := context.Background()
	s.Run(ctx, "const users = [{name: 'a'}, {name: 'b'}]; let n = 3; function f() {}", RunOptions{})
	vs := s.Vars()
	if len(vs) != 3 || vs[0].Name != "f" || vs[1].Name != "n" || vs[1].Preview != "3" || vs[2].Type != "object" {
		t.Fatalf("%+v", vs)
	}
	s.Reset()
	if len(s.Vars()) != 0 {
		t.Fatal("reset kept vars")
	}
	if r := s.Run(ctx, "n", RunOptions{}); r.OK {
		t.Fatal("n survived reset")
	}
	r := s.Run(ctx, "await ops.slow.wait({ms: 5000})", RunOptions{DetachAfter: 10 * time.Millisecond})
	if !r.Detached {
		t.Fatalf("%+v", r)
	}
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not stop the run")
	}
	if r := s.Run(ctx, "1", RunOptions{}); r.OK {
		t.Fatal("run after Close")
	}
}

// Session runs must not disturb one-shot engine runs (aos, ctx-optimize).
func TestSessionLeavesEngineRunUnchanged(t *testing.T) {
	e, _ := sessionEngine(t)
	s := e.NewSession(SessionConfig{})
	defer s.Close()
	s.Run(context.Background(), "let x = 1", RunOptions{})
	if r := e.Run(context.Background(), "typeof x", "<p>"); r.Value != "undefined" {
		t.Fatalf("%+v", r)
	}
	_ = js.Undefined
}
