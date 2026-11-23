//go:build js && wasm

// Command codemode-wasm exposes the Code Mode engine to JavaScript. It sets
// globalThis.__codemode and then blocks forever.
//
// All methods take and return JSON strings unless noted. Engines are
// identified by the integer id that create returns. Methods that await
// anything (run, eval) return a Promise<string>.
//
// create(configJson, invoke) -> id (number), or {error:string} on bad config
//
//	configJson: {
//	  "version": "1.0.0",
//	  "operations": [{name, description, input_schema?, output_schema?,
//	                  side_effects?, idempotent?, authorization?, examples?}],
//	  "limits": {"max_steps":int, "max_call_depth":int,
//	             "max_concurrency":int, "timeout_ms":int},   // all optional
//	  "moduleRoot": "/dir"                                    // optional
//	}
//	invoke(name, argsJson) => Promise<string>: resolves with the result as
//	JSON text (empty string/undefined/null = null); a rejection or throw
//	becomes the operation's error.
//
// Sync, return a JSON string: operations(id), describe(id,name),
// capabilities(id), check(id,src,file), compile(id,src,file).
// Sync, return plain text: docs(id), skill(id), version().
// Async, Promise<JSON string>: run(id,src,file,inputJson,callId,timeoutMs),
// eval(id,expr,inputJson,callId,timeoutMs). callId (string, optional) names
// the call so cancel(callId) can abort it; timeoutMs (decimal string,
// optional) bounds this call in addition to the engine's limits. Cancelling
// or timing out cancels the Go context: the interpreter stops and any host
// operation still pending is abandoned (its JS promise is left unresolved
// and ignored). The JSON is codemode.Result plus a "stdout" string holding
// everything the program printed.
// cancel(callId) -> true if a call with that id was running.
//
// Unknown ids and similar caller errors return {"error":"..."} JSON.
// exec/execStream/shell are unavailable on wasm (Policy.Exec is zero, so
// programs get a denied-by-policy error).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"syscall/js"
	"time"

	"github.com/muthuishere/clicodemode/codemode"
	js_ "github.com/muthuishere/clicodemode/js"
)

type config struct {
	Version    string               `json:"version"`
	Operations []codemode.Operation `json:"operations"`
	Limits     struct {
		MaxSteps       int64 `json:"max_steps"`
		MaxCallDepth   int   `json:"max_call_depth"`
		MaxConcurrency int   `json:"max_concurrency"`
		TimeoutMS      int64 `json:"timeout_ms"`
	} `json:"limits"`
	ModuleRoot string `json:"moduleRoot"`
}

var (
	mu      sync.Mutex
	engines = map[int]*codemode.Engine{}
	nextID  = 1

	calls = map[string]context.CancelFunc{} // running calls by callId, guarded by mu
)

// callCtx returns the context for one run/eval call and a release func.
// Caveat: js/wasm has no preemption, so while the interpreter runs a CPU-bound
// loop no timer or JS callback can fire; timeout/cancel take effect only when
// the program awaits a host operation (or finishes). CPU loops are bounded by
// Limits.MaxSteps.
func callCtx(callID, timeoutMS string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	if ms, err := strconv.ParseInt(timeoutMS, 10, 64); err == nil && ms > 0 {
		var c2 context.CancelFunc
		ctx, c2 = context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
		prev := cancel
		cancel = func() { c2(); prev() }
	}
	if callID != "" {
		mu.Lock()
		calls[callID] = cancel
		mu.Unlock()
	}
	return ctx, func() {
		cancel()
		if callID != "" {
			mu.Lock()
			delete(calls, callID)
			mu.Unlock()
		}
	}
}

func get(id js.Value) (*codemode.Engine, error) {
	mu.Lock()
	defer mu.Unlock()
	e, ok := engines[id.Int()]
	if !ok {
		return nil, fmt.Errorf("unknown engine id %d", id.Int())
	}
	return e, nil
}

func jsonOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return errJSON(err.Error())
	}
	return string(b)
}

func errJSON(msg string) string { return jsonOf(map[string]string{"error": msg}) }

// await waits for a JS promise (or plain value) from a goroutine.
func await(ctx context.Context, v js.Value) (js.Value, error) {
	if v.Type() != js.TypeObject || v.Get("then").Type() != js.TypeFunction {
		return v, nil
	}
	type res struct {
		v   js.Value
		err error
	}
	ch := make(chan res, 1)
	var ok, bad js.Func
	ok = js.FuncOf(func(_ js.Value, a []js.Value) any {
		r := js.Undefined()
		if len(a) > 0 {
			r = a[0]
		}
		ch <- res{v: r}
		return nil
	})
	bad = js.FuncOf(func(_ js.Value, a []js.Value) any {
		msg := "operation rejected"
		if len(a) > 0 {
			if m := a[0].Get("message"); m.Type() == js.TypeString {
				msg = m.String()
			} else if a[0].Type() == js.TypeString {
				msg = a[0].String()
			} else {
				msg = js.Global().Get("String").Invoke(a[0]).String()
			}
		}
		ch <- res{err: errors.New(msg)}
		return nil
	})
	v.Call("then", ok, bad)
	select {
	case r := <-ch:
		ok.Release()
		bad.Release()
		return r.v, r.err
	case <-ctx.Done():
		// Abandoned: the promise may still settle later, so the callbacks
		// are deliberately not released (a released func would be called).
		// ch is buffered, so a late settle never blocks.
		return js.Undefined(), ctx.Err()
	}
}

// callInvoke calls the JS invoke function, converting a synchronous throw
// into an error.
func callInvoke(fn js.Value, name, args string) (out js.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	return fn.Invoke(name, args), nil
}

func create(this js.Value, a []js.Value) any {
	if len(a) < 2 || a[0].Type() != js.TypeString || a[1].Type() != js.TypeFunction {
		return map[string]any{"error": "create(configJson, invoke): need a JSON string and a function"}
	}
	var cfg config
	if err := json.Unmarshal([]byte(a[0].String()), &cfg); err != nil {
		return map[string]any{"error": "bad config JSON: " + err.Error()}
	}
	invoke := a[1]
	reg := codemode.NewRegistry(cfg.Version)
	for _, op := range cfg.Operations {
		name := op.Name
		op.Handler = func(ctx context.Context, args map[string]any) (any, error) {
			if args == nil {
				args = map[string]any{}
			}
			b, err := json.Marshal(args)
			if err != nil {
				return nil, err
			}
			p, err := callInvoke(invoke, name, string(b))
			if err != nil {
				return nil, err
			}
			v, err := await(ctx, p)
			if err != nil {
				return nil, err
			}
			var text string
			switch v.Type() {
			case js.TypeUndefined, js.TypeNull:
				return nil, nil
			case js.TypeString:
				text = v.String()
			default:
				s := js.Global().Get("JSON").Call("stringify", v)
				if s.Type() != js.TypeString {
					return nil, nil
				}
				text = s.String()
			}
			if strings.TrimSpace(text) == "" {
				return nil, nil
			}
			if !json.Valid([]byte(text)) {
				return nil, errors.New("operation returned invalid JSON")
			}
			return codemode.OrderedJSON([]byte(text)), nil
		}
		if err := reg.Register(op); err != nil {
			return map[string]any{"error": err.Error()}
		}
	}
	eng := codemode.NewEngine(reg)
	eng.Policy.Limits = js_.Limits{
		MaxSteps:       cfg.Limits.MaxSteps,
		MaxCallDepth:   cfg.Limits.MaxCallDepth,
		MaxConcurrency: cfg.Limits.MaxConcurrency,
		Timeout:        time.Duration(cfg.Limits.TimeoutMS) * time.Millisecond,
	}
	eng.Policy.ModuleRoot = cfg.ModuleRoot
	mu.Lock()
	id := nextID
	nextID++
	engines[id] = eng
	mu.Unlock()
	return id
}

// sync wraps a method that takes an engine id plus string args.
func syncFn(f func(e *codemode.Engine, s []string) string) js.Func {
	return js.FuncOf(func(_ js.Value, a []js.Value) any {
		if len(a) == 0 {
			return errJSON("missing engine id")
		}
		e, err := get(a[0])
		if err != nil {
			return errJSON(err.Error())
		}
		s := make([]string, 0, len(a)-1)
		for _, v := range a[1:] {
			if v.Type() == js.TypeString {
				s = append(s, v.String())
			} else {
				s = append(s, "")
			}
		}
		for len(s) < 3 {
			s = append(s, "")
		}
		return f(e, s)
	})
}

// promiseFn wraps a method that does its work off the JS callback.
func promiseFn(f func(e *codemode.Engine, s []string) string) js.Func {
	return js.FuncOf(func(_ js.Value, a []js.Value) any {
		var e *codemode.Engine
		var gerr error
		s := []string{"", "", "", "", "", ""}
		if len(a) == 0 {
			gerr = errors.New("missing engine id")
		} else {
			e, gerr = get(a[0])
			for i, v := range a[1:] {
				if i < len(s) && v.Type() == js.TypeString {
					s[i] = v.String()
				}
			}
		}
		var h js.Func
		h = js.FuncOf(func(_ js.Value, pa []js.Value) any {
			resolve := pa[0]
			go func() {
				defer h.Release()
				defer func() {
					if r := recover(); r != nil {
						resolve.Invoke(errJSON(fmt.Sprint("internal error: ", r)))
					}
				}()
				if gerr != nil {
					resolve.Invoke(errJSON(gerr.Error()))
					return
				}
				resolve.Invoke(f(e, s))
			}()
			return nil
		})
		return js.Global().Get("Promise").New(h)
	})
}

// withOutput runs fn on an engine copy whose print output is captured, then
// adds it to the result JSON as "stdout".
func withOutput(e *codemode.Engine, fn func(*codemode.Engine) codemode.Result) string {
	var buf strings.Builder
	c := *e
	c.Stdout = &buf
	r := fn(&c)
	b, _ := json.Marshal(r)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	m["stdout"] = buf.String()
	return jsonOf(m)
}

func rawInput(s string) json.RawMessage {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return json.RawMessage(s)
}

func main() {
	api := map[string]any{
		"create":  js.FuncOf(create),
		"version": js.FuncOf(func(js.Value, []js.Value) any { return codemode.LibraryVersion }),
		"operations": syncFn(func(e *codemode.Engine, _ []string) string {
			return jsonOf(e.Registry.Operations())
		}),
		"cancel": js.FuncOf(func(_ js.Value, a []js.Value) any {
			if len(a) == 0 || a[0].Type() != js.TypeString {
				return false
			}
			mu.Lock()
			cancel, ok := calls[a[0].String()]
			mu.Unlock()
			if ok {
				cancel()
			}
			return ok
		}),
		"describe": syncFn(func(e *codemode.Engine, s []string) string {
			op, ok := e.Registry.Lookup(s[0])
			if !ok {
				return errJSON(fmt.Sprintf("unknown operation %q", s[0]))
			}
			return jsonOf(op)
		}),
		"capabilities": syncFn(func(e *codemode.Engine, _ []string) string {
			return jsonOf(e.Registry.Capabilities())
		}),
		"check": syncFn(func(e *codemode.Engine, s []string) string {
			_, d := e.Compile(s[0], fileName(s[1]))
			return jsonOf(map[string]any{"diagnostics": nonNil(d)})
		}),
		"compile": syncFn(func(e *codemode.Engine, s []string) string {
			p, d := e.Compile(s[0], fileName(s[1]))
			return jsonOf(map[string]any{"program": p, "diagnostics": nonNil(d)})
		}),
		"docs":  syncFn(func(e *codemode.Engine, _ []string) string { return codemode.Docs(e.Registry, "code") }),
		"skill": syncFn(func(e *codemode.Engine, _ []string) string { return codemode.Skill(e.Registry, "cli code") }),
		"run": promiseFn(func(e *codemode.Engine, s []string) string {
			ctx, done := callCtx(s[3], s[4])
			defer done()
			return withOutput(e, func(c *codemode.Engine) codemode.Result {
				return c.RunWithInput(ctx, s[0], fileName(s[1]), rawInput(s[2]))
			})
		}),
		"eval": promiseFn(func(e *codemode.Engine, s []string) string {
			ctx, done := callCtx(s[2], s[3])
			defer done()
			return withOutput(e, func(c *codemode.Engine) codemode.Result {
				return c.EvalWithInput(ctx, s[0], rawInput(s[1]))
			})
		}),
	}
	obj := js.Global().Get("Object").New()
	for k, v := range api {
		obj.Set(k, v)
	}
	js.Global().Set("__codemode", obj)
	select {}
}

func fileName(f string) string {
	if f == "" {
		return "<program>"
	}
	return f
}

func nonNil(d []codemode.Diagnostic) []codemode.Diagnostic {
	if d == nil {
		return []codemode.Diagnostic{}
	}
	return d
}
