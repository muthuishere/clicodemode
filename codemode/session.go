package codemode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/muthuishere/clicodemode/js"
)

// SessionConfig configures a Session. Zero values pick the defaults.
type SessionConfig struct {
	// DetachAfter is how long Run waits for a program's top level before it
	// returns a handle and lets the program finish in the background
	// (default 30s; negative = never detach).
	DetachAfter time.Duration
	// Timeout bounds each run (top level plus its background work). Zero uses
	// the engine's Policy.Limits.Timeout; a run that hits it stops, the
	// session does not.
	Timeout time.Duration
}

// RunOptions are per-run settings for Session.Run.
type RunOptions struct {
	Input       json.RawMessage // the program's global `input`
	Timeout     time.Duration   // overrides SessionConfig.Timeout for this run
	DetachAfter time.Duration   // overrides SessionConfig.DetachAfter for this run
}

// RunResult is what Session.Run returns.
type RunResult struct {
	Handle      int64        `json:"handle"`
	OK          bool         `json:"ok"`
	Value       any          `json:"value,omitempty"`
	Error       string       `json:"error,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
	// Detached is true when the top level was still running at the detach
	// deadline; its result arrives later as a "result" or "error" event.
	Detached bool `json:"detached,omitempty"`
	// Pending is true when background work (unawaited async calls, .then
	// callbacks) was still running when Run returned. Its output and errors
	// arrive through Poll.
	Pending bool `json:"pending,omitempty"`
	// Events are the events this run produced before Run returned (console
	// output, in order). They are not returned again by Poll.
	Events []Event `json:"events,omitempty"`
	NotRun bool    `json:"not_run,omitempty"`
	MS     int64   `json:"ms"`
}

// Event is something a run produced. Kinds: "output" (print/console.*),
// "result" (a detached top level finished), "error" (a detached top level
// failed, or background work failed after Run returned), "done" (the run and
// all its background work finished).
type Event struct {
	Seq         int64        `json:"seq"`
	Handle      int64        `json:"handle"`
	Kind        string       `json:"kind"`
	Text        string       `json:"text,omitempty"`
	Value       any          `json:"value,omitempty"`
	Error       string       `json:"error,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// Var is one persistent top-level binding, for showing a model what is in
// scope.
type Var struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Type    string `json:"type"`
	Preview string `json:"preview"`
}

// Session keeps one JavaScript top-level scope alive across many programs,
// like a REPL. Every Run is checked before it runs (as Engine.Run is), gets
// its own context, limits and timeout, and may leave work running in the
// background. Operations are the engine's registered operations, validated
// exactly as in Engine.Run. A Session is safe for concurrent use; JS from
// different runs never runs at the same time.
type Session struct {
	e   *Engine
	cfg SessionConfig
	sh  *js.Shared

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	next    int64
	seq     int64
	runs    map[int64]*sessionRun
	events  []Event
	closed  bool
	sources map[int64]string
}

type sessionRun struct {
	cancel   context.CancelFunc
	returned bool // Run has returned to its caller
	detached bool // Run returned before the top level finished
	done     bool
}

// NewSession starts a session over the engine's operations.
func (e *Engine) NewSession(cfg SessionConfig) *Session {
	if cfg.DetachAfter == 0 {
		cfg.DetachAfter = 30 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{e: e, cfg: cfg, sh: js.NewShared(), ctx: ctx, cancel: cancel,
		runs: map[int64]*sessionRun{}, sources: map[int64]string{}}
}

// Run checks and runs src in the session scope. Top-level declarations stay
// for later runs, and a later run may declare the same name again. ctx
// cancels this run only while Run has not returned; after that use Cancel.
func (s *Session) Run(ctx context.Context, src string, opts RunOptions) RunResult {
	start := time.Now()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return RunResult{Error: "session is closed", NotRun: true}
	}
	s.next++
	id := s.next
	s.mu.Unlock()
	file := fmt.Sprintf("<run %d>", id)

	p, diags := s.e.Compile(src, file)
	if hasErrors(diags) {
		return RunResult{Handle: id, Error: "program has errors; nothing was run", Diagnostics: withSnippets(diags, src), NotRun: true, MS: time.Since(start).Milliseconds()}
	}
	var input js.Value
	if len(opts.Input) > 0 {
		v, err := js.ParseJSON(opts.Input) // keeps document key order
		if err != nil {
			return RunResult{Handle: id, Error: "input is not valid JSON: " + err.Error(), NotRun: true}
		}
		input = v
	}

	limits := s.e.Policy.Limits
	if t := cmpDur(opts.Timeout, s.cfg.Timeout); t > 0 {
		limits.Timeout = t
	}
	runCtx, runCancel := context.WithCancel(s.ctx)
	s.mu.Lock()
	s.runs[id] = &sessionRun{cancel: runCancel}
	s.sources[id] = src
	s.mu.Unlock()

	it := s.e.newInterpCfg(runCtx, file, js.Config{Limits: limits, Stdout: &eventWriter{s: s, id: id}, Loader: s.e.loader(file), Shared: s.sh})
	it.Define("input", input)

	type top struct {
		v   any
		err error
	}
	topc := make(chan top, 1)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer runCancel()
		defer it.Close()
		v, err := it.RunTop(p.ast, file)
		var g any
		if err == nil {
			it.Locked(func() { g = js.ToGo(v) })
		} else {
			it.Close() // a failed top level stops its background work too
		}
		// Deliver the top-level outcome to Run, or as an event when Run has
		// already detached (decided under s.mu, so exactly one happens).
		s.mu.Lock()
		detached := s.runs[id].detached
		if !detached {
			topc <- top{g, err}
		}
		s.mu.Unlock()
		if detached {
			if err != nil {
				d := withSnippets([]Diagnostic{fromErr(err, file)}, src)
				s.emit(Event{Handle: id, Kind: "error", Error: d[0].String(), Diagnostics: d})
			} else {
				s.emit(Event{Handle: id, Kind: "result", Value: g})
			}
		}
		werr := it.Wait(p.ast, file)
		if err == nil && werr != nil {
			d := withSnippets([]Diagnostic{fromErr(werr, file)}, src)
			s.emit(Event{Handle: id, Kind: "error", Error: d[0].String(), Diagnostics: d})
		}
		s.finishRun(id)
	}()

	// Cancel on the caller's ctx until Run returns.
	stopWatch := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			runCancel()
		case <-stopWatch:
		}
	}()
	defer close(stopWatch)

	detach := cmpDur(opts.DetachAfter, s.cfg.DetachAfter)
	var timer <-chan time.Time
	if detach > 0 {
		t := time.NewTimer(detach)
		defer t.Stop()
		timer = t.C
	}
	res := RunResult{Handle: id}
	var t top
	gotTop := false
	select {
	case t = <-topc:
		gotTop = true
	case <-timer:
		s.mu.Lock()
		select {
		case t = <-topc: // finished at the same moment
			gotTop = true
		default:
			s.runs[id].detached = true
		}
		s.mu.Unlock()
	}
	switch {
	case gotTop:
		res.OK = t.err == nil
		res.Value = t.v
		if t.err != nil {
			res.Diagnostics = withSnippets([]Diagnostic{fromErr(t.err, file)}, src)
			res.Error = res.Diagnostics[0].String()
			res.Value = nil
		}
	default:
		res.Detached = true
	}
	s.mu.Lock()
	if r := s.runs[id]; r != nil {
		r.returned = true
		res.Pending = !r.done
	}
	res.Events = s.takeLocked(id)
	s.mu.Unlock()
	res.MS = time.Since(start).Milliseconds()
	return res
}

func cmpDur(a, b time.Duration) time.Duration {
	if a != 0 {
		return a
	}
	return b
}

// Poll returns the events produced since the last Poll (or since Run
// returned) for one run, or for every run when handle is 0, and whether that
// run (or every run) has finished, background work included.
func (s *Session) Poll(handle int64) (events []Event, done bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events = s.takeLocked(handle)
	if handle == 0 {
		done = true
		for _, r := range s.runs {
			if !r.done {
				done = false
			}
		}
		return events, done
	}
	r := s.runs[handle]
	return events, r == nil || r.done
}

// Cancel stops one run and its background work. It reports whether the run
// was still active.
func (s *Session) Cancel(handle int64) bool {
	s.mu.Lock()
	r := s.runs[handle]
	s.mu.Unlock()
	if r == nil || r.done {
		return false
	}
	r.cancel()
	return true
}

// Vars lists the persistent top-level bindings with short previews, sorted
// by name.
func (s *Session) Vars() []Var {
	var out []Var
	s.sh.Lock(func() {
		for _, b := range s.sh.Bindings() {
			out = append(out, Var{Name: b.Name, Kind: b.Kind, Type: js.TypeOf(b.Value), Preview: preview(js.Inspect(b.Value), 80)})
		}
	})
	return out
}

// Reset forgets every persistent top-level binding. Runs still in flight
// keep the scope they started with.
func (s *Session) Reset() { s.sh.Lock(s.sh.Reset) }

// Close cancels every run and waits for them to stop.
func (s *Session) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
}

func (s *Session) finishRun(id int64) {
	s.mu.Lock()
	if r := s.runs[id]; r != nil {
		r.done = true
	}
	delete(s.sources, id)
	s.mu.Unlock()
	s.emit(Event{Handle: id, Kind: "done"})
}

func (s *Session) emit(ev Event) {
	s.mu.Lock()
	s.seq++
	ev.Seq = s.seq
	s.events = append(s.events, ev)
	s.mu.Unlock()
}

func (s *Session) takeLocked(handle int64) []Event {
	var out, keep []Event
	for _, ev := range s.events {
		if handle == 0 || ev.Handle == handle {
			out = append(out, ev)
		} else {
			keep = append(keep, ev)
		}
	}
	s.events = keep
	return out
}

// eventWriter turns a run's print/console output into "output" events.
type eventWriter struct {
	s  *Session
	id int64
}

func (w *eventWriter) Write(b []byte) (int, error) {
	w.s.emit(Event{Handle: w.id, Kind: "output", Text: strings.TrimSuffix(string(b), "\n")})
	return len(b), nil
}

func preview(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// withSnippets adds the offending source line and a caret to each
// diagnostic that has a position, so a model can fix the program in one go.
func withSnippets(ds []Diagnostic, src string) []Diagnostic {
	lines := strings.Split(src, "\n")
	for i := range ds {
		d := &ds[i]
		if d.Line < 1 || d.Line > len(lines) || d.Snippet != "" {
			continue
		}
		l := strings.TrimRight(lines[d.Line-1], "\r")
		pad := ""
		if d.Col > 1 {
			pad = strings.Repeat(" ", min(d.Col-1, len(l)))
		}
		d.Snippet = fmt.Sprintf("%d | %s\n%s | %s^", d.Line, l, strings.Repeat(" ", len(fmt.Sprint(d.Line))), pad)
	}
	return ds
}
