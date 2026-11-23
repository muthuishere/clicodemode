package js

import (
	"math"
	"strings"
	"testing"
)

// Issue #1: Number::toString layout.
func TestNumberToStringTable(t *testing.T) {
	cases := []struct {
		f    float64
		want string
	}{
		{0, "0"}, {math.Copysign(0, -1), "0"},
		{math.NaN(), "NaN"}, {math.Inf(1), "Infinity"}, {math.Inf(-1), "-Infinity"},
		{1, "1"}, {-1, "-1"}, {100, "100"}, {1e20, "100000000000000000000"},
		{1e21, "1e+21"}, {1.5e21, "1.5e+21"}, {-1e21, "-1e+21"},
		{123456789012345680000, "123456789012345680000"},
		{9007199254740992, "9007199254740992"},
		{123456789.5, "123456789.5"}, {1234567.25, "1234567.25"}, {-1234567.25, "-1234567.25"},
		{0.5, "0.5"}, {-1.5, "-1.5"}, {math.Float64frombits(0x3FD3333333333334), "0.30000000000000004"},
		{4.35, "4.35"}, {1.0000000000000002, "1.0000000000000002"},
		{0.000001, "0.000001"}, {0.0000012345, "0.0000012345"}, {0.00001234, "0.00001234"},
		{1e-7, "1e-7"}, {-1e-7, "-1e-7"}, {1.5e-7, "1.5e-7"}, {1.23e-18, "1.23e-18"},
		{9.999999e-7, "9.999999e-7"},
		{1e300, "1e+300"}, {1.7976931348623157e308, "1.7976931348623157e+308"},
		{5e-324, "5e-324"}, {2.2250738585072014e-308, "2.2250738585072014e-308"},
		{999999999999999900000, "999999999999999900000"},
	}
	for _, c := range cases {
		if got := numberToString(c.f); got != c.want {
			t.Errorf("numberToString(%v) = %q, want %q", c.f, got, c.want)
		}
	}
}

func TestNumberToStringInPrograms(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"String", `String(123456789.5)`, `"123456789.5"`},
		{"template", "`${1234567.25}|${0.000001}|${1e-7}|${1e21}`", `"1234567.25|0.000001|1e-7|1e+21"`},
		{"concat", `"" + 0.1 + 0.2 + (-0)`, `"0.10.20"`},
		{"json", `JSON.stringify([123456789.5, 0.000001, 1e-7, 1e21, 5e-324])`, `"[123456789.5,0.000001,1e-7,1e+21,5e-324]"`},
	}
	for _, c := range cases {
		if got, _ := run(t, c.src); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

// Issue #4: strings are indexed in UTF-16 code units.
func TestStringUTF16(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"length astral", `"😀".length`, `2`},
		{"length mixed", `"a😀b".length`, `4`},
		{"length bmp", `"日本".length`, `2`},
		{"length ascii", `"hello".length`, `5`},
		{"index", `["a😀b"[0], "a😀b"[3], "a😀b"[4]].map(x => x === undefined ? "u" : x).join("|")`, `"a|b|u"`},
		{"index surrogate", `"😀"[0].length + "😀"[1].length`, `2`},
		{"slice whole", `"a😀b".slice(1, 3)`, `"😀"`},
		{"slice tail", `"a😀b".slice(3)`, `"b"`},
		{"slice negative", `"a😀b".slice(-1)`, `"b"`},
		{"substring", `"a😀b".substring(3, 1)`, `"😀"`},
		{"slice split pair rejoins", `"😀".slice(0, 1) + "😀".slice(1)`, `"😀"`},
		{"slice halves length", `"😀".slice(0, 1).length`, `1`},
		{"charAt", `"a😀b".charAt(3)`, `"b"`},
		{"charAt oob", `"a😀b".charAt(4)`, `""`},
		{"at", `"a😀b".at(-1) + "a😀b".at(3)`, `"bb"`},
		{"at oob", `"😀".at(2) === undefined`, `true`},
		{"charCodeAt", `["😀".charCodeAt(0), "😀".charCodeAt(1), "a".charCodeAt(0), "日".charCodeAt(0)].join()`, `"55357,56832,97,26085"`},
		{"charCodeAt nan", `const n = "a".charCodeAt(5); n !== n`, `true`},
		{"codePointAt", `["😀".codePointAt(0), "😀".codePointAt(1), "a😀".codePointAt(1), "a".codePointAt(3) === undefined].join()`, `"128512,56832,128512,true"`},
		{"indexOf", `"a😀b".indexOf("b")`, `3`},
		{"indexOf missing", `"a😀b".indexOf("z")`, `-1`},
		{"split empty", `"a😀".split("").length`, `3`},
		{"split empty ascii", `"abc".split("").join("-")`, `"a-b-c"`},
		{"padStart", `"😀".padStart(4, "x")`, `"xx😀"`},
		{"padEnd emoji pad", `"a".padEnd(4, "😀").length`, `4`},
		{"for of code points", `let n = 0; for (const c of "a😀b") n++; n`, `3`},
		{"spread code points", `[..."a😀b"].length`, `3`},
		{"bmp unchanged", `"héllo wörld".slice(1, 4) + "日本語".charAt(1) + "日本語".indexOf("語")`, `"éll本2"`},
	}
	for _, c := range cases {
		if got, _ := run(t, c.src); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

// Issue #5: eval and regex literals are parse errors that name the construct.
func TestUnsupportedEvalAndRegex(t *testing.T) {
	cases := []struct {
		name, src, contains string
		line, col           int
	}{
		{"eval call", `eval("1 + 1")`, "eval", 1, 1},
		{"eval nested", "const x = 1;\nconst y = eval(x)", "eval", 2, 11},
		{"eval bare", `const f = eval`, "eval", 1, 11},
		{"regex const", `const r = /ab+c/`, "regex", 1, 11},
		{"regex arg", `"a".replace(/a/g, "b")`, "regex", 1, 13},
		{"regex assign op start", `let x; x = /=a/`, "regex", 1, 12},
	}
	for _, c := range cases {
		_, err := Parse(c.src, "t.js")
		if err == nil {
			t.Errorf("%s: want parse error", c.name)
			continue
		}
		e, ok := err.(*Error)
		if !ok {
			t.Errorf("%s: error type %T", c.name, err)
			continue
		}
		if !strings.Contains(e.Msg, c.contains) || e.Pos.Line != c.line || e.Pos.Col != c.col {
			t.Errorf("%s: got %d:%d %q, want %d:%d containing %q", c.name, e.Pos.Line, e.Pos.Col, e.Msg, c.line, c.col, c.contains)
		}
	}
	// Division and member names are untouched.
	for _, src := range []string{`10 / 2 / 5`, `let a = 8; a /= 2; a`, `const o = {eval: 1}; o.eval`} {
		if _, err := Parse(src, "t.js"); err != nil {
			t.Errorf("%q: unexpected parse error %v", src, err)
		}
	}
}

// Issue #3: await continuations and .then jobs share one FIFO job queue.
func TestPromiseJobOrderFIFO(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"then before await", `
const log = [];
Promise.resolve().then(() => log.push(1));
(async () => { await null; log.push(2); })();
await Promise.resolve();
await Promise.resolve();
log`, `[1,2]`},
		{"await before then", `
const log = [];
(async () => { await null; log.push(1); })();
Promise.resolve().then(() => log.push(2));
await Promise.resolve();
await Promise.resolve();
log`, `[1,2]`},
		{"await promise before then", `
const log = [];
(async () => { await Promise.resolve(); log.push(1); })();
Promise.resolve().then(() => log.push(2));
await Promise.resolve();
await Promise.resolve();
log`, `[1,2]`},
		{"many interleaved", `
const log = [];
for (let i = 0; i < 20; i++) {
  if (i % 2 === 0) Promise.resolve().then(() => log.push(i));
  else (async () => { await undefined; log.push(i); })();
}
await null; await null; await null;
log.join()`, `"0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19"`},
		{"chained then", `
const log = [];
Promise.resolve().then(() => log.push("a1")).then(() => log.push("a2"));
Promise.resolve().then(() => log.push("b1")).then(() => log.push("b2"));
await null; await null; await null; await null;
log.join()`, `"a1,b1,a2,b2"`},
	}
	for _, c := range cases {
		for i := 0; i < 25; i++ {
			if got, _ := run(t, c.src); got != c.want {
				t.Fatalf("%s (run %d): got %s want %s", c.name, i, got, c.want)
			}
		}
	}
}
