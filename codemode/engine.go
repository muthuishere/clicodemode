package codemode

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/muthuishere/clicodemode/js"
)

// Policy decides what a program may do. The zero value allows every
// registered operation and denies imports outside the program's directory.
//
// Security note: the interpreter bounds steps, time, call depth and
// concurrency, and exposes only registered operations. It is NOT a sandbox
// for hostile code: operations run with the host process's privileges, so
// the host must only register what it is willing to let a program do.
type Policy struct {
	// Allow is consulted before every operation call. Return an error to deny.
	Allow func(ctx context.Context, op *Operation) error
	// Limits bound each run.
	Limits js.Limits
	// ModuleRoot confines imports; default is the entry program's directory.
	ModuleRoot string
	// Exec controls external processes (exec, execStream, shell). Zero = none.
	Exec ExecPolicy
}

// Engine runs programs against a registry.
type Engine struct {
	Registry *Registry
	Policy   Policy
	Stdout   io.Writer // print/console.log output (default os.Stderr)
	Cache    *Cache    // compiled-plan cache for check/compile (nil = off)
	// WorkflowCLI is the executable toYAML names in `run: <cli> code run-step`
	// (default: the running binary's base name).
	WorkflowCLI string
}

// NewEngine returns an engine with default policy.
func NewEngine(reg *Registry) *Engine { return &Engine{Registry: reg} }

// Diagnostic is one problem found by check, compile or run.
type Diagnostic struct {
	Severity string `json:"severity"` // error | warning
	File     string `json:"file"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Message  string `json:"message"`
	Hint     string `json:"hint,omitempty"`
	// Kind is "syntax" or "runtime" for engine errors, or the thrown
	// error's name (TypeError, ...) for an uncaught throw.
	Kind string `json:"kind,omitempty"`
	// Snippet is the source line with a caret under the column (set by
	// Session runs).
	Snippet string `json:"snippet,omitempty"`
}

func (d Diagnostic) String() string {
	s := fmt.Sprintf("%s:%d:%d: %s: %s", d.File, d.Line, d.Col, d.Severity, d.Message)
	if d.Hint != "" {
		s += " (" + d.Hint + ")"
	}
	return s
}

// Program is a checked program ready to run.
type Program struct {
	File       string   `json:"file"`
	Hash       string   `json:"hash"`       // sha256 of entry + imported sources
	Imports    []string `json:"imports"`    // resolved module paths
	Operations []string `json:"operations"` // statically referenced operations
	CacheKey   string   `json:"cache_key"`  // hash + compiler + capability fingerprint
	ast        *js.Program
	sources    map[string]string
}

// CompilerVersion feeds the compiled-program cache key.
const CompilerVersion = "codemode-js/0.1.0"

// Compile parses the program and every static import, checks operation
// references, and returns the program plus diagnostics. A program with any
// error diagnostic must not be run.
func (e *Engine) Compile(src, file string) (*Program, []Diagnostic) {
	p := &Program{File: file, sources: map[string]string{file: src}}
	ast, err := js.Parse(src, file)
	if err != nil {
		return nil, []Diagnostic{fromErr(err, file)}
	}
	p.ast = ast
	var diags []Diagnostic
	opsUsed := map[string]bool{}
	// Walk the entry module and its imports.
	visited := map[string]bool{file: true}
	queue := []struct {
		file string
		ast  *js.Program
	}{{file, ast}}
	loader := e.loader(file)
	for len(queue) > 0 {
		m := queue[0]
		queue = queue[1:]
		diags = append(diags, e.checkOps(m.ast, m.file, opsUsed)...)
		for _, s := range m.ast.Body {
			im, ok := s.(*js.Import)
			if !ok {
				continue
			}
			msrc, mpath, err := loader(m.file, im.Source)
			if err != nil {
				diags = append(diags, Diagnostic{Severity: "error", File: m.file, Line: im.P().Line, Col: im.P().Col, Message: fmt.Sprintf("cannot import %q: %v", im.Source, err)})
				continue
			}
			if visited[mpath] {
				continue
			}
			visited[mpath] = true
			p.Imports = append(p.Imports, mpath)
			p.sources[mpath] = msrc
			sub, err := js.Parse(msrc, mpath)
			if err != nil {
				diags = append(diags, fromErr(err, mpath))
				continue
			}
			queue = append(queue, struct {
				file string
				ast  *js.Program
			}{mpath, sub})
		}
	}
	for op := range opsUsed {
		p.Operations = append(p.Operations, op)
	}
	sort.Strings(p.Operations)
	sort.Strings(p.Imports)
	h := sha256.New()
	keys := make([]string, 0, len(p.sources))
	for k := range p.sources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "%s\x00%s\x00", k, p.sources[k])
	}
	p.Hash = "sha256:" + hex.EncodeToString(h.Sum(nil))[:32]
	ck := sha256.Sum256([]byte(p.Hash + "|" + CompilerVersion + "|" + e.Registry.Capabilities().Fingerprint))
	p.CacheKey = "sha256:" + hex.EncodeToString(ck[:])[:32]
	return p, diags
}

func fromErr(err error, file string) Diagnostic {
	var je *js.Error
	if errors.As(err, &je) {
		f := je.File
		if f == "" {
			f = file
		}
		return Diagnostic{Severity: "error", File: f, Line: je.Pos.Line, Col: je.Pos.Col, Message: je.Kind + " error: " + je.Msg, Kind: je.Kind}
	}
	var th *js.Throw
	if errors.As(err, &th) {
		f := th.File
		if f == "" {
			f = file
		}
		msg := strings.TrimPrefix(err.Error(), fmt.Sprintf("%s:%s: ", f, th.Pos))
		kind := "throw"
		if o, ok := th.Value.(*js.Object); ok {
			if n, ok := o.Get("name"); ok {
				if s, ok := n.(string); ok && s != "" {
					kind = s
				}
			}
		}
		return Diagnostic{Severity: "error", File: f, Line: th.Pos.Line, Col: th.Pos.Col, Message: msg, Kind: kind}
	}
	return Diagnostic{Severity: "error", File: file, Message: err.Error()}
}

// checkOps finds call("literal", ...) and ops.a.b(...) references and checks
// they name registered operations.
func (e *Engine) checkOps(prog *js.Program, file string, used map[string]bool) []Diagnostic {
	var diags []Diagnostic
	check := func(name string, at js.Pos) {
		used[name] = true
		if _, ok := e.Registry.Lookup(name); ok {
			return
		}
		d := Diagnostic{Severity: "error", File: file, Line: at.Line, Col: at.Col, Message: fmt.Sprintf("unknown operation %q", name)}
		if s := e.suggest(name); s != "" {
			d.Hint = fmt.Sprintf("did you mean %q?", s)
		} else {
			d.Hint = "run `code operations` to list them"
		}
		diags = append(diags, d)
	}
	js.Walk(prog, func(n js.Node) {
		c, ok := n.(*js.Call)
		if !ok {
			return
		}
		if id, ok := c.Callee.(*js.Ident); ok && id.Name == "call" && len(c.Args) > 0 {
			if s, ok := c.Args[0].(*js.StringLit); ok {
				check(s.V, s.P())
				e.checkArgs(s.V, &js.Call{Callee: c.Callee, Args: c.Args[1:]}, file, &diags)
			}
			return
		}
		if id, ok := c.Callee.(*js.Ident); ok && (id.Name == "exec" || id.Name == "execStream") && len(c.Args) > 0 {
			if arr, ok := c.Args[0].(*js.ArrayLit); ok && len(arr.Elems) > 0 {
				if s, ok := arr.Elems[0].(*js.StringLit); ok {
					if err := e.Policy.Exec.check(context.Background(), []string{s.V}); err != nil && e.Policy.Exec.Allow == nil {
						diags = append(diags, Diagnostic{Severity: "error", File: file, Line: s.P().Line, Col: s.P().Col, Message: err.Error()})
					}
				}
			} else if _, isStr := c.Args[0].(*js.StringLit); isStr {
				diags = append(diags, Diagnostic{Severity: "error", File: file, Line: c.Args[0].P().Line, Col: c.Args[0].P().Col, Message: id.Name + " takes an argv array, not a command string", Hint: `e.g. exec(["git", "status"])`})
			}
			return
		}
		if path, ok := opsPath(c.Callee); ok {
			check(path, c.Callee.P())
			e.checkArgs(path, c, file, &diags)
		}
	})
	// ops.ctx.change-plan(...) parses as ops.ctx.change - plan(...): the call
	// never happens and check would see no operation at all.
	js.Walk(prog, func(n js.Node) {
		b, ok := n.(*js.Binary)
		if !ok || b.Op != "-" {
			return
		}
		l := b.L
		if a, isAwait := l.(*js.Await); isAwait {
			l = a.X // await ops.a.b-c(...) parses as (await ops.a.b) - c(...)
		}
		path, ok := opsPath(l)
		if !ok {
			return
		}
		rest := ""
		switch r := b.R.(type) {
		case *js.Ident:
			rest = r.Name
		case *js.Call:
			if id, ok := r.Callee.(*js.Ident); ok {
				rest = id.Name
			}
		}
		if rest == "" {
			return
		}
		name := path + "-" + rest
		i := strings.LastIndex(name, ".")
		quoted := name[:max(i, 0)]
		d := Diagnostic{Severity: "error", File: file, Line: l.P().Line, Col: l.P().Col,
			Message: fmt.Sprintf("ops.%s parses as a subtraction (ops.%s - %s), not an operation call", name, path, rest)}
		if i >= 0 {
			d.Hint = fmt.Sprintf("write ops.%s[%q](...) or call(%q, ...)", quoted, name[i+1:], name)
		} else {
			d.Hint = fmt.Sprintf("write ops[%q](...) or call(%q, ...)", name, name)
		}
		if _, ok := e.Registry.Lookup(name); !ok {
			d.Hint += "; no operation named " + strconv.Quote(name) + " is registered either"
		}
		diags = append(diags, d)
	})
	return diags
}

// checkArgs validates a literal argument of a call to a registered operation
// against its input schema, so check catches what run would reject. Parts
// that are not literals (variables, calls, spreads) are skipped.
func (e *Engine) checkArgs(name string, c *js.Call, file string, diags *[]Diagnostic) {
	op, ok := e.Registry.Lookup(name)
	if !ok || len(c.Args) == 0 {
		return
	}
	arg := c.Args[0]
	v := literalValue(arg)
	if _, unknown := v.(unknownArg); unknown {
		return
	}
	at := arg.P()
	if _, isObj := v.(map[string]any); !isObj {
		if v == nil {
			return // ops.x(null) / undefined: treated as no arguments
		}
		*diags = append(*diags, Diagnostic{Severity: "error", File: file, Line: at.Line, Col: at.Col,
			Message: fmt.Sprintf("%s: arguments must be an object, got %s", name, jsonType(v)), Hint: "pass {key: value}"})
		return
	}
	if len(op.InputSchema) == 0 {
		return
	}
	var s map[string]any
	if json.Unmarshal(op.InputSchema, &s) != nil {
		return
	}
	var errs []string
	validateValue(s, v, "", &errs)
	for _, msg := range errs {
		*diags = append(*diags, Diagnostic{Severity: "error", File: file, Line: at.Line, Col: at.Col,
			Message: fmt.Sprintf("%s: %s", name, msg), Hint: fmt.Sprintf("see `code describe %s`", name)})
	}
}

// literalValue returns the JSON value of a literal expression, with
// unknownArg for anything only known at run time.
func literalValue(n js.Node) any {
	switch x := n.(type) {
	case *js.StringLit:
		return x.V
	case *js.NumberLit:
		return x.V
	case *js.BoolLit:
		return x.V
	case *js.NullLit, *js.UndefLit:
		return nil
	case *js.TemplateLit:
		if len(x.Exprs) == 0 {
			return strings.Join(x.Quasis, "")
		}
		return "" // still a string
	case *js.Unary:
		if f, ok := literalValue(x.X).(float64); ok && x.Op == "-" {
			return -f
		}
	case *js.ArrayLit:
		out := make([]any, 0, len(x.Elems))
		for _, el := range x.Elems {
			if _, sp := el.(*js.Spread); sp {
				return unknownArg{}
			}
			out = append(out, literalValue(el))
		}
		return out
	case *js.ObjectLit:
		out := make(map[string]any, len(x.Props))
		for _, p := range x.Props {
			if p.Spread || p.Computed != nil {
				return unknownArg{}
			}
			if p.Value == nil {
				out[p.Key] = unknownArg{} // shorthand {x}
				continue
			}
			v := literalValue(p.Value)
			if _, isUndef := p.Value.(*js.UndefLit); isUndef {
				continue // {k: undefined} is dropped, as at run time
			}
			out[p.Key] = v
		}
		return out
	}
	return unknownArg{}
}

// opsPath returns "a.b" for the callee ops.a.b.
func opsPath(n js.Node) (string, bool) {
	var parts []string
	for {
		m, ok := n.(*js.Member)
		if !ok {
			break
		}
		name := m.Name
		if m.Computed != nil {
			// ops["capture-screenshot"](...) is checked like ops.x.y; any other
			// computed key is only known at run time.
			s, ok := m.Computed.(*js.StringLit)
			if !ok || strings.Contains(s.V, ".") {
				return "", false
			}
			name = s.V
		}
		parts = append([]string{name}, parts...)
		n = m.Obj
	}
	id, ok := n.(*js.Ident)
	if !ok || id.Name != "ops" || len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "."), true
}

func (e *Engine) suggest(name string) string {
	best, bestD := "", 4
	for _, op := range e.Registry.Operations() {
		if d := levenshtein(name, op.Name); d < bestD {
			best, bestD = op.Name, d
		}
	}
	return best
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// Result is the outcome of running a program.
type Result struct {
	OK          bool         `json:"ok"`
	Value       any          `json:"value"`
	Error       string       `json:"error,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
	MS          int64        `json:"ms"`
	// NotRun is true when diagnostics stopped the program before it started.
	NotRun bool `json:"-"`
}

// Run compiles and runs a program. If the module's default export is a
// function it is called (and awaited) with no arguments; otherwise the
// result is the default export or the last expression statement's value.
func (e *Engine) Run(ctx context.Context, src, file string) Result {
	return e.RunWithInput(ctx, src, file, nil)
}

// RunWithInput is Run with a JSON value exposed to the program as the
// global `input` (null when input is nil). Callers such as an agent's own
// code mode use it to pass data in: `mycli code run - --input '{...}'`.
func (e *Engine) RunWithInput(ctx context.Context, src, file string, input json.RawMessage) Result {
	start := time.Now()
	p, diags := e.Compile(src, file)
	if hasErrors(diags) {
		return Result{OK: false, Error: "program has errors; nothing was run", Diagnostics: diags, MS: time.Since(start).Milliseconds(), NotRun: true}
	}
	it, err := e.newInterpInput(ctx, file, input)
	if err != nil {
		return Result{Error: err.Error(), NotRun: true}
	}
	defer it.Close()
	v, err := it.Run(p.ast, file)
	if err == nil {
		v, err = callIfFunc(it, v, p.ast)
	}
	res := Result{OK: err == nil, Value: js.ToGo(v), Diagnostics: diags, MS: time.Since(start).Milliseconds()}
	if err != nil {
		d := fromErr(err, file)
		res.Error = d.String()
		res.Diagnostics = append(res.Diagnostics, d)
		res.Value = nil
	}
	return res
}

// Eval evaluates a single expression.
func (e *Engine) Eval(ctx context.Context, expr string) Result {
	return e.EvalWithInput(ctx, expr, nil)
}

// EvalWithInput is Eval with the global `input` (see RunWithInput).
func (e *Engine) EvalWithInput(ctx context.Context, expr string, input json.RawMessage) Result {
	start := time.Now()
	x, err := js.ParseExpression(expr, "<eval>")
	if err != nil {
		d := fromErr(err, "<eval>")
		return Result{Error: d.String(), Diagnostics: []Diagnostic{d}, NotRun: true}
	}
	used := map[string]bool{}
	diags := e.checkOps(&js.Program{Body: []js.Node{&js.ExprStmt{X: x}}}, "<eval>", used)
	if hasErrors(diags) {
		return Result{Error: "expression has errors; nothing was run", Diagnostics: diags, NotRun: true}
	}
	it, err := e.newInterpInput(ctx, "<eval>", input)
	if err != nil {
		return Result{Error: err.Error(), NotRun: true}
	}
	defer it.Close()
	v, err := it.Eval(x, "<eval>")
	res := Result{OK: err == nil, Value: js.ToGo(v), MS: time.Since(start).Milliseconds()}
	if err != nil {
		d := fromErr(err, "<eval>")
		res.Error, res.Diagnostics, res.Value = d.String(), []Diagnostic{d}, nil
	}
	return res
}

func callIfFunc(it *js.Interp, v js.Value, at js.Node) (js.Value, error) {
	switch v.(type) {
	case *js.Function, *js.Builtin:
	default:
		return v, nil
	}
	var out js.Value
	var err error
	it.WithLock(func(r *js.Runner) {
		out, err = r.Call(v)
		if err == nil {
			out, err = r.Await(out, at)
		}
	})
	if err == nil {
		err = it.LastError()
	}
	return out, err
}

func hasErrors(ds []Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == "error" {
			return true
		}
	}
	return false
}

// newInterpInput is newInterp plus the global `input` (JSON, null if empty).
func (e *Engine) newInterpInput(ctx context.Context, file string, input json.RawMessage) (*js.Interp, error) {
	var v any
	if len(input) > 0 {
		pv, err := js.ParseJSON(input) // keeps document key order (LANG-6)
		if err != nil {
			return nil, fmt.Errorf("--input is not valid JSON: %v", err)
		}
		v = pv
	}
	it := e.newInterp(ctx, file)
	it.Define("input", js.FromGo(v))
	return it, nil
}

func (e *Engine) newInterp(ctx context.Context, file string) *js.Interp {
	out := e.Stdout
	if out == nil {
		out = os.Stderr
	}
	return e.newInterpCfg(ctx, file, js.Config{Limits: e.Policy.Limits, Stdout: out, Loader: e.loader(file)})
}

func (e *Engine) newInterpCfg(ctx context.Context, file string, cfg js.Config) *js.Interp {
	it := js.New(ctx, cfg)
	if cfg.Shared != nil && !cfg.Shared.Claim("host-globals") {
		return it // a session installs these once; they act through the calling run
	}
	it.Define("call", &js.Builtin{Name: "call", Fn: func(r *js.Runner, _ js.Value, args []js.Value) (js.Value, error) {
		if len(args) == 0 {
			return nil, typeErr("call(name, args) needs an operation name")
		}
		name, ok := args[0].(string)
		if !ok {
			return nil, typeErr("call(name, args): name must be a string")
		}
		var a js.Value = js.Undefined
		if len(args) > 1 {
			a = args[1]
		}
		return e.invoke(r, name, a)
	}})
	it.Define("ops", e.opsNamespace())
	e.installExec(it)
	js.InstallWorkflow(it, js.WorkflowConfig{CLI: cmp.Or(e.WorkflowCLI, filepath.Base(os.Args[0])), BaseDir: filepath.Dir(file)})
	return it
}

func typeErr(msg string) error {
	o := js.NewObject()
	o.Set("name", "TypeError")
	o.Set("message", msg)
	return &js.Throw{Value: o}
}

func (e *Engine) opsNamespace() *js.Object {
	root := js.NewObject()
	for _, op := range e.Registry.Operations() {
		parts := strings.Split(op.Name, ".")
		cur := root
		for _, p := range parts[:len(parts)-1] {
			next, ok := cur.Get(p)
			o, isObj := next.(*js.Object)
			if !ok || !isObj {
				o = js.NewObject()
				cur.Set(p, o)
			}
			cur = o
		}
		name := op.Name
		cur.Set(parts[len(parts)-1], &js.Builtin{Name: name, Fn: func(r *js.Runner, _ js.Value, args []js.Value) (js.Value, error) {
			var a js.Value = js.Undefined
			if len(args) > 0 {
				a = args[0]
			}
			return e.invoke(r, name, a)
		}})
	}
	freeze(root)
	return root
}

func freeze(o *js.Object) {
	o.Frozen = true
	for _, k := range o.Keys() {
		v, _ := o.Get(k)
		if c, ok := v.(*js.Object); ok {
			freeze(c)
		}
	}
}

// invoke validates and starts an operation, returning a promise.
func (e *Engine) invoke(r *js.Runner, name string, a js.Value) (js.Value, error) {
	if err := r.CountOp(); err != nil {
		return nil, err
	}
	op, ok := e.Registry.Lookup(name)
	if !ok {
		msg := fmt.Sprintf("unknown operation %q", name)
		if s := e.suggest(name); s != "" {
			msg += fmt.Sprintf("; did you mean %q?", s)
		}
		return nil, typeErr(msg)
	}
	var args map[string]any
	switch v := js.ToGo(a).(type) {
	case nil:
		args = map[string]any{}
	case map[string]any:
		args = v
	default:
		return nil, typeErr(fmt.Sprintf("%s: arguments must be an object, got %T", name, v))
	}
	if errs := validate(op.InputSchema, args); len(errs) > 0 {
		return nil, typeErr(fmt.Sprintf("%s: invalid arguments: %s", name, strings.Join(errs, "; ")))
	}
	ctx := r.Interp().Context()
	if e.Policy.Allow != nil {
		if err := e.Policy.Allow(ctx, op); err != nil {
			return nil, typeErr(fmt.Sprintf("%s: denied by policy: %v", name, err))
		}
	}
	return r.Interp().HostPromise(func(ctx context.Context) (js.Value, error) {
		out, err := op.Handler(ctx, args)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return resultValue(out)
	}), nil
}

// OrderedJSON is a handler result given as JSON text. The program sees the
// objects in it with their keys in document order (integer-like keys first,
// ascending, as in JavaScript); a Go map[string]any result has no order, so
// its keys arrive sorted. A bridge to another runtime (WASM, WASI) whose
// operations return JSON text should return OrderedJSON(text) instead of
// unmarshalling into any. An encoding/json.RawMessage result is treated the
// same way. Empty text is null; invalid JSON makes the call reject.
type OrderedJSON []byte

// MarshalJSON writes the text unchanged, so the order also survives when the
// result is marshalled again (MCP, CLI).
func (o OrderedJSON) MarshalJSON() ([]byte, error) {
	if len(bytes.TrimSpace(o)) == 0 {
		return []byte("null"), nil
	}
	return o, nil
}

// resultValue turns a handler result into a program value.
func resultValue(out any) (js.Value, error) {
	var raw []byte
	switch x := out.(type) {
	case OrderedJSON:
		raw = x
	case json.RawMessage:
		raw = x
	default:
		return js.FromGo(normalizeJSON(out)), nil
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	v, err := js.ParseJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("operation returned invalid JSON: %v", err)
	}
	return v, nil
}

// normalizeJSON makes handler results plain JSON data (struct -> ordered
// object, fields in declaration order).
func normalizeJSON(v any) any {
	switch v.(type) {
	case nil, bool, string, float64, int, int64, []any, map[string]any:
		return v
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	if o, err := js.ParseJSON(b); err == nil {
		return o
	}
	return fmt.Sprint(v)
}

// loader confines static imports to the module root (default: the entry
// program's directory). Only .js (code) and .json (data, default export)
// files can be imported. Programs read from stdin cannot import.
// within reports whether path p is root or inside it.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (e *Engine) loader(entry string) js.ModuleLoader {
	root := e.Policy.ModuleRoot
	if root == "" && entry != "" && !strings.HasPrefix(entry, "<") {
		root = filepath.Dir(entry)
	}
	return func(from, spec string) (string, string, error) {
		if root == "" {
			return "", "", errors.New("imports are not available for programs read from stdin or eval")
		}
		if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
			return "", "", errors.New("only relative imports (./ or ../) are allowed; there is no npm")
		}
		ext := filepath.Ext(spec)
		if ext != ".js" && ext != ".json" {
			return "", "", errors.New("import path must end in .js or .json")
		}
		base := root
		if from != "" && !strings.HasPrefix(from, "<") {
			base = filepath.Dir(from)
		}
		p := filepath.Clean(filepath.Join(base, spec))
		absRoot, _ := filepath.Abs(root)
		absP, _ := filepath.Abs(p)
		if !within(absRoot, absP) {
			return "", "", fmt.Errorf("%s is outside the module root %s", spec, root)
		}
		// Symlinks are resolved before the containment check, so a link inside
		// the root cannot point an import at a file outside it.
		realRoot, err := filepath.EvalSymlinks(absRoot)
		if err != nil {
			return "", "", err
		}
		realP, err := filepath.EvalSymlinks(absP)
		if err != nil {
			return "", "", err
		}
		if !within(realRoot, realP) {
			return "", "", fmt.Errorf("%s resolves outside the module root %s", spec, root)
		}
		b, err := os.ReadFile(realP)
		if err != nil {
			return "", "", err
		}
		src := string(b)
		if ext == ".json" {
			if !json.Valid(b) {
				return "", "", fmt.Errorf("%s is not valid JSON", spec)
			}
			src = "export default " + src
		}
		return src, p, nil
	}
}
