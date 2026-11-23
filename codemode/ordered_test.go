package codemode

import (
	"context"
	"encoding/json"
	"testing"
)

// Issue #2: key order (LANG-6) for --input JSON and handler results.

func orderEngine(t *testing.T, ret any) *Engine {
	t.Helper()
	reg := NewRegistry("test")
	err := reg.Register(Operation{
		Name:        "k.get",
		Description: "returns a fixed value",
		SideEffects: "none",
		Handler:     func(context.Context, map[string]any) (any, error) { return ret, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewEngine(reg)
}

func runKeys(t *testing.T, e *Engine, src string) any {
	t.Helper()
	res := e.Run(context.Background(), src, "t.js")
	if !res.OK {
		t.Fatalf("run: %s", res.Error)
	}
	return res.Value
}

func TestInputKeepsDocumentOrder(t *testing.T) {
	e := orderEngine(t, nil)
	in := json.RawMessage(`{"b":1,"a":2,"1":3,"n":{"z":1,"y":2,"0":0}}`)
	res := e.RunWithInput(context.Background(), `Object.keys(input).join() + "|" + Object.keys(input.n).join() + "|" + JSON.stringify(input)`, "t.js", in)
	if !res.OK {
		t.Fatal(res.Error)
	}
	want := `1,b,a,n|0,z,y|{"1":3,"b":1,"a":2,"n":{"0":0,"z":1,"y":2}}`
	if res.Value != want {
		t.Fatalf("got %v want %v", res.Value, want)
	}
	res = e.EvalWithInput(context.Background(), `Object.keys(input).join()`, in)
	if res.Value != "1,b,a,n" {
		t.Fatalf("eval: got %v", res.Value)
	}
	bad := e.RunWithInput(context.Background(), `1`, "t.js", json.RawMessage(`{"a":`))
	if bad.OK {
		t.Fatal("invalid input JSON must be an error")
	}
}

func TestHandlerResultOrder(t *testing.T) {
	const prog = `Object.keys(await ops.k.get()).join()`
	text := `{"b":1,"a":2,"1":3}`
	cases := []struct {
		name string
		ret  any
		want string
	}{
		{"OrderedJSON bytes", OrderedJSON(text), "1,b,a"},
		{"OrderedJSON from string", OrderedJSON([]byte(text)), "1,b,a"},
		{"json.RawMessage", json.RawMessage(text), "1,b,a"},
		{"map is sorted", map[string]any{"b": 1, "a": 2, "1": 3}, "1,a,b"},
		{"struct keeps field order", struct {
			B int `json:"b"`
			A int `json:"a"`
		}{1, 2}, "b,a"},
		{"nested raw in map", map[string]any{"x": json.RawMessage(text)}, "x"},
	}
	for _, c := range cases {
		if got := runKeys(t, orderEngine(t, c.ret), prog); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	if got := runKeys(t, orderEngine(t, map[string]any{"x": json.RawMessage(text)}), `Object.keys((await ops.k.get()).x).join()`); got != "1,b,a" {
		t.Errorf("nested raw: got %v", got)
	}
	if got := runKeys(t, orderEngine(t, OrderedJSON("")), `(await ops.k.get()) === null`); got != true {
		t.Errorf("empty OrderedJSON should be null, got %v", got)
	}
	res := orderEngine(t, OrderedJSON(`{"a":`)).Run(context.Background(), `await ops.k.get()`, "t.js")
	if res.OK {
		t.Error("invalid OrderedJSON must reject")
	}
	b, _ := json.Marshal(OrderedJSON(text))
	if string(b) != text {
		t.Errorf("OrderedJSON marshals as %s", b)
	}
}
