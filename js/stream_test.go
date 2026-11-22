package js

import (
	"strings"
	"testing"
)

func TestStreams(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"map/filter/toArray", `await stream([1,2,3,4,5]).filter(x => x % 2).map(x => x * 10).toArray()`, `[10,30,50]`},
		{"take ends early", `await stream([1,2,3,4,5]).take(2).toArray()`, `[1,2]`},
		{"take after filter", `await stream([1,2,3,4,5,6]).filter(x => x > 2).take(2).toArray()`, `[3,4]`},
		{"jsonl", `await stream(['{"level":"ERROR","m":1}', '', '{"level":"INFO","m":2}', '{"level":"ERROR","m":3}']).jsonl().filter(x => x.level === "ERROR").map(x => x.m).toArray()`, `[1,3]`},
		{"csv", `await stream(["name,age", "ann,3", "bo,4"]).csv().toArray()`, `[{"name":"ann","age":"3"},{"name":"bo","age":"4"}]`},
		{"reduce", `await stream([1,2,3]).reduce((a, b) => a + b, 0)`, `6`},
		{"count/first", `[await stream([1,2,3]).count(), await stream([7,8]).first()]`, `[3,7]`},
		{"json", `await stream(['{"a":', '[1,2]}']).json()`, `{"a":[1,2]}`},
		{"async map callback", `await stream([1,2]).map(async x => { await sleep(1); return x + 1 }).toArray()`, `[2,3]`},
		{"lazy: nothing runs without terminal", `let n = 0; stream([1,2]).map(x => n++); await sleep(5); n`, `0`},
		{"take(0)", `await stream([1,2]).take(0).toArray()`, `[]`},
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

func TestStreamWriteAndErrors(t *testing.T) {
	got, out := run(t, `await stream([{a: 1}, {b: "x"}]).writeJSONL()`)
	if got != "2" || out != "{\"a\":1}\n{\"b\":\"x\"}\n" {
		t.Fatalf("got %s / %q", got, out)
	}
	err := runErr(t, `await stream(["{bad"]).jsonl().toArray()`, Config{})
	if err == nil || !strings.Contains(err.Error(), "jsonl:") {
		t.Fatalf("got %v", err)
	}
	got, _ = run(t, `let m; try { await stream([1]).map(x => { throw Error("in map") }).toArray() } catch (e) { m = e.message } m`)
	if got != `"in map"` {
		t.Fatalf("got %s", got)
	}
}

func TestStreamYAML(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"multi doc, key order", `await stream(["z: 1", "a: [x, y]", "m: {q: true, b: null}", "---", "kind: Pod", "spec:", "  n: 2.5", "---"]).yaml().toArray()`,
			`[{"z":1,"a":["x","y"],"m":{"q":true,"b":null}},{"kind":"Pod","spec":{"n":2.5}}]`},
		{"leading sep and inline doc", `await stream(["---", "--- hello", "--- [1, 2]", "...", "# only a comment"]).yaml().toArray()`, `["hello",[1,2]]`},
		{"anchors and merge", `await stream(["base: &b {x: 1, y: 2}", "d:", "  <<: *b", "  y: 3"]).yaml().map(d => Object.keys(d.d).join() + "=" + d.d.y).first()`, `"x,y=3"`},
		{"then filter/take", `await stream(["a: 1", "---", "a: 2", "---", "a: 3"]).yaml().filter(d => d.a > 1).take(1).toArray()`, `[{"a":2}]`},
		{"strings stay strings", `await stream(["v: '010'", "t: 2024-01-02", "s: yes"]).yaml().first()`, `{"v":"010","t":"2024-01-02","s":"yes"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := run(t, c.src)
			if got != c.want {
				t.Fatalf("\nsrc:  %s\ngot:  %s\nwant: %s", c.src, got, c.want)
			}
		})
	}
	err := runErr(t, `await stream(["a: [1, 2", "b: 3"]).yaml().toArray()`, Config{})
	if err == nil || !strings.Contains(err.Error(), "yaml:") {
		t.Fatalf("got %v", err)
	}
}
