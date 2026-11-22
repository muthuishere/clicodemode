package codemode

import (
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
		return Diagnostic{Severity: "error", File: f, Line: je.Pos.Line, Col: je.Pos.Col, Message: je.Kind + " error: " + je.Msg}
	}
	var th *js.Throw
	if errors.As(err, &th) {
		f := th.File
		if f == "" {
			f = file
		}
		msg := strings.TrimPrefix(err.Error(), fmt.Sprintf("%s:%s: ", f, th.Pos))
		return Diagnostic{Severity: "error", File: f, Line: th.Pos.Line, Col: th.Pos.Col, Message: msg}
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
		}
	})
	return diags
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
		if err := json.Unmarshal(input, &v); err != nil {
			return nil, fmt.Errorf("--input is not valid JSON: %v", err)
		}
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
	it := js.New(ctx, js.Config{Limits: e.Policy.Limits, Stdout: out, Loader: e.loader(file)})
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
		return js.FromGo(normalizeJSON(out)), nil
	}), nil
}

// normalizeJSON makes handler results plain JSON data (struct -> map etc.).
func normalizeJSON(v any) any {
	switch v.(type) {
	case nil, bool, string, float64, int, int64, []any, map[string]any:
		return v
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// loader confines static imports to the module root (default: the entry
// program's directory). Only .js (code) and .json (data, default export)
// files can be imported. Programs read from stdin cannot import.
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
		if rel, err := filepath.Rel(absRoot, absP); err != nil || strings.HasPrefix(rel, "..") {
			return "", "", fmt.Errorf("%s is outside the module root %s", spec, root)
		}
		b, err := os.ReadFile(p)
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
