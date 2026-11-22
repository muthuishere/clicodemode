package js

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func run(t *testing.T, src string) (string, string) {
	t.Helper()
	return runCfg(t, src, Config{})
}

// runCfg returns (JSON of result, printed output).
func runCfg(t *testing.T, src string, cfg Config) (string, string) {
	t.Helper()
	prog, err := Parse(src, "test.js")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var out bytes.Buffer
	cfg.Stdout = &out
	it := New(context.Background(), cfg)
	defer it.Close()
	v, err := it.Run(prog, "test.js")
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out.String())
	}
	j, err := jsonStringify(it, v, "")
	if err != nil {
		t.Fatalf("stringify: %v", err)
	}
	return toString(j), out.String()
}

func runErr(t *testing.T, src string, cfg Config) error {
	t.Helper()
	prog, err := Parse(src, "test.js")
	if err != nil {
		return err
	}
	it := New(context.Background(), cfg)
	defer it.Close()
	_, err = it.Run(prog, "test.js")
	return err
}

func TestLanguage(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"arith", `1 + 2 * 3 - 4 / 2`, `5`},
		{"pow right assoc", `2 ** 3 ** 2`, `512`},
		{"string concat", `"a" + 1 + 2`, `"a12"`},
		{"template", "const n = 3; `n=${n + 1}!`", `"n=4!"`},
		{"let/const", `let x = 1; x += 2; const y = x * 2; y`, `6`},
		{"object/array", `const o = {a: 1, b: [1, 2, {c: 3}]}; o.b[2].c + o["a"]`, `4`},
		{"key order", `({b: 1, a: 2, 1: "x"})`, `{"1":"x","b":1,"a":2}`},
		{"if/else", `let r; if (1 > 2) { r = "no" } else if (2 > 1) { r = "yes" } else { r = "?" } r`, `"yes"`},
		{"ternary", `const v = 5; v > 3 ? "big" : "small"`, `"big"`},
		{"for", `let s = 0; for (let i = 0; i < 5; i++) { if (i === 3) continue; s += i } s`, `7`},
		{"for-of break", `let s = 0; for (const x of [1,2,3,4]) { if (x > 2) break; s += x } s`, `3`},
		{"for-in", `const ks = []; for (const k in {a:1,b:2}) ks.push(k); ks`, `["a","b"]`},
		{"while", `let n = 0; while (n < 10) n += 3; n`, `12`},
		{"do-while", `let n = 0; do { n++ } while (n < 0); n`, `1`},
		{"closure counter", `function mk() { let c = 0; return () => ++c } const f = mk(); f(); f(); f()`, `3`},
		{"loop closures capture per-iteration", `const fs = []; for (let i = 0; i < 3; i++) fs.push(() => i); fs.map(f => f())`, `[0,1,2]`},
		{"hoisting", `const r = twice(4); function twice(x) { return x * 2 } r`, `8`},
		{"default+rest params", `function f(a, b = 10, ...r) { return [a, b, r] } f(1)`, `[1,10,[]]`},
		{"destructuring", `const {a, b: {c = 5} = {}, ...rest} = {a: 1, x: 2, y: 3}; const [p, , q = 9] = [7, 8]; [a, c, rest, p, q]`, `[1,5,{"x":2,"y":3},7,9]`},
		{"spread", `const a = [1, 2]; const o = {x: 1}; [...a, 3, {...o, y: 2}]`, `[1,2,3,{"x":1,"y":2}]`},
		{"optional chaining", `const o = {a: null}; [o.a?.b, o.a?.b.c.d, o.f?.(), o?.a]`, `[null,null,null,null]`},
		{"nullish", `[null ?? 1, 0 ?? 1, 0 || 1, "" && 2]`, `[1,0,1,""]`},
		{"typeof", `[typeof 1, typeof "s", typeof null, typeof undefined, typeof nope, typeof (() => 1), typeof []]`, `["number","string","object","undefined","undefined","function","object"]`},
		{"equality", `[1 == "1", 1 === "1", null == undefined, null === undefined, NaN === NaN]`, `[true,false,true,false,false]`},
		{"try/catch/finally", `const log = []; try { log.push("t"); throw Error("boom") } catch (e) { log.push(e.message) } finally { log.push("f") } log`, `["t","boom","f"]`},
		{"catch runtime error", `let m; try { null.x } catch (e) { m = e.name } m`, `"TypeError"`},
		{"finally return wins", `function f() { try { return 1 } finally { return 2 } } f()`, `2`},
		{"throw non-error", `let v; try { throw {code: 7} } catch (e) { v = e.code } v`, `7`},
		{"array methods", `[1,2,3,4].filter(x => x % 2 === 0).map(x => x * 10).reduce((a, b) => a + b, 0)`, `60`},
		{"sort", `[3, 1, 10, 2].sort((a, b) => a - b)`, `[1,2,3,10]`},
		{"sort default is string order", `[3, 1, 10, 2].sort()`, `[1,10,2,3]`},
		{"string methods", `"  Hello World ".trim().toLowerCase().split(" ").join("-")`, `"hello-world"`},
		{"json roundtrip", `JSON.parse(JSON.stringify({z: 1, a: [true, null, "x"]}))`, `{"z":1,"a":[true,null,"x"]}`},
		{"json indent", `JSON.stringify({a: [1]}, null, 2)`, `"{\n  \"a\": [\n    1\n  ]\n}"`},
		{"Object helpers", `Object.entries({a: 1, b: 2}).map(([k, v]) => k + v).concat(Object.keys([5,6]))`, `["a1","b2","0","1"]`},
		{"number formatting", `[0.1 + 0.2, 1/3, 1e21, -0, 100, (3.14159).toFixed(2)]`, `[0.30000000000000004,0.3333333333333333,1e+21,0,100,"3.14"]`},
		{"asi", "let a = 1\nlet b = 2\na + b", `3`},
		{"method shorthand", `const o = { add(a, b) { return a + b } }; o.add(2, 3)`, `5`},
		{"comma + update", `let i = 0, j = 10; i++, j--; [i, j]`, `[1,9]`},
		{"labels-free nested break", `let n = 0; for (const a of [1,2]) { for (const b of [1,2,3]) { if (b === 2) break; n++ } } n`, `2`},
		{"logical assign", `let a = null; a ??= 5; let b = 1; b &&= 7; [a, b]`, `[5,7]`},
		{"parseInt/Float", `[parseInt("42px"), parseInt("ff", 16), parseFloat("3.5e1x"), Number("  12 "), Number("x")]`, `[42,255,35,12,null]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := run(t, c.src)
			if got != c.want {
				t.Fatalf("\nsrc:  %s\ngot:  %s\nwant: %s", c.src, got, c.want)
			}
		})
	}
}

func TestAsync(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"await value", `const f = async () => 41; (await f()) + 1`, `42`},
		{"sync until first await", `const log = []; async function f() { log.push("a"); await null; log.push("c") } const p = f(); log.push("b"); await p; log`, `["a","b","c"]`},
		{"Promise.all order", `await Promise.all([sleep(20).then(() => "slow"), Promise.resolve("fast"), 3])`, `["slow","fast",3]`},
		{"Promise.race", `await Promise.race([sleep(50).then(() => "slow"), sleep(1).then(() => "fast")])`, `"fast"`},
		{"reject + catch", `let m; try { await Promise.reject(Error("no")) } catch (e) { m = e.message } m`, `"no"`},
		{"async throw rejects", `async function f() { throw Error("x") } await f().catch(e => "caught " + e.message)`, `"caught x"`},
		{"then chain", `await Promise.resolve(1).then(x => x + 1).then(x => Promise.resolve(x * 10))`, `20`},
		{"finally passes value", `let ran = false; const v = await Promise.resolve(5).finally(() => { ran = true }); [v, ran]`, `[5,true]`},
		{"allSettled", `await Promise.allSettled([Promise.resolve(1), Promise.reject("e")])`, `[{"status":"fulfilled","value":1},{"status":"rejected","reason":"e"}]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := run(t, c.src)
			if got != c.want {
				t.Fatalf("\nsrc:  %s\ngot:  %s\nwant: %s", c.src, got, c.want)
			}
		})
	}
}

func TestPromiseAllRunsHostCallsInParallel(t *testing.T) {
	var inFlight, peak atomic.Int32
	cfg := Config{Limits: Limits{MaxConcurrency: 4}}
	prog, _ := Parse(`await Promise.all([1,2,3,4,5,6,7,8].map(i => work(i)))`, "p.js")
	it := New(context.Background(), cfg)
	defer it.Close()
	it.Define("work", bi("work", func(r *Runner, _ Value, a []Value) (Value, error) {
		n := a[0]
		return r.it.HostPromise(func(ctx context.Context) (Value, error) {
			c := inFlight.Add(1)
			for {
				p := peak.Load()
				if c <= p || peak.CompareAndSwap(p, c) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			inFlight.Add(-1)
			return n, nil
		}), nil
	}))
	start := time.Now()
	v, err := it.Run(prog, "p.js")
	if err != nil {
		t.Fatal(err)
	}
	if got := Inspect(v); got != "[1, 2, 3, 4, 5, 6, 7, 8]" {
		t.Fatalf("got %s", got)
	}
	if p := peak.Load(); p != 4 {
		t.Fatalf("peak concurrency = %d, want 4 (bounded)", p)
	}
	if el := time.Since(start); el > 200*time.Millisecond {
		t.Fatalf("took %v; calls did not overlap", el)
	}
}

func TestBudgetsAndCancellation(t *testing.T) {
	err := runErr(t, `while (true) {}`, Config{Limits: Limits{MaxSteps: 10_000}})
	if err == nil || !strings.Contains(err.Error(), "step budget exceeded") {
		t.Fatalf("want budget error, got %v", err)
	}
	// budget errors are not catchable
	err = runErr(t, `try { while (true) {} } catch (e) { "swallowed" }`, Config{Limits: Limits{MaxSteps: 10_000}})
	if err == nil || !strings.Contains(err.Error(), "step budget exceeded") {
		t.Fatalf("budget must not be catchable, got %v", err)
	}
	err = runErr(t, `function f() { return f() } f()`, Config{})
	if err == nil || !strings.Contains(err.Error(), "maximum call depth") {
		t.Fatalf("want depth error, got %v", err)
	}
	start := time.Now()
	err = runErr(t, `await sleep(10000)`, Config{Limits: Limits{Timeout: 50 * time.Millisecond}})
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("timeout did not stop the run: %v after %v", err, time.Since(start))
	}
}

func TestDiagnostics(t *testing.T) {
	cases := []struct{ src, want string }{
		{"let x = ;", "test.js:1:9: syntax error: unexpected \";\""},
		{"const a = 1\nconst b", "test.js:2:7: syntax error: `const` declaration needs an initial value"},
		{"class A {}", "test.js:1:1: syntax error: classes are not supported"},
		{"new Date()", "test.js:1:1: syntax error: `new` is not supported"},
		{"import('x')", "test.js:1:1: syntax error: dynamic import() is not supported"},
		{"function f() { await 1 }", "test.js:1:16: syntax error: `await` is only valid in async functions"},
		{"if (x {", "test.js:1:7: syntax error: expected \")\""},
		{"\"abc", "test.js:1:1: syntax error: unterminated string"},
		{"return 1", "test.js:1:1: syntax error: `return` outside a function"},
	}
	for _, c := range cases {
		_, err := Parse(c.src, "test.js")
		if err == nil || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("src %q:\n got  %v\n want prefix %s", c.src, err, c.want)
		}
	}
	// runtime errors carry positions
	err := runErr(t, "const o = {}\no.missing.x", Config{})
	if err == nil || !strings.HasPrefix(err.Error(), "test.js:2:10: uncaught TypeError: cannot read properties of undefined") {
		t.Fatalf("got %v", err)
	}
	err = runErr(t, "const c = 1\nc = 2", Config{})
	if err == nil || !strings.Contains(err.Error(), "assignment to constant") {
		t.Fatalf("got %v", err)
	}
	err = runErr(t, "nope()", Config{})
	if err == nil || !strings.Contains(err.Error(), "nope is not defined") {
		t.Fatalf("got %v", err)
	}
}

func TestModules(t *testing.T) {
	files := map[string]string{
		"steps/health.js": `
			const base = 10
			export function inspect(x) { return {ok: x > base, x} }
			export const report = (r) => "report:" + r.x
			export default "dflt"`,
		"cycle/a.js": `import { b } from "./b.js"; export const a = 1`,
		"cycle/b.js": `import { a } from "./a.js"; export const b = 2`,
	}
	loader := func(from, spec string) (string, string, error) {
		dir := ""
		if i := strings.LastIndex(from, "/"); i >= 0 {
			dir = from[:i+1]
		}
		p := strings.TrimPrefix(spec, "./")
		p = dir + p
		src, ok := files[p]
		if !ok {
			return "", "", errors.New("not found")
		}
		return src, p, nil
	}
	got, _ := runCfg(t, `
		import dflt, { inspect, report as rep } from "./steps/health.js"
		import * as h from "./steps/health.js"
		const r = inspect(42)
		;[dflt, r.ok, rep(r), typeof h.inspect]`, Config{Loader: loader})
	if got != `["dflt",true,"report:42","function"]` {
		t.Fatalf("got %s", got)
	}
	err := runErr(t, `import { a } from "./cycle/a.js"`, Config{Loader: loader})
	if err == nil || !strings.Contains(err.Error(), "import cycle") {
		t.Fatalf("want cycle error, got %v", err)
	}
	err = runErr(t, `import { nope } from "./steps/health.js"`, Config{Loader: loader})
	if err == nil || !strings.Contains(err.Error(), `does not export "nope"`) {
		t.Fatalf("got %v", err)
	}
	err = runErr(t, `import { x } from "./x.js"`, Config{})
	if err == nil || !strings.Contains(err.Error(), "does not allow imports") {
		t.Fatalf("got %v", err)
	}
}

func TestPrint(t *testing.T) {
	_, out := run(t, `print("hi", 1, {a: [1, "x"]}); console.log(null)`)
	if out != "hi 1 { a: [1, \"x\"] }\nnull\n" {
		t.Fatalf("got %q", out)
	}
}

func TestExpression(t *testing.T) {
	x, err := ParseExpression(`[1, 2].map(n => n * 2)`, "eval")
	if err != nil {
		t.Fatal(err)
	}
	it := New(context.Background(), Config{})
	v, err := it.Eval(x, "eval")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(ToGo(v)) != "[2 4]" {
		t.Fatalf("got %v", ToGo(v))
	}
}

func TestLimitsCannotBeSwallowedOnAsyncPaths(t *testing.T) {
	cases := []struct {
		name, src string
		cfg       Config
	}{
		{"budget inside async callee", `try { await (async () => { while (true) {} })() } catch (e) {} "done"`, Config{Limits: Limits{MaxSteps: 5_000}}},
		{"timeout during sleep", `try { await sleep(10000) } catch (e) {} "done"`, Config{Limits: Limits{Timeout: 50 * time.Millisecond}}},
		{"timeout via .catch", `await sleep(10000).catch(() => "swallowed"); "done"`, Config{Limits: Limits{Timeout: 50 * time.Millisecond}}},
		{"top-level expression loop", `[1,2,3].map(() => 1); [1,2,3].map(() => 1); "done"`, Config{Limits: Limits{MaxSteps: 3}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := runErr(t, c.src, c.cfg); err == nil {
				t.Fatal("run succeeded; the limit was swallowed")
			}
		})
	}
}

func TestUnhandledRejectionFailsRun(t *testing.T) {
	err := runErr(t, `Promise.reject(Error("lost")); 1`, Config{})
	if err == nil || !strings.Contains(err.Error(), "unhandled promise rejection: Error: lost") {
		t.Fatalf("got %v", err)
	}
	if err := runErr(t, `const p = Promise.reject(Error("x")); try { await p } catch (e) {} 1`, Config{}); err != nil {
		t.Fatalf("handled rejection reported: %v", err)
	}
	if err := runErr(t, `Promise.reject(1).catch(() => 0); 1`, Config{}); err != nil {
		t.Fatalf(".catch should handle: %v", err)
	}
}

func TestAwaitInTemplateNeedsAsync(t *testing.T) {
	if _, err := Parse("function f() { return `${await 1}` }", "t.js"); err == nil {
		t.Fatal("await inside a template in a non-async function must be a syntax error")
	}
	if _, err := Parse("async function f() { return `${await 1}` }", "t.js"); err != nil {
		t.Fatal(err)
	}
}
