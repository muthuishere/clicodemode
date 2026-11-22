package js

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// WorkflowConfig configures the workflow/step globals.
type WorkflowConfig struct {
	// CLI is the executable named in toYAML's `run: <CLI> code run-step ...`.
	CLI string
	// BaseDir is the directory plan refs are relative to (the entry
	// program's directory). Empty means refs use the module path as is.
	BaseDir string
}

// InstallWorkflow defines the globals `workflow(name, steps, opts?)` and
// `step(id, fn, opts?)`.
//
// Two forms share one definition: .run(input?) executes the steps
// in-process (imperative, not durable); .plan() and .toYAML() export a
// declarative, serializable plan in the WFNexus shape, where each step
// function becomes a static module ref "./steps/health.js#inspect".
func InstallWorkflow(it *Interp, cfg WorkflowConfig) {
	if cfg.CLI == "" {
		cfg.CLI = "cli"
	}
	var mu sync.Mutex
	steps := map[*Object]*wfStep{}
	it.Define("step", bi("step", func(r *Runner, _ Value, args []Value) (Value, error) {
		s, err := parseStep(args)
		if err != nil {
			return nil, err
		}
		o := frozen("id", s.id, "kind", "step")
		mu.Lock()
		steps[o] = s
		mu.Unlock()
		return o, nil
	}))
	it.Define("workflow", bi("workflow", func(r *Runner, _ Value, args []Value) (Value, error) {
		name, ok := arg(args, 0).(string)
		if !ok || name == "" {
			return nil, wfErr("workflow(name, steps, opts?): name must be a non-empty string")
		}
		arr, ok := arg(args, 1).(*Array)
		if !ok || len(arr.Elems) == 0 {
			return nil, wfErr("workflow(name, steps, opts?): steps must be a non-empty array of step(...) values")
		}
		w := &wf{name: name, maxParallel: 4, cfg: cfg, index: map[string]int{}}
		for i, e := range arr.Elems {
			o, _ := e.(*Object)
			mu.Lock()
			s := steps[o]
			mu.Unlock()
			if s == nil {
				return nil, wfErr(fmt.Sprintf("workflow %q: steps[%d] is not a step(...) value", name, i))
			}
			if _, dup := w.index[s.id]; dup {
				return nil, wfErr(fmt.Sprintf("workflow %q: duplicate step id %q", name, s.id))
			}
			w.index[s.id] = len(w.steps)
			w.steps = append(w.steps, s)
		}
		if opts, ok := arg(args, 2).(*Object); ok {
			for _, k := range opts.Keys() {
				v, _ := opts.Get(k)
				switch k {
				case "maxParallel":
					n, ok := v.(float64)
					if !ok || n < 1 || n != math.Trunc(n) {
						return nil, wfErr("workflow opts.maxParallel must be an integer >= 1")
					}
					w.maxParallel = int(n)
				default:
					return nil, wfErr(fmt.Sprintf("workflow: unknown option %q (known: maxParallel)", k))
				}
			}
		} else if arg(args, 2) != Undefined {
			return nil, wfErr("workflow(name, steps, opts?): opts must be an object")
		}
		if err := w.validate(); err != nil {
			return nil, err
		}
		return frozen(
			"name", name,
			"run", bi("run", func(r *Runner, _ Value, a []Value) (Value, error) { return w.run(r.it, arg(a, 0)), nil }),
			"plan", bi("plan", func(r *Runner, _ Value, _ []Value) (Value, error) { return w.plan() }),
			"toYAML", bi("toYAML", func(r *Runner, _ Value, _ []Value) (Value, error) { return w.toYAML() }),
		), nil
	}))
}

func wfErr(msg string) error { return &Throw{Value: makeError("TypeError", msg)} }

// ---------------- definitions ----------------

type guard struct {
	path      string
	hasEquals bool
	equals    any    // JSON data
	eqJSON    []byte // json.Marshal(equals)
	exists    bool
}

type wfStep struct {
	id              string
	fn              Value
	needs           []string
	when            []guard
	attempts        int
	backoff         time.Duration
	timeout         time.Duration
	loop            string // "" | foreach | while | until
	items           []Value
	itemsGo         []any
	parallel        int
	loopGuard       *guard
	maxIter         int
	continueOnError bool
	outcomes        []string // declared {outcome} values; exported as exit-codes
}

// OutcomeExitBase is the first exit code run-step uses for declared outcomes:
// outcomes[i] exits with OutcomeExitBase+i.
const OutcomeExitBase = 10

// checkOutcome enforces a declared outcome on a step's return value.
func (s *wfStep) checkOutcome(out any) error {
	if len(s.outcomes) == 0 {
		return nil
	}
	m, _ := out.(map[string]any)
	o, _ := m["outcome"].(string)
	for _, d := range s.outcomes {
		if o == d {
			return nil
		}
	}
	return fmt.Errorf("step %q must return {outcome: one of %s}, got %s", s.id, strings.Join(s.outcomes, ", "), jsonStr(m["outcome"]))
}

func parseStep(args []Value) (*wfStep, error) {
	id, ok := arg(args, 0).(string)
	if !ok || id == "" {
		return nil, wfErr("step(id, fn, opts?): id must be a non-empty string")
	}
	s := &wfStep{id: id, fn: arg(args, 1), attempts: 1, parallel: 1}
	switch s.fn.(type) {
	case *Function, *Builtin:
	default:
		return nil, wfErr(fmt.Sprintf("step %q: fn must be a function", id))
	}
	bad := func(f string, a ...any) error { return wfErr(fmt.Sprintf("step %q: ", id) + fmt.Sprintf(f, a...)) }
	o, isObj := arg(args, 2).(*Object)
	if !isObj {
		if arg(args, 2) != Undefined {
			return nil, bad("opts must be an object")
		}
		return s, nil
	}
	hasMax := false
	for _, k := range o.Keys() {
		v, _ := o.Get(k)
		switch k {
		case "needs":
			a, ok := v.(*Array)
			if !ok {
				return nil, bad("needs must be an array of step ids")
			}
			for _, e := range a.Elems {
				n, ok := e.(string)
				if !ok {
					return nil, bad("needs must be an array of step ids")
				}
				s.needs = append(s.needs, n)
			}
		case "when":
			var list []Value
			if a, ok := v.(*Array); ok {
				list = a.Elems
			} else {
				list = []Value{v}
			}
			for _, e := range list {
				g, err := parseGuard(e)
				if err != nil {
					return nil, bad("when: %v", err)
				}
				s.when = append(s.when, g)
			}
		case "retry":
			ro, ok := v.(*Object)
			if !ok {
				return nil, bad(`retry must be {attempts, backoff}`)
			}
			for _, rk := range ro.Keys() {
				rv, _ := ro.Get(rk)
				switch rk {
				case "attempts":
					n, ok := rv.(float64)
					if !ok || n < 1 || n != math.Trunc(n) {
						return nil, bad("retry.attempts must be an integer >= 1")
					}
					s.attempts = int(n)
				case "backoff":
					d, err := parseDur(rv)
					if err != nil {
						return nil, bad("retry.backoff: %v", err)
					}
					s.backoff = d
				default:
					return nil, bad("retry: unknown field %q (known: attempts, backoff)", rk)
				}
			}
		case "timeout":
			d, err := parseDur(v)
			if err != nil || d <= 0 {
				return nil, bad(`timeout must be a positive duration ("30s" or milliseconds)`)
			}
			s.timeout = d
		case "foreach":
			a, ok := v.(*Array)
			if !ok {
				return nil, bad("foreach must be an array")
			}
			if s.loop != "" {
				return nil, bad("use exactly one of foreach, while, until")
			}
			s.loop = "foreach"
			s.items = append([]Value{}, a.Elems...)
			for _, e := range a.Elems {
				s.itemsGo = append(s.itemsGo, ToGo(e))
			}
		case "parallel":
			n, ok := v.(float64)
			if !ok || n < 1 || n != math.Trunc(n) {
				return nil, bad("parallel must be an integer >= 1")
			}
			s.parallel = int(n)
		case "while", "until":
			if s.loop != "" {
				return nil, bad("use exactly one of foreach, while, until")
			}
			g, err := parseGuard(v)
			if err != nil {
				return nil, bad("%s: %v", k, err)
			}
			s.loop, s.loopGuard = k, &g
		case "maxIterations":
			n, ok := v.(float64)
			if !ok || n < 1 || n > 999 || n != math.Trunc(n) {
				return nil, bad("maxIterations must be an integer from 1 to 999")
			}
			s.maxIter, hasMax = int(n), true
		case "continueOnError":
			b, ok := v.(bool)
			if !ok {
				return nil, bad("continueOnError must be a boolean")
			}
			s.continueOnError = b
		case "outcomes":
			arr, ok := v.(*Array)
			var list []Value
			if ok {
				list = arr.Elems
			}
			if !ok || len(list) == 0 || len(list) > 200 {
				return nil, bad("outcomes must be a non-empty array of up to 200 strings")
			}
			seen := map[string]bool{}
			for _, x := range list {
				o, ok := x.(string)
				if !ok || o == "" || seen[o] || strings.ContainsAny(o, " \t\n\",:") {
					return nil, bad("outcomes must be unique non-empty words (no spaces, commas or colons)")
				}
				seen[o] = true
				s.outcomes = append(s.outcomes, o)
			}
		default:
			return nil, bad("unknown option %q (known: needs, when, retry, timeout, foreach, parallel, while, until, maxIterations, continueOnError, outcomes)", k)
		}
	}
	if s.loop == "while" || s.loop == "until" {
		if !hasMax {
			return nil, bad("%s requires maxIterations (1..999)", s.loop)
		}
		if s.parallel > 1 {
			return nil, bad("%s loops run one iteration at a time; parallel must be 1", s.loop)
		}
	}
	if s.parallel > 1 && s.loop != "foreach" {
		return nil, bad("parallel only applies to foreach")
	}
	if hasMax && s.loop == "foreach" {
		return nil, bad("maxIterations applies to while/until; foreach is bounded by its array")
	}
	if hasMax && s.loop == "" {
		return nil, bad("maxIterations needs while or until")
	}
	return s, nil
}

func parseGuard(v Value) (guard, error) {
	o, ok := v.(*Object)
	if !ok {
		return guard{}, errors.New("a guard is {path, equals} or {path, exists}")
	}
	var g guard
	hasExists := false
	for _, k := range o.Keys() {
		x, _ := o.Get(k)
		switch k {
		case "path":
			p, ok := x.(string)
			if !ok || p == "" {
				return g, errors.New("guard path must be a non-empty string")
			}
			g.path = p
		case "equals":
			g.hasEquals = true
			g.equals = ToGo(x)
			b, err := json.Marshal(g.equals)
			if err != nil {
				return g, fmt.Errorf("guard equals is not JSON data: %v", err)
			}
			g.eqJSON = b
		case "exists":
			b, ok := x.(bool)
			if !ok {
				return g, errors.New("guard exists must be a boolean")
			}
			g.exists, hasExists = b, true
		default:
			return g, fmt.Errorf("unknown guard field %q (known: path, equals, exists)", k)
		}
	}
	if g.path == "" {
		return g, errors.New("guard needs a path")
	}
	if g.hasEquals == hasExists {
		return g, errors.New("guard needs exactly one of equals or exists")
	}
	return g, nil
}

// parseDur accepts milliseconds (number) or a Go duration string ("2s").
func parseDur(v Value) (time.Duration, error) {
	switch x := v.(type) {
	case float64:
		if x < 0 || math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, errors.New("must be >= 0")
		}
		return time.Duration(x * float64(time.Millisecond)), nil
	case string:
		d, err := time.ParseDuration(x)
		if err != nil || d < 0 {
			return 0, fmt.Errorf(`bad duration %q (use "2s", "500ms" or milliseconds)`, x)
		}
		return d, nil
	}
	return 0, errors.New(`must be a duration string like "2s" or milliseconds`)
}

type wf struct {
	name        string
	steps       []*wfStep
	index       map[string]int
	maxParallel int
	cfg         WorkflowConfig
}

func (w *wf) hasNeeds() bool {
	for _, s := range w.steps {
		if len(s.needs) > 0 {
			return true
		}
	}
	return false
}

func (w *wf) validate() error {
	for _, s := range w.steps {
		for _, n := range s.needs {
			if n == s.id {
				return wfErr(fmt.Sprintf("workflow %q: step %q needs itself", w.name, s.id))
			}
			if _, ok := w.index[n]; !ok {
				return wfErr(fmt.Sprintf("workflow %q: step %q needs unknown step %q", w.name, s.id, n))
			}
		}
	}
	// cycle detection: DFS in declaration order, reporting the path.
	state := make([]int, len(w.steps)) // 0 new 1 on stack 2 done
	var stack []string
	var visit func(i int) []string
	visit = func(i int) []string {
		state[i] = 1
		stack = append(stack, w.steps[i].id)
		for _, n := range w.steps[i].needs {
			j := w.index[n]
			if state[j] == 1 {
				start := 0
				for k, id := range stack {
					if id == n {
						start = k
					}
				}
				return append(append([]string{}, stack[start:]...), n)
			}
			if state[j] == 0 {
				if c := visit(j); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[i] = 2
		return nil
	}
	for i := range w.steps {
		if state[i] == 0 {
			if c := visit(i); c != nil {
				return wfErr(fmt.Sprintf("workflow %q: dependency cycle %s", w.name, strings.Join(c, " → ")))
			}
		}
	}
	return nil
}

// deps returns the effective dependencies: needs, or the previous step when
// no step declares needs (strictly sequential).
func (w *wf) deps(i int) []int {
	if !w.hasNeeds() {
		if i == 0 {
			return nil
		}
		return []int{i - 1}
	}
	var out []int
	for _, n := range w.steps[i].needs {
		out = append(out, w.index[n])
	}
	return out
}

// ---------------- imperative run ----------------

type stepState struct {
	status     string // pending running done skipped failed
	output     Value
	outGo      any
	hasOut     bool
	err        string
	attempts   int
	skipReason string
	blocked    bool // skipped because a dependency failed
}

func (w *wf) run(it *Interp, input Value) *Promise {
	p := it.newPromise()
	inputGo := ToGo(input) // caller holds the lock
	it.tasks.Add(1)
	go func() {
		defer it.tasks.Done()
		states := w.execute(it, input, inputGo)
		res := NewObject()
		ok := true
		so := NewObject()
		for i, s := range w.steps {
			st := states[i]
			if st.status == "failed" && !s.continueOnError {
				ok = false
			}
			o := NewObject()
			o.Set("status", st.status)
			if st.hasOut {
				o.Set("output", st.output)
			} else {
				o.Set("output", Undefined)
			}
			if st.err != "" {
				o.Set("error", st.err)
			}
			o.Set("attempts", float64(st.attempts))
			if st.skipReason != "" {
				o.Set("skipReason", st.skipReason)
			}
			so.Set(s.id, o)
		}
		if it.ctx.Err() != nil {
			ok = false
		}
		res.Set("ok", ok)
		res.Set("steps", so)
		p.settle(1, res)
	}()
	return p
}

func (w *wf) execute(it *Interp, input Value, inputGo any) []*stepState {
	states := make([]*stepState, len(w.steps))
	for i := range states {
		states[i] = &stepState{status: "pending"}
	}
	var mu sync.Mutex
	done := make(chan int, len(w.steps))
	sem := make(chan struct{}, w.maxParallel)
	finished, running := 0, 0
	for finished < len(w.steps) {
		mu.Lock()
		progressed := true
		for progressed {
			progressed = false
			for i, s := range w.steps {
				st := states[i]
				if st.status != "pending" {
					continue
				}
				ready := true
				var failedDep string
				for _, d := range w.deps(i) {
					ds := states[d]
					switch ds.status {
					case "pending", "running":
						ready = false
					case "failed":
						if !w.steps[d].continueOnError && failedDep == "" {
							failedDep = w.steps[d].id
						}
					case "skipped":
						if ds.blocked && failedDep == "" {
							failedDep = w.steps[d].id
						}
					}
				}
				if !ready {
					continue
				}
				progressed = true
				switch {
				case it.ctx.Err() != nil:
					st.status, st.skipReason, st.blocked = "skipped", "cancelled", true
					finished++
					continue
				case failedDep != "":
					st.status, st.skipReason, st.blocked = "skipped", fmt.Sprintf("dependency %q failed", failedDep), true
					finished++
					continue
				}
				if reason := w.checkGuards(s.when, inputGo, states, "when"); reason != "" {
					st.status, st.skipReason = "skipped", reason
					finished++
					continue
				}
				st.status = "running"
				running++
				go func(i int) {
					select {
					case sem <- struct{}{}:
						w.runStep(it, i, input, inputGo, states, &mu)
						<-sem
					case <-it.ctx.Done():
						mu.Lock()
						states[i].status, states[i].err = "failed", "cancelled"
						mu.Unlock()
					}
					done <- i
				}(i)
			}
		}
		mu.Unlock()
		if finished >= len(w.steps) {
			break
		}
		if running == 0 {
			break // cannot happen for a validated DAG
		}
		<-done
		running--
		finished++
	}
	return states
}

// snapshot builds the step context {input, steps, item, index}; caller holds
// the interpreter lock (and reads states under mu).
func (w *wf) snapshot(input Value, states []*stepState, mu *sync.Mutex, self int, selfOut Value, item, index Value) Value {
	so := NewObject()
	mu.Lock()
	for i, s := range w.steps {
		if states[i].status == "done" && states[i].hasOut {
			so.Set(s.id, states[i].output)
		}
	}
	mu.Unlock()
	if selfOut != nil {
		so.Set(w.steps[self].id, selfOut)
	}
	ctx := NewObject()
	ctx.Set("input", input)
	ctx.Set("steps", so)
	ctx.Set("item", item)
	ctx.Set("index", index)
	return ctx
}

func (w *wf) runStep(it *Interp, i int, input Value, inputGo any, states []*stepState, mu *sync.Mutex) {
	s := w.steps[i]
	var out Value
	var outGo any
	attempts := 0
	var err error
	switch s.loop {
	case "":
		out, outGo, attempts, err = w.attempt(it, s, func() Value {
			return w.snapshot(input, states, mu, i, nil, Undefined, Undefined)
		})
	case "foreach":
		out, outGo, attempts, err = w.foreach(it, i, input, states, mu)
	default:
		out, outGo, attempts, err = w.loopGuarded(it, i, input, inputGo, states, mu)
	}
	mu.Lock()
	defer mu.Unlock()
	st := states[i]
	st.attempts = attempts
	if err != nil {
		st.status, st.err = "failed", err.Error()
		return
	}
	st.status, st.output, st.outGo, st.hasOut = "done", out, outGo, true
}

// attempt calls s.fn with retry and backoff; backoff waits end on cancel.
func (w *wf) attempt(it *Interp, s *wfStep, mkArg func() Value) (Value, any, int, error) {
	for a := 1; ; a++ {
		v, g, err := w.call(it, s, mkArg)
		if err == nil {
			err = s.checkOutcome(g)
		}
		if err == nil {
			return v, g, a, nil
		}
		if it.ctx.Err() != nil || a >= s.attempts {
			return nil, nil, a, err
		}
		if s.backoff > 0 {
			t := time.NewTimer(s.backoff)
			select {
			case <-t.C:
			case <-it.ctx.Done():
				t.Stop()
				return nil, nil, a, fmt.Errorf("cancelled during retry backoff: %v", err)
			}
		}
	}
}

// call runs one invocation under the interpreter lock, waiting for a
// returned promise (bounded by the step timeout) without the lock.
func (w *wf) call(it *Interp, s *wfStep, mkArg func() Value) (Value, any, error) {
	it.gil.Lock()
	if it.ctx.Err() != nil {
		it.gil.Unlock()
		return nil, nil, errors.New("cancelled")
	}
	r := &Runner{it: it}
	v, err := r.Call(s.fn, mkArg())
	it.gil.Unlock()
	if err != nil {
		return nil, nil, errors.New(errText(err))
	}
	if p, ok := v.(*Promise); ok {
		p.mu.Lock()
		p.handled = true
		p.mu.Unlock()
		var timeout <-chan time.Time
		if s.timeout > 0 {
			t := time.NewTimer(s.timeout)
			defer t.Stop()
			timeout = t.C
		}
		select {
		case <-p.done:
		case <-it.ctx.Done():
			return nil, nil, errors.New("cancelled")
		case <-timeout:
			return nil, nil, fmt.Errorf("timed out after %s", s.timeout)
		}
		if p.state == 2 {
			return nil, nil, errors.New(errText(&Throw{Value: p.value}))
		}
		v = p.value
	}
	it.gil.Lock()
	g := ToGo(v)
	it.gil.Unlock()
	return v, g, nil
}

func errText(err error) string {
	var t *Throw
	if errors.As(err, &t) {
		if o, ok := t.Value.(*Object); ok && isErrorObject(o) {
			return toString(o)
		}
		return Inspect(t.Value)
	}
	return err.Error()
}

func loopOutput(iters []Value, last Value) Value {
	o := NewObject()
	o.Set("iterations", &Array{Elems: iters})
	o.Set("count", float64(len(iters)))
	o.Set("last", last)
	return o
}

func (w *wf) foreach(it *Interp, i int, input Value, states []*stepState, mu *sync.Mutex) (Value, any, int, error) {
	s := w.steps[i]
	n := len(s.items)
	outs := make([]Value, n)
	var wg sync.WaitGroup
	var emu sync.Mutex
	var first error
	total := 0
	sem := make(chan struct{}, s.parallel)
	for k := 0; k < n; k++ {
		emu.Lock()
		stop := first != nil
		emu.Unlock()
		if stop {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-it.ctx.Done():
			wg.Wait()
			return nil, nil, total, errors.New("cancelled")
		}
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			defer func() { <-sem }()
			v, _, a, err := w.attempt(it, s, func() Value {
				return w.snapshot(input, states, mu, i, nil, s.items[k], float64(k))
			})
			emu.Lock()
			total += a
			if err != nil && first == nil {
				first = fmt.Errorf("%s[%d]: %v", s.id, k, err)
			}
			emu.Unlock()
			outs[k] = v
		}(k)
	}
	wg.Wait()
	if first != nil {
		return nil, nil, total, first
	}
	var last Value = Undefined
	if n > 0 {
		last = outs[n-1]
	}
	out := loopOutput(outs, last)
	it.gil.Lock()
	g := ToGo(out)
	it.gil.Unlock()
	return out, g, total, nil
}

func (w *wf) loopGuarded(it *Interp, i int, input Value, inputGo any, states []*stepState, mu *sync.Mutex) (Value, any, int, error) {
	s := w.steps[i]
	var iters []Value
	var last Value = Undefined
	total := 0
	for k := 0; k < s.maxIter; k++ {
		var selfOut Value
		if k > 0 {
			selfOut = last
		}
		v, g, a, err := w.attempt(it, s, func() Value {
			return w.snapshot(input, states, mu, i, selfOut, Undefined, float64(k))
		})
		total += a
		if err != nil {
			return nil, nil, total, fmt.Errorf("%s[%d]: %v", s.id, k, err)
		}
		iters = append(iters, v)
		last = v
		pass := w.guardPasses(*s.loopGuard, inputGo, states, mu, i, g)
		if (s.loop == "until" && pass) || (s.loop == "while" && !pass) {
			out := loopOutput(iters, last)
			it.gil.Lock()
			og := ToGo(out)
			it.gil.Unlock()
			return out, og, total, nil
		}
	}
	return nil, nil, total, fmt.Errorf("%s loop reached maxIterations %d before its condition ended it", s.loop, s.maxIter)
}

// ---------------- guards ----------------

// resolve walks a WFNexus-style path: "Steps.x.ok", "steps.x.ok", "x.ok"
// (head = longest matching step id) or "input.x". Only done steps resolve.
func (w *wf) resolve(path string, inputGo any, outs map[string]any) (any, bool) {
	rest := path
	switch {
	case path == "input" || path == "Input":
		return inputGo, true
	case strings.HasPrefix(path, "input.") || strings.HasPrefix(path, "Input."):
		return walk(inputGo, strings.Split(path[len("input."):], "."))
	case strings.HasPrefix(path, "Steps.") || strings.HasPrefix(path, "steps."):
		rest = path[len("steps."):]
	}
	best := ""
	for _, s := range w.steps {
		if (rest == s.id || strings.HasPrefix(rest, s.id+".")) && len(s.id) > len(best) {
			best = s.id
		}
	}
	if best == "" {
		return nil, false
	}
	v, ok := outs[best]
	if !ok {
		return nil, false
	}
	if rest == best {
		return v, true
	}
	return walk(v, strings.Split(rest[len(best)+1:], "."))
}

func walk(v any, parts []string) (any, bool) {
	for _, p := range parts {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[p]; !ok {
			return nil, false
		}
	}
	return v, true
}

func (w *wf) outputs(states []*stepState) map[string]any {
	outs := map[string]any{}
	for i, s := range w.steps {
		if states[i].status == "done" && states[i].hasOut {
			outs[s.id] = states[i].outGo
		}
	}
	return outs
}

func evalGuard(g guard, v any, ok bool) bool {
	if !g.hasEquals {
		return ok == g.exists
	}
	if !ok {
		return false
	}
	b, err := json.Marshal(v)
	return err == nil && bytes.Equal(b, g.eqJSON)
}

// checkGuards returns "" when every guard passes, else a skip reason.
// Caller holds mu.
func (w *wf) checkGuards(gs []guard, inputGo any, states []*stepState, label string) string {
	if len(gs) == 0 {
		return ""
	}
	outs := w.outputs(states)
	for _, g := range gs {
		v, ok := w.resolve(g.path, inputGo, outs)
		if evalGuard(g, v, ok) {
			continue
		}
		switch {
		case !ok && g.hasEquals:
			return fmt.Sprintf("%s: %s did not resolve", label, g.path)
		case g.hasEquals:
			got, _ := json.Marshal(v)
			return fmt.Sprintf("%s: %s is %s, not %s", label, g.path, got, g.eqJSON)
		default:
			return fmt.Sprintf("%s: %s exists is %v, want %v", label, g.path, ok, g.exists)
		}
	}
	return ""
}

func (w *wf) guardPasses(g guard, inputGo any, states []*stepState, mu *sync.Mutex, self int, selfGo any) bool {
	mu.Lock()
	outs := w.outputs(states)
	mu.Unlock()
	outs[w.steps[self].id] = selfGo
	v, ok := w.resolve(g.path, inputGo, outs)
	return evalGuard(g, v, ok)
}

// ---------------- declarative plan ----------------

const errInlinePlan = "declarative plans need step functions imported from a module"

func (w *wf) ref(s *wfStep) (string, error) {
	f, ok := s.fn.(*Function)
	if !ok || f.ExportModule == "" || f.ExportName == "" {
		return "", wfErr(fmt.Sprintf("%s (step %q uses an inline function; export it from a .js file and import it)", errInlinePlan, s.id))
	}
	mod := f.ExportModule
	if w.cfg.BaseDir != "" {
		base, err1 := filepath.Abs(w.cfg.BaseDir)
		abs, err2 := filepath.Abs(mod)
		if err1 == nil && err2 == nil {
			if rel, err := filepath.Rel(base, abs); err == nil {
				mod = rel
			}
		}
	}
	mod = filepath.ToSlash(mod)
	if !strings.HasPrefix(mod, "./") && !strings.HasPrefix(mod, "../") && !strings.HasPrefix(mod, "/") {
		mod = "./" + mod
	}
	return mod + "#" + f.ExportName, nil
}

func guardObj(g guard) *Object {
	o := NewObject()
	o.Set("path", g.path)
	if g.hasEquals {
		o.Set("equals", FromGo(g.equals))
	} else {
		o.Set("exists", g.exists)
	}
	return o
}

func (w *wf) plan() (Value, error) {
	out := NewObject()
	out.Set("name", w.name)
	out.Set("maxParallel", float64(w.maxParallel))
	arr := &Array{}
	for _, s := range w.steps {
		ref, err := w.ref(s)
		if err != nil {
			return nil, err
		}
		o := NewObject()
		o.Set("id", s.id)
		o.Set("ref", ref)
		if len(s.needs) > 0 {
			n := &Array{}
			for _, x := range s.needs {
				n.Elems = append(n.Elems, x)
			}
			o.Set("needs", n)
		}
		if len(s.when) > 0 {
			a := &Array{}
			for _, g := range s.when {
				a.Elems = append(a.Elems, guardObj(g))
			}
			o.Set("when", a)
		}
		if s.attempts > 1 || s.backoff > 0 {
			ro := NewObject()
			ro.Set("attempts", float64(s.attempts))
			ro.Set("backoffMs", float64(s.backoff.Milliseconds()))
			o.Set("retry", ro)
		}
		if s.timeout > 0 {
			o.Set("timeoutMs", float64(s.timeout.Milliseconds()))
		}
		switch s.loop {
		case "foreach":
			o.Set("foreach", FromGo(s.itemsGo))
			if s.parallel > 1 {
				o.Set("parallel", float64(s.parallel))
			}
		case "while", "until":
			o.Set(s.loop, guardObj(*s.loopGuard))
			o.Set("maxIterations", float64(s.maxIter))
		}
		if s.continueOnError {
			o.Set("continueOnError", true)
		}
		if len(s.outcomes) > 0 {
			o.Set("outcomes", FromGo(append([]string{}, s.outcomes...)))
		}
		arr.Elems = append(arr.Elems, o)
	}
	out.Set("steps", arr)
	return out, nil
}

// yamlGuardOK rejects guard paths WFNexus can never resolve: a run step's
// output is fixed at {ok, exitCode, stdout, stderr, outcome}, so a JS return
// field is invisible there. Use outcomes + "<id>.outcome" instead.
func (w *wf) yamlGuardOK(path, kind, stepID string) error {
	p := wfnPath(path)
	if p == "input" || strings.HasPrefix(p, "input.") {
		return nil
	}
	var head *wfStep
	for _, s := range w.steps {
		if (p == s.id || strings.HasPrefix(p, s.id+".")) && (head == nil || len(s.id) > len(head.id)) {
			head = s
		}
	}
	if head == nil {
		return wfErr(fmt.Sprintf("step %q: %s path %q names no step or input", stepID, kind, path))
	}
	field := strings.TrimPrefix(strings.TrimPrefix(p, head.id), ".")
	switch field {
	case "ok", "exitCode":
		return nil
	case "outcome":
		if len(head.outcomes) == 0 {
			return wfErr(fmt.Sprintf("step %q: %s reads %s.outcome but step %q declares no outcomes; add outcomes: [...] and return {outcome}", stepID, kind, head.id, head.id))
		}
		return nil
	}
	return wfErr(fmt.Sprintf("step %q: %s path %q cannot work in WFNexus: a run step's output is only {ok, exitCode, stdout, stderr, outcome}; declare outcomes on %q, return {outcome}, and guard on %s.outcome", stepID, kind, path, head.id, head.id))
}

// wfnPath translates a guard path to WFNexus form: "Steps.x.ok" -> "x.ok".
func wfnPath(p string) string {
	switch {
	case strings.HasPrefix(p, "Steps.") || strings.HasPrefix(p, "steps."):
		return p[len("steps."):]
	case strings.HasPrefix(p, "Input.") || p == "Input":
		return "input" + p[len("Input"):]
	}
	return p
}

var plainYAML = regexp.MustCompile(`^[A-Za-z_./][A-Za-z0-9_ ./#@-]*$`)

func yamlStr(s string) string {
	switch strings.ToLower(s) {
	case "true", "false", "null", "yes", "no", "on", "off", "y", "n", "~":
		return jsonStr(s)
	}
	if plainYAML.MatchString(s) && !strings.Contains(s, " #") && !strings.HasSuffix(s, " ") {
		return s
	}
	return jsonStr(s)
}

func jsonStr(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func ceilSec(d time.Duration) int64 { return int64(math.Ceil(d.Seconds())) }

func writeGuard(b *strings.Builder, ind string, g guard) {
	fmt.Fprintf(b, "%spath: %s\n", ind, yamlStr(wfnPath(g.path)))
	if g.hasEquals {
		fmt.Fprintf(b, "%sequals: %s\n", ind, jsonStr(g.equals))
	} else {
		fmt.Fprintf(b, "%sexists: %v\n", ind, g.exists)
	}
}

// toYAML emits WFNexus workflow YAML using only fields WFNexus accepts.
// upstream returns the steps whose outputs step i can read: every earlier
// step in sequential mode, otherwise the transitive closure of its needs.
func (w *wf) upstream(i int) []*wfStep {
	if !w.hasNeeds() {
		return w.steps[:i]
	}
	seen := map[int]bool{}
	var walk func(j int)
	walk = func(j int) {
		for _, n := range w.steps[j].needs {
			k := w.index[n]
			if !seen[k] {
				seen[k] = true
				walk(k)
			}
		}
	}
	walk(i)
	var out []*wfStep
	for k, s := range w.steps {
		if seen[k] {
			out = append(out, s)
		}
	}
	return out
}

// StepEnvName is the environment variable that carries a step's output into
// `run-step` when a plan runs in WFNexus: CODEMODE_STEP_<ID>, upper-cased,
// non-alphanumerics as "_".
func StepEnvName(id string) string {
	var b strings.Builder
	b.WriteString("CODEMODE_STEP_")
	for _, r := range strings.ToUpper(id) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func (w *wf) toYAML() (Value, error) {
	envNames := map[string]string{}
	for _, s := range w.steps {
		n := StepEnvName(s.id)
		if other, dup := envNames[n]; dup {
			return nil, wfErr(fmt.Sprintf("steps %q and %q map to the same variable %s; rename one", other, s.id, n))
		}
		envNames[n] = s.id
	}
	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\nsteps:\n", yamlStr(w.name))
	for si, s := range w.steps {
		ref, err := w.ref(s)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "  - id: %s\n", yamlStr(s.id))
		if len(s.needs) > 0 {
			ns := make([]string, len(s.needs))
			for i, n := range s.needs {
				ns[i] = yamlStr(n)
			}
			fmt.Fprintf(&b, "    needs: [%s]\n", strings.Join(ns, ", "))
		}
		for _, g := range s.when {
			if err := w.yamlGuardOK(g.path, "when", s.id); err != nil {
				return nil, err
			}
		}
		if s.loopGuard != nil {
			if err := w.yamlGuardOK(s.loopGuard.path, s.loop, s.id); err != nil {
				return nil, err
			}
		}
		if len(s.when) > 0 {
			b.WriteString("    when:\n")
			for _, g := range s.when {
				var gb strings.Builder
				writeGuard(&gb, "        ", g)
				b.WriteString("      - " + strings.TrimPrefix(gb.String(), "        "))
			}
		}
		switch s.loop {
		case "foreach":
			if len(s.itemsGo) == 0 {
				return nil, wfErr(fmt.Sprintf("step %q: WFNexus needs a non-empty foreach list", s.id))
			}
			fmt.Fprintf(&b, "    foreach: %s\n", jsonStr(s.itemsGo))
			if s.parallel > 1 {
				fmt.Fprintf(&b, "    parallel: %d\n", s.parallel)
			}
		case "while", "until":
			if p := wfnPath(s.loopGuard.path); p != s.id && !strings.HasPrefix(p, s.id+".") {
				return nil, wfErr(fmt.Sprintf("step %q: WFNexus %s loops must read their own output (path %s.<field>), got %q", s.id, s.loop, s.id, s.loopGuard.path))
			}
			// WFNexus types until/while as []Guard, like when.
			fmt.Fprintf(&b, "    %s:\n", s.loop)
			var gb strings.Builder
			writeGuard(&gb, "        ", *s.loopGuard)
			b.WriteString("      - " + strings.TrimPrefix(gb.String(), "        "))
			fmt.Fprintf(&b, "    max_iterations: %d\n", s.maxIter)
		}
		if s.attempts > 1 || s.backoff > 0 {
			fmt.Fprintf(&b, "    retry:\n      max_attempts: %d\n", s.attempts)
			if s.backoff > 0 {
				fmt.Fprintf(&b, "      backoff_sec: %d\n", ceilSec(s.backoff))
			}
		}
		if s.timeout > 0 {
			fmt.Fprintf(&b, "    timeout_sec: %d\n", ceilSec(s.timeout))
		}
		if s.continueOnError {
			b.WriteString("    continue-on-error: true\n")
		}
		// WFNexus hands a run step only its run: text. Plain value refs there
		// ({{ .Input }}, {{ .Steps.<id>.stdout }}, {{ .Item }}) are rewritten by
		// the engine into safe ${WFX_DATA_n} expansions, so pass ctx as inline
		// assignments; run-step rebuilds {input, steps, item, index}. (Step
		// env: values are literal and template functions are refused there.)
		assign := []string{`CODEMODE_INPUT="{{ .Input }}"`}
		var ids []string
		for _, u := range w.upstream(si) {
			path := ".Steps." + u.id + ".stdout"
			if u.loop != "" {
				path = ".Steps." + u.id + ".last.stdout"
			}
			assign = append(assign, fmt.Sprintf(`%s="{{ %s }}"`, StepEnvName(u.id), path))
			ids = append(ids, u.id+"="+StepEnvName(u.id))
		}
		if len(ids) > 0 {
			assign = append(assign, "CODEMODE_STEP_IDS="+strings.Join(ids, ","))
		}
		if s.loop == "foreach" {
			assign = append(assign, `CODEMODE_ITEM="{{ .Item }}"`, `CODEMODE_INDEX="{{ .Index }}"`)
		}
		runLine := strings.Join(assign, " ") + " " + fmt.Sprintf("%s code run-step %s", w.cfg.CLI, ref)
		if len(s.outcomes) > 0 {
			b.WriteString("    exit-codes:\n")
			for i, o := range s.outcomes {
				fmt.Fprintf(&b, "      %d: %s\n", OutcomeExitBase+i, yamlStr(o))
			}
			runLine += " --outcomes " + strings.Join(s.outcomes, ",")
		}
		fmt.Fprintf(&b, "    run: %s\n", yamlStr(runLine))
	}
	return b.String(), nil
}
