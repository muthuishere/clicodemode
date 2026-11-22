// Package mcp exposes a codemode.Engine as a Model Context Protocol server
// over newline-delimited JSON-RPC 2.0 on stdio. It is optional: the codemode
// package does not import it, and hosts that do not want MCP never link it.
//
// The server follows the Code Mode shape: a handful of tools (capabilities,
// describe, check, run, docs) instead of one tool per operation. Set
// Options.ExposeOperations to also publish every operation as its own tool.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/muthuishere/clicodemode/codemode"
)

// ProtocolVersion is the MCP revision this server implements.
const ProtocolVersion = "2025-06-18"

// JSON-RPC error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Options configures Serve.
type Options struct {
	// Name is serverInfo.name; default "clicodemode".
	Name string
	// ExposeOperations also lists each registered operation as its own tool
	// (dots replaced by underscores).
	ExposeOperations bool
	// Instructions is optional text returned from initialize.
	Instructions string
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type server struct {
	e    *codemode.Engine
	opts Options

	wmu sync.Mutex
	w   io.Writer

	mu       sync.Mutex
	inflight map[string]context.CancelFunc
}

// Serve reads requests from r and writes responses to w until r reaches EOF
// or ctx is done. Requests are handled concurrently; writes are serialized.
// In-flight tool calls are cancelled by ctx or notifications/cancelled.
func Serve(ctx context.Context, e *codemode.Engine, r io.Reader, w io.Writer, opts Options) error {
	if opts.Name == "" {
		opts.Name = "clicodemode"
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &server{e: e, opts: opts, w: w, inflight: map[string]context.CancelFunc{}}

	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		br := bufio.NewReaderSize(r, 64*1024)
		for {
			line, err := br.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				select {
				case lines <- line:
				case <-ctx.Done():
					readErr <- ctx.Err()
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil
				}
				readErr <- err
				return
			}
		}
	}()

	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			wg.Wait()
			return err
		case line := <-lines:
			s.dispatch(ctx, line, &wg)
		}
	}
}

func (s *server) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{CodeInternalError, err.Error()}})
	}
	b = append(b, '\n')
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, _ = s.w.Write(b)
}

func (s *server) reply(id json.RawMessage, result any) {
	s.write(response{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *server) fail(id json.RawMessage, code int, msg string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	s.write(response{JSONRPC: "2.0", ID: id, Error: &rpcError{code, msg}})
}

func (s *server) dispatch(ctx context.Context, line []byte, wg *sync.WaitGroup) {
	trimmed := bytes.TrimSpace(line)
	if !json.Valid(trimmed) {
		s.fail(nil, CodeParseError, "parse error")
		return
	}
	if trimmed[0] == '[' {
		s.fail(nil, CodeInvalidRequest, "batch requests are not supported")
		return
	}
	var req request
	if err := json.Unmarshal(trimmed, &req); err != nil || req.JSONRPC != "2.0" || req.Method == "" {
		s.fail(req.ID, CodeInvalidRequest, "invalid request")
		return
	}
	isNotification := len(req.ID) == 0
	if !isNotification && !validID(req.ID) {
		s.fail(nil, CodeInvalidRequest, "invalid request id")
		return
	}
	if isNotification {
		s.notification(req)
		return
	}
	// Register the cancel func before starting, so a notifications/cancelled
	// that arrives right after the request always finds it.
	rctx, cancel := context.WithCancel(ctx)
	key := string(req.ID)
	s.mu.Lock()
	s.inflight[key] = cancel
	s.mu.Unlock()
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.inflight, key)
			s.mu.Unlock()
			cancel()
		}()
		s.handle(rctx, req)
	}()
}

func validID(id json.RawMessage) bool {
	c := id[0]
	return c == '"' || c == '-' || (c >= '0' && c <= '9')
}

func (s *server) notification(req request) {
	switch req.Method {
	case "notifications/cancelled":
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(req.Params, &p) == nil && len(p.RequestID) > 0 {
			s.mu.Lock()
			if c, ok := s.inflight[string(p.RequestID)]; ok {
				c()
			}
			s.mu.Unlock()
		}
	default:
		// notifications/initialized and unknown notifications are ignored.
	}
}

func (s *server) handle(ctx context.Context, req request) {
	defer func() {
		if p := recover(); p != nil {
			s.fail(req.ID, CodeInternalError, fmt.Sprintf("internal error: %v", p))
		}
	}()
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				s.fail(req.ID, CodeInvalidParams, "invalid params: "+err.Error())
				return
			}
		}
		// Only one revision is implemented; per the MCP lifecycle rules an
		// unsupported request (e.g. 2024-11-05) is answered with ours and the
		// client decides whether to continue.
		v := ProtocolVersion
		res := map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.opts.Name, "version": s.e.Registry.Version},
		}
		if s.opts.Instructions != "" {
			res["instructions"] = s.opts.Instructions
		}
		s.reply(req.ID, res)
	case "ping":
		s.reply(req.ID, map[string]any{})
	case "tools/list":
		s.reply(req.ID, map[string]any{"tools": s.tools()})
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			s.fail(req.ID, CodeInvalidParams, "invalid params: tools/call needs {name, arguments}")
			return
		}
		res, rerr := s.call(ctx, p.Name, p.Arguments)
		if rerr != nil {
			s.fail(req.ID, rerr.Code, rerr.Message)
			return
		}
		s.reply(req.ID, res)
	default:
		s.fail(req.ID, CodeMethodNotFound, "method not found: "+req.Method)
	}
}

type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations map[string]any  `json:"annotations,omitempty"`
}

const emptySchema = `{"type":"object","properties":{}}`

func (s *server) tools() []tool {
	ro := map[string]any{"readOnlyHint": true}
	ts := []tool{
		{Name: "code_capabilities", Description: "List every operation with its contract, the program language, and a fingerprint to cache on.", InputSchema: json.RawMessage(emptySchema), Annotations: ro},
		{Name: "code_describe", Description: "Describe one operation: input/output schema, side effects, examples.", InputSchema: json.RawMessage(`{"type":"object","properties":{"operation":{"type":"string","description":"dotted operation name, e.g. health.check"}},"required":["operation"]}`), Annotations: ro},
		{Name: "code_check", Description: "Check a program without running it; returns diagnostics with line:col.", InputSchema: json.RawMessage(`{"type":"object","properties":{"program":{"type":"string","description":"codemode-js source"},"input":{"description":"any JSON value, exposed to the program as the global input"}},"required":["program"]}`), Annotations: ro},
		{Name: "code_run", Description: "Run a codemode-js program that calls operations via call(name, args) or ops.a.b(args). Its result is the default export or the last expression.", InputSchema: json.RawMessage(`{"type":"object","properties":{"program":{"type":"string","description":"codemode-js source"}},"required":["program"]}`)},
		{Name: "code_docs", Description: "Reference for the program language and every operation.", InputSchema: json.RawMessage(emptySchema), Annotations: ro},
	}
	if s.opts.ExposeOperations {
		for _, op := range s.e.Registry.Operations() {
			schema := op.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(emptySchema)
			}
			var ann map[string]any
			if op.SideEffects == codemode.SideEffectNone {
				ann = map[string]any{"readOnlyHint": true, "idempotentHint": op.Idempotent}
			} else {
				ann = map[string]any{"readOnlyHint": false, "idempotentHint": op.Idempotent, "openWorldHint": op.SideEffects == codemode.SideEffectExternal}
			}
			ts = append(ts, tool{Name: toolName(op.Name), Description: op.Description, InputSchema: schema, Annotations: ann})
		}
	}
	return ts
}

func toolName(op string) string { return strings.ReplaceAll(op, ".", "_") }

// opForTool maps a tool name back to its operation. Operation names may
// contain underscores themselves, so match against the registry.
func (s *server) opForTool(name string) (*codemode.Operation, bool) {
	for _, op := range s.e.Registry.Operations() {
		if toolName(op.Name) == name {
			return op, true
		}
	}
	return nil, false
}

func toolResult(v any, isError bool) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"error": err.Error()})
		isError = true
	}
	res := map[string]any{"content": []map[string]any{{"type": "text", "text": string(b)}}}
	// structuredContent must be a JSON object.
	var obj map[string]any
	if json.Unmarshal(b, &obj) == nil && obj != nil {
		res["structuredContent"] = obj
	}
	if isError {
		res["isError"] = true
	}
	return res
}

func textResult(text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
}

func stringArg(args map[string]any, k string) (string, *rpcError) {
	v, ok := args[k].(string)
	if !ok {
		return "", &rpcError{CodeInvalidParams, fmt.Sprintf("invalid params: %q (string) is required", k)}
	}
	return v, nil
}

const programFile = "<mcp>"

func (s *server) call(ctx context.Context, name string, args map[string]any) (map[string]any, *rpcError) {
	switch name {
	case "code_capabilities":
		return toolResult(s.e.Registry.Capabilities(), false), nil
	case "code_describe":
		opName, err := stringArg(args, "operation")
		if err != nil {
			return nil, err
		}
		op, ok := s.e.Registry.Lookup(opName)
		if !ok {
			return toolResult(map[string]any{"error": fmt.Sprintf("unknown operation %q", opName)}, true), nil
		}
		return toolResult(op, false), nil
	case "code_check":
		src, err := stringArg(args, "program")
		if err != nil {
			return nil, err
		}
		p, diags := s.e.Compile(src, programFile)
		ok := true
		for _, d := range diags {
			if d.Severity == "error" {
				ok = false
			}
		}
		out := map[string]any{"ok": ok, "diagnostics": diags}
		if diags == nil {
			out["diagnostics"] = []codemode.Diagnostic{}
		}
		if p != nil {
			out["operations"] = p.Operations
			out["hash"] = p.Hash
		}
		return toolResult(out, !ok), nil
	case "code_run":
		src, err := stringArg(args, "program")
		if err != nil {
			return nil, err
		}
		var input json.RawMessage
		if v, ok := args["input"]; ok {
			input, _ = json.Marshal(v)
		}
		r := s.e.RunWithInput(ctx, src, programFile, input)
		return toolResult(r, !r.OK), nil
	case "code_docs":
		return textResult(codemode.Docs(s.e.Registry, s.opts.Name)), nil
	}
	if s.opts.ExposeOperations {
		if op, ok := s.opForTool(name); ok {
			return s.callOperation(ctx, op, args), nil
		}
	}
	return nil, &rpcError{CodeInvalidParams, "unknown tool: " + name}
}

// callOperation runs one operation through the engine (so validation, policy
// and limits apply) by wrapping it in a one-line program.
func (s *server) callOperation(ctx context.Context, op *codemode.Operation, args map[string]any) map[string]any {
	if args == nil {
		args = map[string]any{}
	}
	nameJSON, _ := json.Marshal(op.Name)
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return toolResult(map[string]any{"ok": false, "error": err.Error()}, true)
	}
	src := fmt.Sprintf("export default async () => await call(%s, %s)", nameJSON, argsJSON)
	r := s.e.Run(ctx, src, programFile)
	return toolResult(r, !r.OK)
}
