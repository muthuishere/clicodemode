//go:build wasip1

// Command codemode-wasi is the Code Mode engine as a WASI command, driven by
// a host (the Rust crate in rust/) over JSON lines on stdin/stdout. One
// process hosts one engine for its whole life.
//
// Host -> engine, one JSON object per line:
//
//	{"id":N,"method":"create","params":{"version":"1.0.0","operations":[...],
//	    "limits":{"max_steps":0,"max_call_depth":0,"max_concurrency":0,"timeout_ms":0}}}
//	{"id":N,"method":"operations|capabilities|docs|skill|version"}
//	{"id":N,"method":"describe","params":{"name":"a.b"}}
//	{"id":N,"method":"check|compile","params":{"src":"...","file":"prog.js"}}
//	{"id":N,"method":"run","params":{"src":"...","file":"prog.js","input":<json>,"timeout_ms":0}}
//	{"id":N,"method":"eval","params":{"expr":"1+2","input":<json>,"timeout_ms":0}}
//
// timeout_ms, when positive, replaces the create-time timeout for that one
// run or eval.
//
// Engine -> host:
//
//	{"id":N,"ok":true,"result":...}   or   {"id":N,"ok":false,"error":"..."}
//	{"log":"text"}                    print/console.log output (never the reply)
//	{"call":{"id":C,"name":"a.b","args":{...}}}
//	    emitted while a run is in progress; the engine then reads
//	{"callResult":{"id":C,"ok":true,"result":...}} (or "ok":false,"error":"...")
//	from stdin. Several calls may be outstanding; results match by call id.
//
// exec, execStream and shell are unavailable (Policy.Exec is zero), so
// programs get a "not permitted" error. There is no filesystem, so imports
// are unavailable too.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/muthuishere/clicodemode/codemode"
	"github.com/muthuishere/clicodemode/js"
)

type request struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type callResult struct {
	ID     int64           `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

type inbound struct {
	request
	CallResult *callResult `json:"callResult"`
}

type config struct {
	Version    string               `json:"version"`
	Operations []codemode.Operation `json:"operations"`
	// Modules are JS/JSON modules the host ships in its build (import "scheme:name").
	Modules []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Source      string `json:"source"`
	} `json:"modules"`
	Limits struct {
		MaxSteps       int64 `json:"max_steps"`
		MaxCallDepth   int   `json:"max_call_depth"`
		MaxConcurrency int   `json:"max_concurrency"`
		TimeoutMS      int64 `json:"timeout_ms"`
	} `json:"limits"`
}

var (
	outMu sync.Mutex
	out   = bufio.NewWriterSize(os.Stdout, 64*1024)

	// Reading stdin blocks the whole single-threaded wasm process, so only a
	// goroutine that has already emitted a call (and so knows the host will
	// answer) may read; the reader mutex serialises them and results are
	// handed over through the done map.
	rdMu   sync.Mutex
	in     = bufio.NewReaderSize(os.Stdin, 1<<20)
	done   = map[int64]callResult{}
	nextCh int64
	chMu   sync.Mutex
)

func emit(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"log": "codemode-wasi: cannot encode message: " + err.Error()})
	}
	outMu.Lock()
	out.Write(b)
	out.WriteByte('\n')
	out.Flush()
	outMu.Unlock()
}

func readLine() ([]byte, error) {
	var line []byte
	for {
		part, isPrefix, err := in.ReadLine()
		if err != nil {
			return nil, err
		}
		line = append(line, part...)
		if !isPrefix {
			return line, nil
		}
	}
}

type logWriter struct{}

func (logWriter) Write(p []byte) (int, error) {
	emit(map[string]string{"log": string(p)})
	return len(p), nil
}

// hostCall emits a call and waits for its result.
func hostCall(name string, args map[string]any) (any, error) {
	chMu.Lock()
	nextCh++
	id := nextCh
	chMu.Unlock()
	emit(map[string]any{"call": map[string]any{"id": id, "name": name, "args": args}})
	for {
		rdMu.Lock()
		if r, ok := done[id]; ok {
			delete(done, id)
			rdMu.Unlock()
			if !r.OK {
				if r.Error == "" {
					r.Error = "operation failed"
				}
				return nil, fmt.Errorf("%s", r.Error)
			}
			// OrderedJSON keeps the host's object key order (LANG-6); empty is
			// null and invalid JSON rejects the call.
			return codemode.OrderedJSON(r.Result), nil
		}
		line, err := readLine()
		if err != nil {
			rdMu.Unlock()
			return nil, fmt.Errorf("host closed the connection")
		}
		var m inbound
		if err := json.Unmarshal(line, &m); err == nil && m.CallResult != nil {
			done[m.CallResult.ID] = *m.CallResult
		}
		rdMu.Unlock()
	}
}

var engine *codemode.Engine

func create(params json.RawMessage) (any, error) {
	if engine != nil {
		return nil, fmt.Errorf("engine already created in this process")
	}
	var cfg config
	if err := json.Unmarshal(params, &cfg); err != nil {
		return nil, fmt.Errorf("bad create params: %v", err)
	}
	reg := codemode.NewRegistry(cfg.Version)
	for _, op := range cfg.Operations {
		name := op.Name
		op.Handler = func(ctx context.Context, args map[string]any) (any, error) {
			return hostCall(name, args)
		}
		if err := reg.Register(op); err != nil {
			return nil, err
		}
	}
	for _, m := range cfg.Modules {
		if err := reg.RegisterModule(codemode.Module{Name: m.Name, Description: m.Description, Source: m.Source}); err != nil {
			return nil, err
		}
	}
	e := codemode.NewEngine(reg)
	e.Stdout = logWriter{}
	e.Policy.Limits = js.Limits{
		MaxSteps:       cfg.Limits.MaxSteps,
		MaxCallDepth:   cfg.Limits.MaxCallDepth,
		MaxConcurrency: cfg.Limits.MaxConcurrency,
		Timeout:        time.Duration(cfg.Limits.TimeoutMS) * time.Millisecond,
	}
	engine = e
	return map[string]any{"operations": len(cfg.Operations)}, nil
}

func decode(params json.RawMessage, v any) error {
	if len(params) == 0 {
		return nil
	}
	return json.Unmarshal(params, v)
}

func handle(r request) (any, error) {
	if r.Method == "create" {
		return create(r.Params)
	}
	if r.Method == "version" {
		return fmt.Sprintf("clicodemode %s, spec %s", codemode.LibraryVersion, codemode.SpecVersion), nil
	}
	if engine == nil {
		return nil, fmt.Errorf("engine not created; send create first")
	}
	reg := engine.Registry
	ctx := context.Background()
	switch r.Method {
	case "operations":
		return reg.Operations(), nil
	case "capabilities":
		return reg.Capabilities(), nil
	case "describe":
		var p struct {
			Name string `json:"name"`
		}
		if err := decode(r.Params, &p); err != nil {
			return nil, err
		}
		op, ok := reg.Lookup(p.Name)
		if !ok {
			return nil, fmt.Errorf("unknown operation %q", p.Name)
		}
		return op, nil
	case "docs":
		return codemode.Docs(reg, "<cli> code"), nil
	case "skill":
		return codemode.Skill(reg, "<cli> code"), nil
	case "check", "compile":
		var p struct{ Src, File string }
		if err := decode(r.Params, &p); err != nil {
			return nil, err
		}
		if p.File == "" {
			p.File = "program.js"
		}
		prog, diags := engine.Compile(p.Src, p.File)
		if diags == nil {
			diags = []codemode.Diagnostic{}
		}
		if r.Method == "check" {
			return diags, nil
		}
		return map[string]any{"program": prog, "diagnostics": diags}, nil
	case "run":
		var p struct {
			Src, File string
			Input     json.RawMessage
			TimeoutMS int64 `json:"timeout_ms"`
		}
		if err := decode(r.Params, &p); err != nil {
			return nil, err
		}
		if p.File == "" {
			p.File = "program.js"
		}
		defer withTimeout(p.TimeoutMS)()
		return engine.RunWithInput(ctx, p.Src, p.File, nullToEmpty(p.Input)), nil
	case "eval":
		var p struct {
			Expr      string
			Input     json.RawMessage
			TimeoutMS int64 `json:"timeout_ms"`
		}
		if err := decode(r.Params, &p); err != nil {
			return nil, err
		}
		defer withTimeout(p.TimeoutMS)()
		return engine.EvalWithInput(ctx, p.Expr, nullToEmpty(p.Input)), nil
	}
	return nil, fmt.Errorf("unknown method %q", r.Method)
}

// withTimeout overrides the engine timeout for one request and returns the
// function that restores it. Requests are handled one at a time.
func withTimeout(ms int64) func() {
	if ms <= 0 {
		return func() {}
	}
	prev := engine.Policy.Limits.Timeout
	engine.Policy.Limits.Timeout = time.Duration(ms) * time.Millisecond
	return func() { engine.Policy.Limits.Timeout = prev }
}

func nullToEmpty(b json.RawMessage) json.RawMessage {
	if string(b) == "null" {
		return nil
	}
	return b
}

func main() {
	for {
		rdMu.Lock()
		line, err := readLine()
		rdMu.Unlock()
		if err != nil {
			return
		}
		if len(line) == 0 {
			continue
		}
		var m inbound
		if err := json.Unmarshal(line, &m); err != nil {
			emit(map[string]any{"id": 0, "ok": false, "error": "bad request: " + err.Error()})
			continue
		}
		if m.CallResult != nil {
			continue // stray
		}
		res, err := handle(m.request)
		if err != nil {
			emit(map[string]any{"id": m.ID, "ok": false, "error": err.Error()})
			continue
		}
		emit(map[string]any{"id": m.ID, "ok": true, "result": res})
	}
}
