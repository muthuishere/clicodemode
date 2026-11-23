package js

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Limits bound a single run. Zero values pick the defaults below.
type Limits struct {
	MaxSteps       int64         // statements + calls + loop iterations (default 10,000,000)
	MaxCallDepth   int           // nested function calls (default 512)
	MaxConcurrency int           // host operations in flight at once (default 8)
	Timeout        time.Duration // wall clock for the whole run (default none; use ctx)
	MaxOps         int64         // host operation calls per run (default 0: unlimited)
	MaxStringBytes int           // longest string a program may build (default 64 MiB)
	MaxArrayLen    int           // longest array a program may build (default 10,000,000)
}

// ModuleLoader resolves a static import. from is the importing file ("" for
// the entry program), spec is the string in the import statement. It returns
// the module source and a canonical path used for caching and cycles.
type ModuleLoader func(from, spec string) (src, path string, err error)

// Config sets up an interpreter.
type Config struct {
	Limits Limits
	Stdout io.Writer
	Loader ModuleLoader
	// Shared, when set, makes this interpreter one run of a session: it uses
	// the session's interpreter lock, globals and persistent top-level scope.
	Shared *Shared
}

// Shared is the state a session keeps across runs: one interpreter lock (so
// JS from different runs never races), one set of globals and one top-level
// scope whose declarations carry over from run to run. Each run still gets
// its own Interp, so its context, limits, background tasks and unhandled
// rejections belong to that run only.
type Shared struct {
	gil     *fifoLock
	globals *Env
	scope   *Env
	once    sync.Once
}

// NewShared returns empty session state.
func NewShared() *Shared {
	sh := &Shared{gil: &fifoLock{}, globals: newEnv(nil)}
	sh.scope = newEnv(sh.globals)
	return sh
}

// Lock runs fn while holding the session's interpreter lock, so it can read
// or change the persistent scope safely.
func (sh *Shared) Lock(fn func()) {
	sh.gil.Lock()
	defer sh.gil.Unlock()
	fn()
}

// Binding is one persistent top-level name.
type Binding struct {
	Name  string
	Kind  string // let, const, var, function
	Value Value
}

// Bindings lists the persistent top-level names, sorted by name. Call it
// inside Lock.
func (sh *Shared) Bindings() []Binding {
	out := make([]Binding, 0, len(sh.scope.vars))
	for n, b := range sh.scope.vars {
		out = append(out, Binding{Name: n, Kind: b.kind, Value: b.v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Reset drops every persistent top-level binding. Call it inside Lock.
func (sh *Shared) Reset() {
	sh.scope = newEnv(sh.globals)
	declGen.Add(1)
}

// Interp runs programs. One Interp runs one program at a time.
//
// Concurrency model: every async function call and every promise callback
// runs on its own goroutine, but only the goroutine holding the interpreter
// lock executes JS. `await` and host operations release the lock, so host I/O
// overlaps (Promise.all really runs operations in parallel) while JS code
// itself never races. The lock is a FIFO queue (fifoLock) and an await
// continuation or .then job takes its place in that queue at the moment its
// promise settles (or at once when it is already settled), so jobs run in the
// order JavaScript specifies and the result does not depend on goroutine
// scheduling. Order between host operations settling on their own goroutines
// is by arrival.
type Interp struct {
	ctx     context.Context
	cancel  context.CancelFunc
	cfg     Config
	globals *Env
	gil     *fifoLock
	steps   atomic.Int64
	ops     atomic.Int64
	tasks   sync.WaitGroup
	sem     chan struct{}
	modules map[string]*module
	modMu   sync.Mutex

	fatalMu  sync.Mutex
	fatal    error // first limit/cancel error; ends the run, never catchable
	promMu   sync.Mutex
	lastErr  error
	promises []*Promise // every promise that can reject, for unhandled-rejection checks
}

// fail records a fatal error (budget, depth, cancellation) and stops the run.
func (it *Interp) fail(err error) error {
	it.fatalMu.Lock()
	if it.fatal == nil {
		it.fatal = err
	}
	it.fatalMu.Unlock()
	it.cancel()
	return err
}

func (it *Interp) fatalErr() error {
	it.fatalMu.Lock()
	defer it.fatalMu.Unlock()
	return it.fatal
}

// stopErr is the error to surface once the run context is done.
func (it *Interp) stopErr(at Node, file string) error {
	if f := it.fatalErr(); f != nil {
		return f
	}
	return &Error{Kind: "runtime", Pos: posOf(at), File: file, Msg: "cancelled: " + it.ctx.Err().Error()}
}

func (it *Interp) newPromise() *Promise {
	p := newPromise()
	it.promMu.Lock()
	it.promises = append(it.promises, p)
	it.promMu.Unlock()
	return p
}

// finish drains background work and turns fatal errors and unhandled
// rejections into the run's error.
func (it *Interp) finish(at Node, file string, err error) error {
	done := make(chan struct{})
	go func() { it.tasks.Wait(); close(done) }()
	select {
	case <-done:
	case <-it.ctx.Done():
	}
	if f := it.fatalErr(); f != nil {
		return f
	}
	if err != nil {
		return err
	}
	if it.ctx.Err() != nil {
		return it.stopErr(at, file)
	}
	it.promMu.Lock()
	defer it.promMu.Unlock()
	for _, p := range it.promises {
		p.mu.Lock()
		bad := p.state == 2 && !p.handled
		v := p.value
		p.mu.Unlock()
		if bad {
			msg := Inspect(v)
			if o, ok := v.(*Object); ok && isErrorObject(o) {
				msg = toString(o)
			}
			return &Error{Kind: "runtime", Pos: posOf(at), File: file, Msg: "unhandled promise rejection: " + msg + " (did you forget `await`?)"}
		}
	}
	return nil
}

type module struct {
	exports *Object
	loading bool
}

// New returns an interpreter bound to ctx. Cancel ctx to stop a run.
func New(ctx context.Context, cfg Config) *Interp {
	if cfg.Limits.MaxSteps == 0 {
		cfg.Limits.MaxSteps = 10_000_000
	}
	if cfg.Limits.MaxCallDepth == 0 {
		cfg.Limits.MaxCallDepth = 512
	}
	if cfg.Limits.MaxStringBytes == 0 {
		cfg.Limits.MaxStringBytes = 64 << 20
	}
	if cfg.Limits.MaxArrayLen == 0 {
		cfg.Limits.MaxArrayLen = 10_000_000
	}
	if cfg.Limits.MaxConcurrency == 0 {
		cfg.Limits.MaxConcurrency = 32 // operations are mostly I/O; hosts lower it for heavy ones
	}
	if cfg.Stdout == nil {
		cfg.Stdout = io.Discard
	}
	var cancel context.CancelFunc
	if cfg.Limits.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, cfg.Limits.Timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	it := &Interp{
		ctx: ctx, cancel: cancel, cfg: cfg,
		gil:     &fifoLock{},
		globals: newEnv(nil),
		sem:     make(chan struct{}, cfg.Limits.MaxConcurrency),
		modules: map[string]*module{},
	}
	if sh := cfg.Shared; sh != nil {
		it.gil, it.globals = sh.gil, sh.globals
		sh.once.Do(func() { installGlobals(it) })
		return it
	}
	installGlobals(it)
	return it
}

// Context returns the run context (cancelled on timeout or Close).
func (it *Interp) Context() context.Context { return it.ctx }

// Close cancels the run context.
func (it *Interp) Close() { it.cancel() }

// Define adds a global binding (read-only to the program).
func (it *Interp) Define(name string, v Value) {
	if it.cfg.Shared != nil { // globals are shared with runs still in flight
		it.gil.Lock()
		defer it.gil.Unlock()
	}
	it.globals.declare(name, v, "const")
}

// Acquire takes a host-concurrency slot; release it with the returned func.
func (it *Interp) Acquire() (func(), error) {
	select {
	case it.sem <- struct{}{}:
		return func() { <-it.sem }, nil
	case <-it.ctx.Done():
		return nil, it.ctx.Err()
	}
}

// ---------------- environments ----------------

type binding struct {
	v    Value
	kind string
}

// Env is a lexical scope.
type Env struct {
	vars   map[string]*binding
	parent *Env
	redecl map[string]bool // session scope: names a run may declare again
}

func newEnv(parent *Env) *Env { return &Env{parent: parent} } // vars allocated on first declare

// declGen changes whenever any binding is declared anywhere; identifier
// caches are only trusted while it is unchanged, so a later declaration that
// shadows a cached name is never missed.
var declGen atomic.Uint64

type identCache struct {
	env *Env
	b   *binding
	gen uint64
}

// resolve looks up an identifier, caching the binding on the AST node for
// repeated evaluation from the same scope (loops).
func resolve(n *Ident, env *Env) (*binding, bool) {
	g := declGen.Load()
	c := n.cache.Load()
	if c != nil && c.env == env && c.gen == g {
		return c.b, true
	}
	b, ok := env.lookup(n.Name)
	if ok && (c == nil || c.env == env) {
		n.cache.Store(&identCache{env: env, b: b, gen: g})
	}
	return b, ok
}

func (e *Env) declare(name string, v Value, kind string) {
	declGen.Add(1)
	if e.vars == nil {
		e.vars = make(map[string]*binding, 4)
	}
	e.vars[name] = &binding{v, kind}
}

func (e *Env) lookup(name string) (*binding, bool) {
	for s := e; s != nil; s = s.parent {
		if b, ok := s.vars[name]; ok {
			return b, true
		}
	}
	return nil, false
}

// ---------------- control flow ----------------

type ctrl int

const (
	cNormal ctrl = iota
	cBreak
	cContinue
	cReturn
)

// Throw is a JS exception propagating through Go code.
type Throw struct {
	Value Value
	Pos   Pos
	File  string
}

func (t *Throw) Error() string {
	f := t.File
	if f == "" {
		f = "<program>"
	}
	msg := Inspect(t.Value)
	if o, ok := t.Value.(*Object); ok && isErrorObject(o) {
		msg = toString(o)
	}
	return fmt.Sprintf("%s:%s: uncaught %s", f, t.Pos, msg)
}

// Runner is one goroutine's execution context.
type Runner struct {
	it    *Interp
	depth int
	file  string
	yield chan struct{} // non-nil until an async call first suspends
}

// Interp returns the interpreter this runner belongs to.
func (r *Runner) Interp() *Interp { return r.it }

func (it *Interp) typeError(msg string) error {
	return &Throw{Value: makeError("TypeError", msg)}
}

func (r *Runner) throwAt(n Node, name, f string, a ...any) error {
	var p Pos
	if n != nil {
		p = n.P()
	}
	return &Throw{Value: makeError(name, fmt.Sprintf(f, a...)), Pos: p, File: r.file}
}

// positioned fills in a missing source position on an error from a builtin.
func (r *Runner) positioned(n Node, err error) error {
	var t *Throw
	if errors.As(err, &t) && t.Pos == (Pos{}) {
		t.Pos = n.P()
		t.File = r.file
	}
	return err
}

// ErrBudget is returned when the step budget is exhausted.
var ErrBudget = errors.New("step budget exceeded")

// ErrSizeLimit is returned when a string or array outgrows its limit.
var ErrSizeLimit = errors.New("size limit exceeded")

// ErrOpLimit is returned when a run makes more host operation calls than
// Limits.MaxOps allows.
var ErrOpLimit = errors.New("operation call limit exceeded")

// sizeCheck fails the run (uncatchable, like the step budget) when v is a
// string or array over its limit.
func (r *Runner) sizeCheck(n Node, v Value) error {
	switch x := v.(type) {
	case string:
		if len(x) > r.it.cfg.Limits.MaxStringBytes {
			return r.it.fail(&Error{Kind: "runtime", Pos: posOf(n), File: r.file, Msg: fmt.Sprintf("%v: string of %d bytes (limit %d)", ErrSizeLimit, len(x), r.it.cfg.Limits.MaxStringBytes)})
		}
	case *Array:
		if len(x.Elems) > r.it.cfg.Limits.MaxArrayLen {
			return r.it.fail(&Error{Kind: "runtime", Pos: posOf(n), File: r.file, Msg: fmt.Sprintf("%v: array of %d elements (limit %d)", ErrSizeLimit, len(x.Elems), r.it.cfg.Limits.MaxArrayLen)})
		}
	}
	return nil
}

// CountOp records one host operation call and fails the run once
// Limits.MaxOps is exceeded. Hosts call it before starting an operation.
func (r *Runner) CountOp() error {
	max := r.it.cfg.Limits.MaxOps
	if n := r.it.ops.Add(1); max > 0 && n > max {
		return r.it.fail(&Error{Kind: "runtime", File: r.file, Msg: fmt.Sprintf("%v (limit %d)", ErrOpLimit, max)})
	}
	return nil
}

func (r *Runner) tick(n Node) error {
	n0 := r.it.steps.Add(1)
	if n0 > r.it.cfg.Limits.MaxSteps {
		return r.it.fail(&Error{Kind: "runtime", Pos: n.P(), File: r.file, Msg: fmt.Sprintf("%v (limit %d)", ErrBudget, r.it.cfg.Limits.MaxSteps)})
	}
	if n0&63 == 0 && r.it.ctx.Err() != nil {
		return r.it.stopErr(n, r.file)
	}
	return nil
}

// release gives up the interpreter lock (or hands it back to the caller that
// started this async call), reacquire takes it again.
func (r *Runner) release() {
	if r.yield != nil {
		close(r.yield)
		r.yield = nil
		return
	}
	r.it.gil.Unlock()
}

// Await waits for a value; non-promises return immediately.
func (r *Runner) Await(v Value, at Node) (Value, error) {
	p, ok := v.(*Promise)
	if !ok {
		// JS `await x` always yields once, even for plain values: queue the
		// continuation behind every job already waiting.
		c := make(chan struct{})
		r.it.gil.enqueue(c)
		r.release()
		<-c
		return v, nil
	}
	p.mu.Lock()
	p.handled = true
	p.mu.Unlock()
	c := make(chan struct{})
	var once sync.Once
	enq := func() { once.Do(func() { r.it.gil.enqueue(c) }) }
	p.onSettle(enq) // joins the job queue when p settles
	r.release()
	select {
	case <-c:
	case <-r.it.ctx.Done():
		enq()
		<-c
	}
	select {
	case <-p.done:
	default:
		return nil, r.it.stopErr(at, r.file)
	}
	if p.state == 2 {
		return nil, &Throw{Value: p.value, Pos: posOf(at), File: r.file}
	}
	return p.value, nil
}

func posOf(n Node) Pos {
	if n == nil {
		return Pos{}
	}
	return n.P()
}

// ---------------- running programs ----------------

// Run executes a parsed module and returns its result: the default export if
// there is one, otherwise the value of the last expression statement. A
// promise result is awaited. Run waits for all outstanding async work.
func (it *Interp) Run(prog *Program, file string) (Value, error) {
	it.gil.Lock()
	r := &Runner{it: it, file: file}
	res, exports, err := r.runModule(prog, file)
	if err == nil {
		if d, ok := exports.Get("default"); ok {
			res = d
		}
		res, err = r.Await(res, prog)
	}
	// Drain background tasks (then-callbacks, unawaited async calls).
	it.gil.Unlock()
	if err = it.finish(prog, file, err); err != nil {
		return nil, err
	}
	return res, nil
}

// RunTop runs a program in the session's persistent top-level scope (see
// Shared) and returns once the top level and its result have finished. Unlike
// Run it does not wait for background work (unawaited async calls, .then
// callbacks); call Wait for that. Without Shared it uses a fresh scope.
func (it *Interp) RunTop(prog *Program, file string) (Value, error) {
	it.gil.Lock()
	r := &Runner{it: it, file: file}
	var env *Env
	if sh := it.cfg.Shared; sh != nil {
		env = sh.scope
		env.redecl = make(map[string]bool, len(env.vars))
		for n := range env.vars { // REPL rule: a later run may declare a name again
			env.redecl[n] = true
		}
	}
	res, exports, err := r.runModuleIn(prog, file, env)
	if env != nil {
		env.redecl = nil
	}
	if err == nil {
		if d, ok := exports.Get("default"); ok {
			res = d
		}
		res, err = r.Await(res, prog)
	}
	it.gil.Unlock()
	if f := it.fatalErr(); f != nil {
		return nil, f
	}
	if err == nil && it.ctx.Err() != nil {
		err = it.stopErr(prog, file)
	}
	return res, err
}

// Wait waits for the background work of a RunTop and reports a fatal error
// or an unhandled rejection, as Run does at its end.
func (it *Interp) Wait(prog *Program, file string) error { return it.finish(prog, file, nil) }

// Locked runs fn holding the interpreter lock, without draining background
// work (unlike WithLock). Use it to read JS values safely.
func (it *Interp) Locked(fn func()) {
	it.gil.Lock()
	defer it.gil.Unlock()
	fn()
}

// Eval evaluates a single expression in the global scope.
func (it *Interp) Eval(x Node, file string) (Value, error) {
	prog := &Program{base: base{x.P()}, Body: []Node{&ExprStmt{base{x.P()}, x}}}
	return it.Run(prog, file)
}

func (r *Runner) runModule(prog *Program, file string) (Value, *Object, error) {
	return r.runModuleIn(prog, file, nil)
}

func (r *Runner) runModuleIn(prog *Program, file string, env *Env) (Value, *Object, error) {
	if env == nil {
		env = newEnv(r.it.globals)
	}
	exports := NewObject()
	// Static imports are resolved before the body runs.
	for _, s := range prog.Body {
		im, ok := s.(*Import)
		if !ok {
			continue
		}
		if err := r.bindImport(env, im, file); err != nil {
			return nil, nil, err
		}
	}
	r.hoist(prog.Body, env)
	var last Value = Undefined
	for _, s := range prog.Body {
		switch n := s.(type) {
		case *Import:
			continue
		case *Export:
			if err := r.doExport(n, env, exports); err != nil {
				return nil, nil, err
			}
			continue
		case *ExprStmt:
			if err := r.tick(n); err != nil {
				return nil, nil, err
			}
			v, err := r.eval(n.X, env)
			if err != nil {
				return nil, nil, err
			}
			last = v
			continue
		}
		c, _, _, err := r.exec(s, env)
		if err != nil {
			return nil, nil, err
		}
		if c == cBreak || c == cContinue {
			return nil, nil, &Error{Kind: "syntax", Pos: s.P(), File: file, Msg: "break/continue outside a loop"}
		}
	}
	return last, exports, nil
}

func (r *Runner) doExport(n *Export, env *Env, exports *Object) error {
	set := func(name string, v Value) {
		exports.Set(name, v)
		if f, ok := v.(*Function); ok && f.ExportModule == "" {
			f.ExportModule, f.ExportName = r.file, name
		}
	}
	switch {
	case n.Default != nil:
		v, err := r.eval(n.Default, env)
		if err != nil {
			return err
		}
		if f, ok := v.(*Function); ok && f.Name == "" {
			f.Name = "default"
		}
		set("default", v)
	case n.Decl != nil:
		if _, _, _, err := r.exec(n.Decl, env); err != nil {
			return err
		}
		if vd, ok := n.Decl.(*VarDecl); ok {
			for _, d := range vd.Decls {
				for _, name := range patternNames(d.Target) {
					b, _ := env.lookup(name)
					set(name, b.v)
				}
			}
		}
	default:
		for exported, local := range n.Names {
			b, ok := env.lookup(local)
			if !ok {
				return r.throwAt(n, "ReferenceError", "cannot export %s: not defined", local)
			}
			set(exported, b.v)
		}
	}
	return nil
}

func patternNames(n Node) []string {
	switch v := n.(type) {
	case *Ident:
		return []string{v.Name}
	case *ArrayPattern:
		var out []string
		for _, e := range v.Elems {
			if e.Target != nil {
				out = append(out, patternNames(e.Target)...)
			}
		}
		return out
	case *ObjectPattern:
		var out []string
		for _, p := range v.Props {
			out = append(out, patternNames(p.Target)...)
		}
		if v.Rest != nil {
			out = append(out, patternNames(v.Rest)...)
		}
		return out
	}
	return nil
}

func (r *Runner) bindImport(env *Env, im *Import, from string) error {
	if r.it.cfg.Loader == nil {
		return &Error{Kind: "runtime", Pos: im.P(), File: from, Msg: fmt.Sprintf("cannot import %q: this host does not allow imports", im.Source)}
	}
	src, path, err := r.it.cfg.Loader(from, im.Source)
	if err != nil {
		return &Error{Kind: "runtime", Pos: im.P(), File: from, Msg: fmt.Sprintf("cannot import %q: %v", im.Source, err)}
	}
	r.it.modMu.Lock()
	m, seen := r.it.modules[path]
	if seen && m.loading {
		r.it.modMu.Unlock()
		return &Error{Kind: "runtime", Pos: im.P(), File: from, Msg: fmt.Sprintf("import cycle through %q", path)}
	}
	if !seen {
		m = &module{loading: true}
		r.it.modules[path] = m
	}
	r.it.modMu.Unlock()
	if !seen {
		prog, err := Parse(src, path)
		if err != nil {
			return err
		}
		sub := &Runner{it: r.it, file: path, depth: r.depth}
		_, exports, err := sub.runModule(prog, path)
		if err != nil {
			return err
		}
		r.it.modMu.Lock()
		m.exports, m.loading = exports, false
		r.it.modMu.Unlock()
	}
	if im.Default != "" {
		v, ok := m.exports.Get("default")
		if !ok {
			return &Error{Kind: "runtime", Pos: im.P(), File: from, Msg: fmt.Sprintf("%q has no default export", im.Source)}
		}
		env.declare(im.Default, v, "const")
	}
	if im.Namespace != "" {
		ns := NewObject()
		for _, k := range m.exports.Keys() {
			v, _ := m.exports.Get(k)
			ns.Set(k, v)
		}
		ns.Frozen = true
		env.declare(im.Namespace, ns, "const")
	}
	for local, imported := range im.Names {
		v, ok := m.exports.Get(imported)
		if !ok {
			return &Error{Kind: "runtime", Pos: im.P(), File: from, Msg: fmt.Sprintf("%q does not export %q (exports: %s)", im.Source, imported, strings.Join(m.exports.Keys(), ", "))}
		}
		env.declare(local, v, "const")
	}
	return nil
}

// hoist pre-declares function declarations in a block.
func (r *Runner) hoist(body []Node, env *Env) {
	for _, s := range body {
		if ex, ok := s.(*Export); ok && ex.Decl != nil {
			s = ex.Decl
		}
		if vd, ok := s.(*VarDecl); ok && vd.Kind == "function" {
			d := vd.Decls[0]
			fn := d.Init.(*Func)
			env.declare(fn.Name, &Function{Decl: fn, Env: env, Name: fn.Name, File: r.file}, "let")
		}
	}
}

// ---------------- statements ----------------

func (r *Runner) execBlock(body []Node, env *Env) (ctrl, string, Value, error) {
	r.hoist(body, env)
	for _, s := range body {
		c, l, v, err := r.exec(s, env)
		if err != nil || c != cNormal {
			return c, l, v, err
		}
	}
	return cNormal, "", nil, nil
}

func (r *Runner) exec(s Node, env *Env) (ctrl, string, Value, error) {
	if err := r.tick(s); err != nil {
		return 0, "", nil, err
	}
	switch n := s.(type) {
	case *ExprStmt:
		_, err := r.eval(n.X, env)
		return cNormal, "", nil, err
	case *VarDecl:
		if n.Kind == "function" {
			return cNormal, "", nil, nil // hoisted
		}
		for _, d := range n.Decls {
			var v Value = Undefined
			if d.Init != nil {
				var err error
				if v, err = r.eval(d.Init, env); err != nil {
					return 0, "", nil, err
				}
				if f, ok := v.(*Function); ok && f.Name == "" {
					if id, ok := d.Target.(*Ident); ok {
						f.Name = id.Name
					}
				}
			}
			if err := r.bindPattern(d.Target, v, env, n.Kind); err != nil {
				return 0, "", nil, err
			}
		}
		return cNormal, "", nil, nil
	case *Block:
		return r.execBlock(n.Body, newEnv(env))
	case *If:
		t, err := r.eval(n.Test, env)
		if err != nil {
			return 0, "", nil, err
		}
		if truthy(t) {
			return r.exec(n.Then, env)
		} else if n.Else != nil {
			return r.exec(n.Else, env)
		}
		return cNormal, "", nil, nil
	case *While:
		for first := true; ; first = false {
			if !(n.Do && first) {
				t, err := r.eval(n.Test, env)
				if err != nil {
					return 0, "", nil, err
				}
				if !truthy(t) {
					return cNormal, "", nil, nil
				}
			}
			if err := r.tick(n); err != nil {
				return 0, "", nil, err
			}
			c, l, v, err := r.exec(n.Body, env)
			if err != nil {
				return 0, "", nil, err
			}
			if c == cBreak && l == "" {
				return cNormal, "", nil, nil
			}
			if c == cReturn || (c != cNormal && l != "") {
				return c, l, v, nil
			}
		}
	case *For:
		loopEnv := newEnv(env)
		if n.Init != nil {
			if _, _, _, err := r.exec(n.Init, loopEnv); err != nil {
				return 0, "", nil, err
			}
		}
		for {
			if n.Test != nil {
				t, err := r.eval(n.Test, loopEnv)
				if err != nil {
					return 0, "", nil, err
				}
				if !truthy(t) {
					return cNormal, "", nil, nil
				}
			}
			if err := r.tick(n); err != nil {
				return 0, "", nil, err
			}
			if !n.Closures {
				c, l, v, err := r.exec(n.Body, loopEnv)
				if err != nil {
					return 0, "", nil, err
				}
				if c == cBreak && l == "" {
					return cNormal, "", nil, nil
				}
				if c == cReturn || (c != cNormal && l != "") {
					return c, l, v, nil
				}
				if n.Update != nil {
					if _, err := r.eval(n.Update, loopEnv); err != nil {
						return 0, "", nil, err
					}
				}
				continue
			}
			// per-iteration copy of let bindings so closures capture this iteration
			iterEnv := newEnv(env)
			for k, b := range loopEnv.vars {
				iterEnv.declare(k, b.v, b.kind)
			}
			c, l, v, err := r.exec(n.Body, iterEnv)
			for k, b := range iterEnv.vars {
				if lb, ok := loopEnv.vars[k]; ok {
					lb.v = b.v
				}
			}
			if err != nil {
				return 0, "", nil, err
			}
			if c == cBreak && l == "" {
				return cNormal, "", nil, nil
			}
			if c == cReturn || (c != cNormal && l != "") {
				return c, l, v, nil
			}
			if n.Update != nil {
				if _, err := r.eval(n.Update, loopEnv); err != nil {
					return 0, "", nil, err
				}
			}
		}
	case *ForOf:
		coll, err := r.eval(n.Right, env)
		if err != nil {
			return 0, "", nil, err
		}
		var items []Value
		if n.In {
			switch c := coll.(type) {
			case *Object:
				for _, k := range c.Keys() {
					items = append(items, k)
				}
			case *Array:
				for i := range c.Elems {
					items = append(items, numberToString(float64(i)))
				}
			case nil, undefinedT:
			default:
				return 0, "", nil, r.throwAt(n.Right, "TypeError", "for-in needs an object")
			}
		} else {
			items, err = r.iterate(coll, n.Right)
			if err != nil {
				return 0, "", nil, err
			}
		}
		for _, item := range items {
			if err := r.tick(n); err != nil {
				return 0, "", nil, err
			}
			iterEnv := newEnv(env)
			kind := n.Kind
			if kind == "" {
				if err := r.assignTo(n.Left, item, env); err != nil {
					return 0, "", nil, err
				}
			} else if err := r.bindPattern(n.Left, item, iterEnv, kind); err != nil {
				return 0, "", nil, err
			}
			c, l, v, err := r.exec(n.Body, iterEnv)
			if err != nil {
				return 0, "", nil, err
			}
			if c == cBreak && l == "" {
				return cNormal, "", nil, nil
			}
			if c == cReturn || (c != cNormal && l != "") {
				return c, l, v, nil
			}
		}
		return cNormal, "", nil, nil
	case *Break:
		return cBreak, n.Label, nil, nil
	case *Continue:
		return cContinue, n.Label, nil, nil
	case *Return:
		var v Value = Undefined
		if n.X != nil {
			var err error
			if v, err = r.eval(n.X, env); err != nil {
				return 0, "", nil, err
			}
		}
		return cReturn, "", v, nil
	case *Try:
		return r.execTry(n, env)
	case *Empty:
		return cNormal, "", nil, nil
	case *Import, *Export:
		return 0, "", nil, &Error{Kind: "syntax", Pos: n.P(), File: r.file, Msg: "import/export only at module top level"}
	}
	if t, ok := s.(*ThrowStmt); ok {
		v, err := r.eval(t.X, env)
		if err != nil {
			return 0, "", nil, err
		}
		return 0, "", nil, &Throw{Value: v, Pos: t.P(), File: r.file}
	}
	return 0, "", nil, &Error{Kind: "runtime", Pos: s.P(), File: r.file, Msg: fmt.Sprintf("unsupported statement %T", s)}
}

func (r *Runner) execTry(n *Try, env *Env) (c ctrl, l string, v Value, err error) {
	c, l, v, err = r.execBlock(n.Body.Body, newEnv(env))
	if err != nil && n.Catch != nil {
		if r.it.ctx.Err() != nil {
			// Limits and cancellation can arrive as ordinary rejections (from
			// sleep, an operation or an async callee); they are still fatal.
			return 0, "", nil, r.it.stopErr(n, r.file)
		}
		if thrown, ok := catchable(err); ok {
			cenv := newEnv(env)
			if n.Param != nil {
				if berr := r.bindPattern(n.Param, thrown, cenv, "let"); berr != nil {
					return 0, "", nil, berr
				}
			}
			c, l, v, err = r.execBlock(n.Catch.Body, cenv)
		}
	}
	if n.Finally != nil {
		fc, fl, fv, ferr := r.execBlock(n.Finally.Body, newEnv(env))
		if ferr != nil {
			return 0, "", nil, ferr
		}
		if fc != cNormal {
			return fc, fl, fv, nil
		}
	}
	return c, l, v, err
}

// catchable converts an error into the JS value a catch clause receives.
// Budget exhaustion and cancellation are NOT catchable: a program must not be
// able to swallow its own limits.
func catchable(err error) (Value, bool) {
	var t *Throw
	if errors.As(err, &t) {
		return t.Value, true
	}
	var e *Error
	if errors.As(err, &e) {
		return nil, false
	}
	return makeError("Error", err.Error()), true
}

// iterate turns an iterable into a slice (arrays, strings, objects with
// Symbol-free iteration are not supported).
func (r *Runner) iterate(v Value, at Node) ([]Value, error) {
	switch c := v.(type) {
	case *Array:
		return append([]Value(nil), c.Elems...), nil
	case string:
		out := []Value{}
		for _, ch := range c {
			out = append(out, string(ch))
		}
		return out, nil
	}
	return nil, r.throwAt(at, "TypeError", "%s is not iterable", typeOf(v))
}

// ---------------- binding & assignment ----------------

func (r *Runner) bindPattern(target Node, v Value, env *Env, kind string) error {
	switch t := target.(type) {
	case *Ident:
		if b, ok := env.vars[t.Name]; ok && kind != "var" && b.kind != "var" && b.kind != "function" && !env.redecl[t.Name] {
			return r.throwAt(t, "SyntaxError", "identifier %q has already been declared", t.Name)
		}
		delete(env.redecl, t.Name) // only once per run
		env.declare(t.Name, v, kind)
		return nil
	case *ArrayPattern:
		items, err := r.iterate(v, t)
		if err != nil {
			return err
		}
		for i, e := range t.Elems {
			if e.Target == nil {
				continue
			}
			if e.Rest {
				rest := &Array{}
				if i < len(items) {
					rest.Elems = append(rest.Elems, items[i:]...)
				}
				if err := r.bindPattern(e.Target, rest, env, kind); err != nil {
					return err
				}
				break
			}
			var item Value = Undefined
			if i < len(items) {
				item = items[i]
			}
			if item == Undefined && e.Default != nil {
				if item, err = r.eval(e.Default, env); err != nil {
					return err
				}
			}
			if err := r.bindPattern(e.Target, item, env, kind); err != nil {
				return err
			}
		}
		return nil
	case *ObjectPattern:
		if v == nil || v == Undefined {
			return r.throwAt(t, "TypeError", "cannot destructure %s", toString(v))
		}
		used := map[string]bool{}
		for _, p := range t.Props {
			used[p.Key] = true
			item, err := r.getMember(v, p.Key, t)
			if err != nil {
				return err
			}
			if item == Undefined && p.Default != nil {
				if item, err = r.eval(p.Default, env); err != nil {
					return err
				}
			}
			if err := r.bindPattern(p.Target, item, env, kind); err != nil {
				return err
			}
		}
		if t.Rest != nil {
			rest := NewObject()
			if o, ok := v.(*Object); ok {
				for _, k := range o.Keys() {
					if !used[k] {
						rest.Set(k, o.vals[k])
					}
				}
			}
			return r.bindPattern(t.Rest, rest, env, kind)
		}
		return nil
	}
	return r.throwAt(target, "SyntaxError", "invalid binding target")
}

func (r *Runner) assignTo(target Node, v Value, env *Env) error {
	switch t := target.(type) {
	case *Ident:
		b, ok := resolve(t, env)
		if !ok {
			return r.throwAt(t, "ReferenceError", "%s is not defined", t.Name)
		}
		if b.kind == "const" {
			return r.throwAt(t, "TypeError", "assignment to constant variable %q", t.Name)
		}
		b.v = v
		return nil
	case *Member:
		obj, err := r.eval(t.Obj, env)
		if err != nil {
			return err
		}
		key, err := r.memberKey(t, env)
		if err != nil {
			return err
		}
		return r.setMember(obj, key, v, t)
	case *ArrayPattern, *ObjectPattern:
		// destructuring assignment to existing bindings
		tmp := newEnv(env)
		if err := r.bindPattern(t, v, tmp, "let"); err != nil {
			return err
		}
		for name, b := range tmp.vars {
			if err := r.assignTo(&Ident{base: base{target.P()}, Name: name}, b.v, env); err != nil {
				return err
			}
		}
		return nil
	}
	return r.throwAt(target, "SyntaxError", "invalid assignment target")
}

func (r *Runner) memberKey(m *Member, env *Env) (string, error) {
	if m.Computed == nil {
		return m.Name, nil
	}
	k, err := r.eval(m.Computed, env)
	if err != nil {
		return "", err
	}
	return toString(k), nil
}

func (r *Runner) setMember(obj Value, key string, v Value, at Node) error {
	switch o := obj.(type) {
	case *Object:
		if o.Frozen {
			return r.throwAt(at, "TypeError", "cannot assign to read-only property %q", key)
		}
		o.Set(key, v)
		return nil
	case *Array:
		if key == "length" {
			n := int(toNumber(v))
			if n < 0 || n > 1<<24 {
				return r.throwAt(at, "RangeError", "invalid array length")
			}
			for len(o.Elems) < n {
				o.Elems = append(o.Elems, Undefined)
			}
			o.Elems = o.Elems[:n]
			return nil
		}
		if isArrayIndex(key) {
			i := int(toNumber(key))
			if i > len(o.Elems)+1<<20 {
				return r.throwAt(at, "RangeError", "array index too large")
			}
			for len(o.Elems) <= i {
				o.Elems = append(o.Elems, Undefined)
			}
			o.Elems[i] = v
			return nil
		}
		return r.throwAt(at, "TypeError", "cannot set property %q on an array", key)
	case nil, undefinedT:
		return r.throwAt(at, "TypeError", "cannot set properties of %s (setting %q)", toString(obj), key)
	}
	return r.throwAt(at, "TypeError", "cannot set property %q on a %s", key, typeOf(obj))
}

// ---------------- expressions ----------------

func (r *Runner) eval(x Node, env *Env) (Value, error) {
	v, err := r.evalInner(x, env)
	if err == errShortCircuit {
		return Undefined, nil
	}
	return v, err
}

// evalInner evaluates without ending an optional chain; used for the object
// part of member/call expressions so a?.b.c short-circuits the whole chain.
func (r *Runner) evalInner(x Node, env *Env) (Value, error) {
	switch n := x.(type) {
	case *NumberLit:
		if n.boxed != nil {
			return n.boxed, nil
		}
		return n.V, nil
	case *StringLit:
		if n.boxed != nil {
			return n.boxed, nil
		}
		return n.V, nil
	case *BoolLit:
		return n.V, nil
	case *NullLit:
		return nil, nil
	case *UndefLit:
		return Undefined, nil
	case *TemplateLit:
		var b strings.Builder
		for i, q := range n.Quasis {
			b.WriteString(q)
			if i < len(n.Exprs) {
				v, err := r.eval(n.Exprs[i], env)
				if err != nil {
					return nil, err
				}
				b.WriteString(toString(v))
				if b.Len() > r.it.cfg.Limits.MaxStringBytes {
					return nil, r.sizeCheck(n, b.String())
				}
			}
		}
		return b.String(), nil
	case *Ident:
		b, ok := resolve(n, env)
		if !ok {
			return nil, r.throwAt(n, "ReferenceError", "%s is not defined", n.Name)
		}
		return b.v, nil
	case *ArrayLit:
		a := &Array{Elems: make([]Value, 0, len(n.Elems))}
		for _, e := range n.Elems {
			if e == nil {
				a.Elems = append(a.Elems, Undefined)
				continue
			}
			if sp, ok := e.(*Spread); ok {
				v, err := r.eval(sp.X, env)
				if err != nil {
					return nil, err
				}
				items, err := r.iterate(v, sp)
				if err != nil {
					return nil, err
				}
				a.Elems = append(a.Elems, items...)
				if err := r.sizeCheck(n, a); err != nil {
					return nil, err
				}
				continue
			}
			v, err := r.eval(e, env)
			if err != nil {
				return nil, err
			}
			a.Elems = append(a.Elems, v)
		}
		return a, nil
	case *ObjectLit:
		o := NewObject()
		for _, p := range n.Props {
			if p.Spread {
				v, err := r.eval(p.Value, env)
				if err != nil {
					return nil, err
				}
				switch s := v.(type) {
				case *Object:
					for _, k := range s.Keys() {
						o.Set(k, s.vals[k])
					}
				case *Array:
					for i, e := range s.Elems {
						o.Set(numberToString(float64(i)), e)
					}
				}
				continue
			}
			key := p.Key
			if p.Computed != nil {
				k, err := r.eval(p.Computed, env)
				if err != nil {
					return nil, err
				}
				key = toString(k)
			}
			if as, ok := p.Value.(*Assign); ok && as.Op == "=" {
				if _, isId := as.Target.(*Ident); isId && p.Computed == nil {
					if id := as.Target.(*Ident); id.Name == key {
						return nil, r.throwAt(as, "SyntaxError", "invalid shorthand property initializer")
					}
				}
			}
			v, err := r.eval(p.Value, env)
			if err != nil {
				return nil, err
			}
			if f, ok := v.(*Function); ok && f.Name == "" {
				f.Name = key
			}
			o.Set(key, v)
		}
		return o, nil
	case *Func:
		return &Function{Decl: n, Env: env, Name: n.Name, File: r.file}, nil
	case *Unary:
		if n.Op == "typeof" {
			if id, ok := n.X.(*Ident); ok {
				if _, found := env.lookup(id.Name); !found {
					return "undefined", nil
				}
			}
		}
		v, err := r.eval(n.X, env)
		if err != nil {
			return nil, err
		}
		switch n.Op {
		case "!":
			return !truthy(v), nil
		case "-":
			return -toNumber(v), nil
		case "+":
			return toNumber(v), nil
		case "~":
			return float64(^toInt32(v)), nil
		case "typeof":
			return typeOf(v), nil
		case "void":
			return Undefined, nil
		}
	case *Update:
		old, err := r.eval(n.X, env)
		if err != nil {
			return nil, err
		}
		o := toNumber(old)
		nf := o + 1
		if n.Op == "--" {
			nf = o - 1
		}
		nv := num(nf)
		if err := r.assignTo(n.X, nv, env); err != nil {
			return nil, err
		}
		if n.Prefix {
			return nv, nil
		}
		if _, isNum := old.(float64); isNum {
			return old, nil // already boxed: no new allocation
		}
		return num(o), nil
	case *Binary:
		l, err := r.eval(n.L, env)
		if err != nil {
			return nil, err
		}
		rv, err := r.eval(n.R, env)
		if err != nil {
			return nil, err
		}
		v, err := binaryOp(n.Op, l, rv)
		if err != nil {
			return nil, r.throwAt(n, "TypeError", "%s", err.Error())
		}
		if n.Op == "+" {
			if err := r.sizeCheck(n, v); err != nil {
				return nil, err
			}
		}
		return v, nil
	case *Logical:
		l, err := r.eval(n.L, env)
		if err != nil {
			return nil, err
		}
		switch n.Op {
		case "&&":
			if !truthy(l) {
				return l, nil
			}
		case "||":
			if truthy(l) {
				return l, nil
			}
		case "??":
			if l != nil && l != Undefined {
				return l, nil
			}
		}
		return r.eval(n.R, env)
	case *Cond:
		t, err := r.eval(n.Test, env)
		if err != nil {
			return nil, err
		}
		if truthy(t) {
			return r.eval(n.Then, env)
		}
		return r.eval(n.Else, env)
	case *Assign:
		return r.evalAssign(n, env)
	case *Member:
		obj, err := r.evalInner(n.Obj, env)
		if err != nil {
			return nil, err
		}
		if n.Optional && (obj == nil || obj == Undefined) {
			return Undefined, errShortCircuit
		}
		key, err := r.memberKey(n, env)
		if err != nil {
			return nil, err
		}
		return r.getMember(obj, key, n)
	case *Call:
		return r.evalCallInner(n, env)
	case *Await:
		v, err := r.eval(n.X, env)
		if err != nil {
			return nil, err
		}
		return r.Await(v, n)
	case *Seq:
		var v Value = Undefined
		for _, e := range n.List {
			var err error
			if v, err = r.eval(e, env); err != nil {
				return nil, err
			}
		}
		return v, nil
	case *Spread:
		return nil, r.throwAt(n, "SyntaxError", "unexpected spread")
	}
	return nil, &Error{Kind: "runtime", Pos: x.P(), File: r.file, Msg: fmt.Sprintf("unsupported expression %T", x)}
}

// errShortCircuit propagates an optional-chain short-circuit to the end of
// the chain; evalChain converts it to undefined.
var errShortCircuit = errors.New("optional chain short-circuit")

func (r *Runner) evalAssign(n *Assign, env *Env) (Value, error) {
	if n.Op == "=" {
		v, err := r.eval(n.Value, env)
		if err != nil {
			return nil, err
		}
		if f, ok := v.(*Function); ok && f.Name == "" {
			if id, ok := n.Target.(*Ident); ok {
				f.Name = id.Name
			}
		}
		return v, r.assignTo(n.Target, v, env)
	}
	cur, err := r.eval(n.Target, env)
	if err != nil {
		return nil, err
	}
	var v Value
	switch n.Op {
	case "&&=":
		if !truthy(cur) {
			return cur, nil
		}
		v, err = r.eval(n.Value, env)
	case "||=":
		if truthy(cur) {
			return cur, nil
		}
		v, err = r.eval(n.Value, env)
	case "??=":
		if cur != nil && cur != Undefined {
			return cur, nil
		}
		v, err = r.eval(n.Value, env)
	default:
		var rhs Value
		if rhs, err = r.eval(n.Value, env); err == nil {
			v, err = binaryOp(strings.TrimSuffix(n.Op, "="), cur, rhs)
			if err != nil {
				return nil, r.throwAt(n, "TypeError", "%s", err.Error())
			}
			if err := r.sizeCheck(n, v); err != nil {
				return nil, err
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return v, r.assignTo(n.Target, v, env)
}

func binaryOp(op string, l, r Value) (Value, error) {
	switch op {
	case "+":
		_, ls := l.(string)
		_, rs := r.(string)
		if ls || rs || !isPrimitive(l) || !isPrimitive(r) {
			return concatStr(toString(l), toString(r)), nil
		}
		return num(toNumber(l) + toNumber(r)), nil
	case "-":
		return num(toNumber(l) - toNumber(r)), nil
	case "*":
		return num(toNumber(l) * toNumber(r)), nil
	case "/":
		return num(toNumber(l) / toNumber(r)), nil
	case "%":
		a, b := toNumber(l), toNumber(r)
		if a >= -(1<<53) && a <= 1<<53 && b >= -(1<<53) && b <= 1<<53 && b != 0 &&
			a == math.Trunc(a) && b == math.Trunc(b) {
			m := float64(int64(a) % int64(b)) // Go and JS both take the dividend's sign
			if m == 0 && (a < 0 || (a == 0 && math.Signbit(a))) {
				return math.Copysign(0, -1), nil // JS: -4 % 2 === -0
			}
			return num(m), nil
		}
		return num(math.Mod(a, b)), nil
	case "**":
		return math.Pow(toNumber(l), toNumber(r)), nil
	case "==":
		return looseEquals(l, r), nil
	case "!=":
		return !looseEquals(l, r), nil
	case "===":
		return strictEquals(l, r), nil
	case "!==":
		return !strictEquals(l, r), nil
	case "<", ">", "<=", ">=":
		ls, lok := l.(string)
		rs, rok := r.(string)
		if lok && rok {
			switch op {
			case "<":
				return ls < rs, nil
			case ">":
				return ls > rs, nil
			case "<=":
				return ls <= rs, nil
			default:
				return ls >= rs, nil
			}
		}
		a, b := toNumber(l), toNumber(r)
		switch op {
		case "<":
			return a < b, nil
		case ">":
			return a > b, nil
		case "<=":
			return a <= b, nil
		default:
			return a >= b, nil
		}
	case "&":
		return float64(toInt32(l) & toInt32(r)), nil
	case "|":
		return float64(toInt32(l) | toInt32(r)), nil
	case "^":
		return float64(toInt32(l) ^ toInt32(r)), nil
	case "<<":
		return float64(toInt32(l) << (toUint32(r) & 31)), nil
	case ">>":
		return float64(toInt32(l) >> (toUint32(r) & 31)), nil
	case ">>>":
		return float64(toUint32(l) >> (toUint32(r) & 31)), nil
	case "in":
		switch o := r.(type) {
		case *Object:
			_, ok := o.Get(toString(l))
			return ok, nil
		case *Array:
			k := toString(l)
			if k == "length" {
				return true, nil
			}
			if isArrayIndex(k) {
				return int(toNumber(k)) < len(o.Elems), nil
			}
			return false, nil
		}
		return nil, fmt.Errorf("cannot use 'in' operator on %s", typeOf(r))
	}
	return nil, fmt.Errorf("unknown operator %s", op)
}

func (r *Runner) evalCallInner(n *Call, env *Env) (Value, error) {
	if err := r.tick(n); err != nil {
		return nil, err
	}
	var fn, this Value = nil, Undefined
	if m, ok := n.Callee.(*Member); ok {
		obj, err := r.evalInner(m.Obj, env)
		if err != nil {
			return nil, err
		}
		if m.Optional && (obj == nil || obj == Undefined) {
			return nil, errShortCircuit
		}
		key, err := r.memberKey(m, env)
		if err != nil {
			return nil, err
		}
		if fn, err = r.getMember(obj, key, m); err != nil {
			return nil, err
		}
		this = obj
		if fn == Undefined && !n.Optional {
			return nil, r.throwAt(m, "TypeError", "%s is not a function", describeCallee(m))
		}
	} else {
		var err error
		if fn, err = r.evalInner(n.Callee, env); err != nil {
			return nil, err
		}
	}
	if n.Optional && (fn == nil || fn == Undefined) {
		return nil, errShortCircuit
	}
	args := make([]Value, 0, len(n.Args))
	for _, a := range n.Args {
		if sp, ok := a.(*Spread); ok {
			v, err := r.eval(sp.X, env)
			if err != nil {
				return nil, err
			}
			items, err := r.iterate(v, sp)
			if err != nil {
				return nil, err
			}
			args = append(args, items...)
			continue
		}
		v, err := r.eval(a, env)
		if err != nil {
			return nil, err
		}
		args = append(args, v)
	}
	switch fn.(type) {
	case *Function, *Builtin:
	default:
		return nil, r.throwAt(n, "TypeError", "%s is not a function", describeCallee(n.Callee))
	}
	res, err := r.CallWithThis(fn, this, args)
	if err != nil {
		return nil, r.positioned(n, err)
	}
	return res, nil
}

func describeCallee(n Node) string {
	switch c := n.(type) {
	case *Ident:
		return c.Name
	case *Member:
		if c.Computed == nil {
			return describeCallee(c.Obj) + "." + c.Name
		}
		return describeCallee(c.Obj) + "[...]"
	}
	return "expression"
}

// Call invokes a JS or builtin function with no `this`.
func (r *Runner) Call(fn Value, args ...Value) (Value, error) {
	return r.CallWithThis(fn, Undefined, args)
}

// CallWithThis invokes fn. For async JS functions it returns a Promise.
func (r *Runner) CallWithThis(fn, this Value, args []Value) (Value, error) {
	switch f := fn.(type) {
	case *Builtin:
		v, err := f.Fn(r, this, args)
		if err != nil {
			return v, err
		}
		// Builtins that grow a string or array (join, repeat, concat, push,
		// ...) are held to the same size limits as + and spread.
		if err := r.sizeCheck(nil, v); err != nil {
			return nil, err
		}
		if a, ok := this.(*Array); ok {
			if err := r.sizeCheck(nil, a); err != nil {
				return nil, err
			}
		}
		return v, nil
	case *Function:
		if r.depth+1 > r.it.cfg.Limits.MaxCallDepth {
			return nil, &Error{Kind: "runtime", Pos: f.Decl.P(), File: r.file, Msg: fmt.Sprintf("maximum call depth %d exceeded", r.it.cfg.Limits.MaxCallDepth)}
		}
		if f.Decl.Async {
			return r.startAsync(f, args), nil
		}
		// Sync calls reuse this goroutine's runner (no allocation per call).
		depth, file := r.depth, r.file
		r.depth, r.file = depth+1, f.File
		v, err := r.callBody(f, args)
		r.depth, r.file = depth, file
		return v, err
	}
	return nil, r.it.typeError(fmt.Sprintf("%s is not a function", typeOf(fn)))
}

func (r *Runner) callBody(f *Function, args []Value) (Value, error) {
	env := newEnv(f.Env)
	for i, p := range f.Decl.Params {
		if p.Rest {
			rest := &Array{}
			if i < len(args) {
				rest.Elems = append(rest.Elems, args[i:]...)
			}
			if err := r.bindPattern(p.Target, rest, env, "let"); err != nil {
				return nil, err
			}
			break
		}
		var v Value = Undefined
		if i < len(args) {
			v = args[i]
		}
		if v == Undefined && p.Default != nil {
			var err error
			if v, err = r.eval(p.Default, env); err != nil {
				return nil, err
			}
		}
		if err := r.bindPattern(p.Target, v, env, "let"); err != nil {
			return nil, err
		}
	}
	c, _, v, err := r.execBlock(f.Decl.Body, env)
	if err != nil {
		return nil, err
	}
	if c == cReturn {
		return v, nil
	}
	return Undefined, nil
}

// startAsync runs an async function on its own goroutine. The caller blocks
// until the callee first suspends or finishes, matching JS's "runs
// synchronously until the first await".
func (r *Runner) startAsync(f *Function, args []Value) *Promise {
	p := r.it.newPromise()
	yield := make(chan struct{})
	r.it.tasks.Add(1)
	go func() {
		defer r.it.tasks.Done()
		child := &Runner{it: r.it, depth: r.depth + 1, file: f.File, yield: yield}
		v, err := child.callBody(f, args)
		if err != nil {
			if t, ok := catchable(err); ok {
				p.settle(2, t)
			} else {
				p.settle(2, makeError("Error", err.Error()))
				r.it.fail(err) // a budget/cancel error stops the whole run
			}
		} else {
			r.it.resolveWith(p, v)
		}
		child.release()
	}()
	<-yield
	return p
}

// resolveWith settles p with v, adopting v's state if v is a promise.
func (it *Interp) resolveWith(p *Promise, v Value) {
	inner, ok := v.(*Promise)
	if !ok {
		p.settle(1, v)
		return
	}
	if inner == p {
		p.settle(2, makeError("TypeError", "promise resolved with itself"))
		return
	}
	// Adopt inner's outcome when it settles. Anything waiting on p (await,
	// .then, Promise.all) also watches the run context, so a cancelled run
	// needs no extra goroutine here.
	it.tasks.Add(1)
	inner.onSettle(func() {
		defer it.tasks.Done()
		p.settle(inner.state, inner.value)
	})
}

// HostPromise runs fn on a goroutine (holding a concurrency slot, NOT the
// interpreter lock) and returns a promise for its result. fn must not touch
// JS values other than its own inputs and outputs.
func (it *Interp) HostPromise(fn func(ctx context.Context) (Value, error)) *Promise {
	p := it.newPromise()
	it.tasks.Add(1)
	go func() {
		defer it.tasks.Done()
		release, err := it.Acquire()
		if err != nil {
			p.settle(2, makeError("Error", "cancelled: "+err.Error()))
			return
		}
		v, err := func() (v Value, err error) {
			defer func() {
				if rec := recover(); rec != nil {
					err = fmt.Errorf("panic: %v", rec)
				}
			}()
			return fn(it.ctx)
		}()
		release()
		if err != nil {
			var t *Throw
			if errors.As(err, &t) {
				p.settle(2, t.Value)
			} else {
				p.settle(2, makeError("Error", err.Error()))
			}
			return
		}
		it.resolveWith(p, v)
	}()
	return p
}

// then schedules cb to run (under the interpreter lock) once p settles.
func (it *Interp) then(p *Promise, onFul, onRej Value, depth int, file string) *Promise {
	out := it.newPromise()
	p.mu.Lock()
	p.handled = true
	p.mu.Unlock()
	it.tasks.Add(1)
	c := make(chan struct{})
	var once sync.Once
	enq := func() { once.Do(func() { it.gil.enqueue(c) }) }
	p.onSettle(enq) // the job takes its place in the FIFO queue now
	go func() {
		defer it.tasks.Done()
		select {
		case <-c:
		case <-it.ctx.Done():
			enq()
			<-c
		}
		// The job settles its result promise while it still owns the lock, so
		// whatever that promise releases queues behind the jobs already
		// waiting (FIFO), not behind whichever goroutine wakes first.
		defer it.gil.Unlock()
		if it.ctx.Err() != nil { // never run .catch/.then handlers after a fatal stop
			out.settle(2, makeError("Error", "cancelled"))
			return
		}
		cb := onFul
		if p.state == 2 {
			cb = onRej
		}
		switch cb.(type) {
		case *Function, *Builtin:
		default:
			out.settle(p.state, p.value) // pass through
			return
		}
		r := &Runner{it: it, depth: depth, file: file}
		v, err := r.Call(cb, p.value)
		if err != nil {
			if t, ok := catchable(err); ok {
				out.settle(2, t)
			} else {
				out.settle(2, makeError("Error", err.Error()))
				it.fail(err)
			}
			return
		}
		it.resolveWith(out, v)
	}()
	return out
}

// ThrowStmt is the statement form of throw (Throw is the Go error type).
type ThrowStmt struct {
	base
	X Node
}

// fifoLock is the interpreter lock: a mutex that grants itself to waiters in
// the order they joined the queue. enqueue lets a caller join the queue
// before it starts waiting, which is how a promise reaction claims its place
// at the moment the promise settles.
type fifoLock struct {
	mu   sync.Mutex
	held bool
	q    []chan struct{}
}

// enqueue adds c to the queue; c is closed when the caller owns the lock.
func (l *fifoLock) enqueue(c chan struct{}) {
	l.mu.Lock()
	if !l.held && len(l.q) == 0 {
		l.held = true
		close(c)
	} else {
		l.q = append(l.q, c)
	}
	l.mu.Unlock()
}

func (l *fifoLock) Lock() {
	c := make(chan struct{})
	l.enqueue(c)
	<-c
}

func (l *fifoLock) Unlock() {
	l.mu.Lock()
	if len(l.q) > 0 {
		c := l.q[0]
		l.q[0] = nil
		l.q = l.q[1:]
		close(c) // ownership passes straight to the next waiter
	} else {
		l.held = false
	}
	l.mu.Unlock()
}
