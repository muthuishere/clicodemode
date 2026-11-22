package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/clicodemode/codemode"
)

type client struct {
	t   *testing.T
	in  *io.PipeWriter
	out *bufio.Scanner
	err chan error
}

func newClient(t *testing.T, opts Options) *client {
	t.Helper()
	reg := codemode.NewRegistry("test 1.0")
	reg.MustRegister(codemode.Operation{
		Name: "text.upper", Description: "upper",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		SideEffects: codemode.SideEffectNone, Idempotent: true,
		Handler: func(ctx context.Context, a map[string]any) (any, error) {
			return strings.ToUpper(a["text"].(string)), nil
		},
	})
	reg.MustRegister(codemode.Operation{
		Name: "time.block", Description: "blocks until cancelled",
		SideEffects: codemode.SideEffectNone,
		Handler: func(ctx context.Context, a map[string]any) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	eng := codemode.NewEngine(reg)
	eng.Stdout = io.Discard
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &client{t: t, in: inW, out: bufio.NewScanner(outR), err: make(chan error, 1)}
	c.out.Buffer(make([]byte, 1<<20), 1<<20)
	go func() {
		c.err <- Serve(context.Background(), eng, inR, outW, opts)
		outW.Close()
	}()
	t.Cleanup(func() {
		inW.Close()
		select {
		case err := <-c.err:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after EOF")
		}
	})
	return c
}

func (c *client) send(line string) {
	c.t.Helper()
	if _, err := io.WriteString(c.in, line+"\n"); err != nil {
		c.t.Fatal(err)
	}
}

type resp struct {
	ID     json.RawMessage `json:"id"`
	Result map[string]any  `json:"result"`
	Error  *rpcError       `json:"error"`
}

func (c *client) recv() resp {
	c.t.Helper()
	done := make(chan bool, 1)
	var ok bool
	go func() { ok = c.out.Scan(); done <- true }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		c.t.Fatal("timed out waiting for response")
	}
	if !ok {
		c.t.Fatalf("no response: %v", c.out.Err())
	}
	var r resp
	if err := json.Unmarshal(c.out.Bytes(), &r); err != nil {
		c.t.Fatalf("bad response %q: %v", c.out.Text(), err)
	}
	return r
}

func (c *client) roundTrip(line string) resp {
	c.t.Helper()
	c.send(line)
	return c.recv()
}

func callLine(id int, tool string, args any) string {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
	return string(b)
}

func TestInitializeAndList(t *testing.T) {
	c := newClient(t, Options{ExposeOperations: true})
	r := c.roundTrip(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
	if r.Error != nil || r.Result["protocolVersion"] != ProtocolVersion {
		t.Fatalf("initialize: %+v", r)
	}
	if si := r.Result["serverInfo"].(map[string]any); si["version"] != "test 1.0" {
		t.Fatalf("serverInfo: %v", si)
	}
	if _, ok := r.Result["capabilities"].(map[string]any)["tools"]; !ok {
		t.Fatal("no tools capability")
	}
	c.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	c.send(`{"jsonrpc":"2.0","method":"notifications/whatever"}`)
	if r := c.roundTrip(`{"jsonrpc":"2.0","id":"p","method":"ping"}`); string(r.ID) != `"p"` || r.Error != nil {
		t.Fatalf("ping: %+v", r)
	}
	r = c.roundTrip(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var names []string
	for _, x := range r.Result["tools"].([]any) {
		names = append(names, x.(map[string]any)["name"].(string))
	}
	got := strings.Join(names, ",")
	want := "code_capabilities,code_describe,code_check,code_run,code_docs,text_upper,time_block"
	if got != want {
		t.Fatalf("tools = %s, want %s", got, want)
	}
	// Exposed operation maps back to text.upper.
	r = c.roundTrip(callLine(3, "text_upper", map[string]any{"text": "go"}))
	if sc := r.Result["structuredContent"].(map[string]any); sc["value"] != "GO" {
		t.Fatalf("text_upper: %+v", r)
	}
	r = c.roundTrip(callLine(4, "code_describe", map[string]any{"operation": "text.upper"}))
	if sc := r.Result["structuredContent"].(map[string]any); sc["name"] != "text.upper" {
		t.Fatalf("describe: %+v", r)
	}
}

func TestCodeRun(t *testing.T) {
	c := newClient(t, Options{})
	r := c.roundTrip(callLine(1, "code_run", map[string]any{"program": `const a = await ops.text.upper({text: "x"}); a + "!"`}))
	if r.Error != nil || r.Result["isError"] != nil {
		t.Fatalf("run: %+v", r)
	}
	sc := r.Result["structuredContent"].(map[string]any)
	if sc["ok"] != true || sc["value"] != "X!" {
		t.Fatalf("structured: %v", sc)
	}
	text := r.Result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, `"X!"`) {
		t.Fatalf("text content: %s", text)
	}
}

func TestCodeRunDiagnosticsIsError(t *testing.T) {
	c := newClient(t, Options{})
	r := c.roundTrip(callLine(1, "code_run", map[string]any{"program": `await call("text.uper", {text: "x"})`}))
	if r.Error != nil || r.Result["isError"] != true {
		t.Fatalf("want isError: %+v", r)
	}
	sc := r.Result["structuredContent"].(map[string]any)
	diags := sc["diagnostics"].([]any)
	if len(diags) == 0 || !strings.Contains(diags[0].(map[string]any)["hint"].(string), "text.upper") {
		t.Fatalf("diagnostics: %v", sc)
	}
	r = c.roundTrip(callLine(2, "code_check", map[string]any{"program": `await call("text.uper", {})`}))
	if r.Result["isError"] != true {
		t.Fatalf("check: %+v", r)
	}
}

func TestProtocolErrors(t *testing.T) {
	c := newClient(t, Options{})
	cases := []struct {
		line string
		code int
	}{
		{`{"jsonrpc":"2.0","id":1,"method":"nope"}`, CodeMethodNotFound},
		{`{not json`, CodeParseError},
		{`{"jsonrpc":"1.0","id":2,"method":"ping"}`, CodeInvalidRequest},
		{`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"code_run","arguments":{}}}`, CodeInvalidParams},
		{`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"text_upper","arguments":{}}}`, CodeInvalidParams},
	}
	for _, tc := range cases {
		r := c.roundTrip(tc.line)
		if r.Error == nil || r.Error.Code != tc.code {
			t.Errorf("%s: got %+v, want code %d", tc.line, r, tc.code)
		}
	}
}

func TestCancellation(t *testing.T) {
	c := newClient(t, Options{})
	c.send(callLine(7, "code_run", map[string]any{"program": `await call("time.block", {})`}))
	// The server keeps serving other requests while the run blocks.
	if r := c.roundTrip(`{"jsonrpc":"2.0","id":8,"method":"ping"}`); string(r.ID) != "8" {
		t.Fatalf("ping during run: %+v", r)
	}
	c.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7,"reason":"user"}}`)
	r := c.recv()
	if string(r.ID) != "7" || r.Result["isError"] != true {
		t.Fatalf("cancelled run: %+v", r)
	}
	if sc := r.Result["structuredContent"].(map[string]any); !strings.Contains(sc["error"].(string), "cancel") {
		t.Fatalf("error should mention cancel: %v", sc)
	}
}

func TestServeStopsOnContext(t *testing.T) {
	reg := codemode.NewRegistry("x")
	ctx, cancel := context.WithCancel(context.Background())
	inR, inW := io.Pipe()
	defer inW.Close()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, codemode.NewEngine(reg), inR, io.Discard, Options{}) }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want ctx error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve ignored ctx")
	}
}
