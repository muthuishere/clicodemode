package codemode

import (
	"context"
	"strings"
	"testing"
)

// #6: check must reject literal arguments that run would reject.
func TestCheckValidatesLiteralArgs(t *testing.T) {
	reg, calls := testRegistry(t)
	e := NewEngine(reg)
	cases := map[string]string{
		`await ops.math.add({a: "x", b: 2})`: "a must be number",
		`await ops.health.check({zzz: 3})`:   "zzz",
		`await ops.math.add(42)`:             "arguments must be an object",
		`await call("math.add", {a: 1})`:     "b is required",
	}
	for src, want := range cases {
		_, diags := e.Compile(src, "p.js")
		if len(diags) == 0 || !strings.Contains(diags[0].Message, want) {
			t.Errorf("%s: want %q, got %v", src, want, diags)
		}
	}
	// Non-literal parts are left to run time.
	for _, src := range []string{
		`const a = 1; await ops.math.add({a, b: 2})`,
		`const x = {a: 1, b: 2}; await ops.math.add(x)`,
		`const b = 2; await ops.math.add({a: 1, b})`,
		`const o = {b: 2}; await ops.math.add({a: 1, ...o})`,
		`await ops.math.add({a: 1, b: 2})`,
	} {
		if _, diags := e.Compile(src, "p.js"); len(diags) != 0 {
			t.Errorf("%s: unexpected %v", src, diags)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("check must not run handlers")
	}
}

// #6: ops.a.b-c(...) is a subtraction; check must say so.
func TestCheckRejectsHyphenatedDotPath(t *testing.T) {
	reg, _ := testRegistry(t)
	e := NewEngine(reg)
	_, diags := e.Compile(`await ops.math.add-two({})`, "p.js")
	if len(diags) == 0 || !strings.Contains(diags[0].Message, "subtraction") || !strings.Contains(diags[0].Hint, `ops.math["add-two"]`) {
		t.Fatalf("got %v", diags)
	}
	r := e.Run(context.Background(), `await ops.math.add-two({})`, "p.js")
	if r.OK {
		t.Fatal("must not run")
	}
}
