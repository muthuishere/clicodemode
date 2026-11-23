package js

import (
	"strings"
	"testing"
	"time"
)

type langCase struct{ name, src, want string }

func runCases(t *testing.T, cases []langCase) {
	t.Helper()
	for _, c := range cases {
		if got, _ := run(t, c.src); got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.name, got, c.want)
		}
	}
}

// LANG-7: regex literals and RegExp, run on RE2.
func TestLangRegex(t *testing.T) {
	runCases(t, []langCase{
		{"test", `/ab+c/i.test("xABBCx")`, `true`},
		{"match groups", `const m = "2026-11-24".match(/(\d+)-(\d+)-(\d+)/); [m[0], m[1], m.index]`, `["2026-11-24","2026",0]`},
		{"named groups", `"key=val".match(/(?<k>\w+)=(?<v>\w+)/).groups`, `{"k":"key","v":"val"}`},
		{"match g", `"a1b22c333".match(/\d+/g)`, `["1","22","333"]`},
		{"match none", `"abc".match(/\d/)`, `null`},
		{"matchAll", `[..."a1b2".matchAll(/([a-z])(\d)/g)].map(m => m[1] + m[2] + m.index)`, `["a10","b22"]`},
		{"replace first", `"aaa".replace(/a/, "b")`, `"baa"`},
		{"replace g", `"aaa".replace(/a/g, "b")`, `"bbb"`},
		{"replace groups", `"John Smith".replace(/(\w+)\s(\w+)/, "$2, $1")`, `"Smith, John"`},
		{"replace named", `"2026-11".replace(/(?<y>\d+)-(?<m>\d+)/, "$<m>/$<y>")`, `"11/2026"`},
		{"replace fn", `"a1b2".replace(/\d/g, (d, i) => "[" + d * 2 + "@" + i + "]")`, `"a[2@1]b[4@3]"`},
		{"replaceAll", `"x.y.z".replaceAll(/\./g, "/")`, `"x/y/z"`},
		{"split", `"a, b,c ,d".split(/\s*,\s*/)`, `["a","b","c","d"]`},
		{"split captures", `"a1b2c".split(/(\d)/)`, `["a","1","b","2","c"]`},
		{"search", `"hello world".search(/o\s/)`, `4`},
		{"exec loop", `const re = /\d/g; const out = []; let m; while ((m = re.exec("a1b2")) !== null) out.push(m[0] + re.lastIndex); out`, `["12","24"]`},
		{"new RegExp", `new RegExp("^" + "a.c" + "$").test("abc")`, `true`},
		{"flags", `const r = /x/gim; [r.source, r.flags, r.global, String(r)]`, `["x","gim",true,"/x/gim"]`},
		{"division still works", `const a = 10, b = 2; [a / b, (a) / b, [4][0] / 2]`, `[5,5,2]`},
		{"multiline", `"a\nb".match(/^b$/m)[0]`, `"b"`},
		{"unicode index", `"héllo wörld".search(/w/)`, `6`},
		{"g with word boundary", `"foo bar".replace(/\b\w/g, c => c.toUpperCase())`, `"Foo Bar"`},
		{"g with caret", `"aaa".replace(/^a/g, "b")`, `"baa"`},
		{"exec loop with caret", `const re = /^\w/g; const out = []; let m; while ((m = re.exec("ab")) !== null) out.push(m[0]); out`, `["a"]`},
		{"split with boundary", `"one two".split(/\b/)`, `["one"," ","two"]`},
		{"multiline g", `"a1\nb2".match(/^\w/gm)`, `["a","b"]`},
	})
}

// LANG-8: switch.
func TestLangSwitch(t *testing.T) {
	runCases(t, []langCase{
		{"match", `function f(x) { switch (x) { case 1: return "one"; case "1": return "str"; default: return "other" } } [f(1), f("1"), f(2)]`, `["one","str","other"]`},
		{"fallthrough", `const out = []; switch (2) { case 1: out.push(1); case 2: out.push(2); case 3: out.push(3); break; case 4: out.push(4) } out`, `[2,3]`},
		{"default in middle", `let r; switch (9) { case 1: r = "a"; break; default: r = "d"; case 2: r += "2" } r`, `"d2"`},
		{"break in loop", `let n = 0; for (const x of [1, 2, 3]) { switch (x) { case 2: continue; default: n += x } } n`, `4`},
		{"block scope", `switch (1) { case 1: { const y = 5 } } typeof y`, `"undefined"`},
	})
}

// LANG-9: class, new, this, extends, super, instanceof.
func TestLangClass(t *testing.T) {
	runCases(t, []langCase{
		{"basic", `class P { constructor(x, y) { this.x = x; this.y = y } len() { return Math.hypot(this.x, this.y) } } const p = new P(3, 4); [p.len(), p.x, p instanceof P]`, `[5,3,true]`},
		{"fields", `class C { n = 1; list = []; add(v) { this.list.push(v); this.n++; return this } } const c = new C().add("a").add("b"); [c.n, c.list]`, `[3,["a","b"]]`},
		{"extends super", `class A { constructor(n) { this.n = n } hi() { return "A" + this.n } }
class B extends A { constructor(n) { super(n * 2); this.b = true } hi() { return "B" + super.hi() } }
const b = new B(2); [b.hi(), b.n, b.b, b instanceof A, b instanceof B, new A(1) instanceof B]`, `["BA4",4,true,true,true,false]`},
		{"default ctor passes args", `class A { constructor(v) { this.v = v } } class B extends A {} new B(7).v`, `7`},
		{"static", `class M { static of(x) { return new M(x) } static count = 2; constructor(x) { this.x = x } } [M.of(5).x, M.count, M.name]`, `[5,2,"M"]`},
		{"static inherited", `class A { static k() { return "k" + this.name } } class B extends A {} B.k()`, `"kB"`},
		{"async method", `class S { async get(v) { await null; return v + 1 } } await new S().get(1)`, `2`},
		{"arrow keeps this", `class T { constructor() { this.v = 3 } f() { return [1, 2].map(x => x * this.v) } } new T().f()`, `[3,6]`},
		{"json own fields only", `class U { constructor() { this.a = 1 } m() {} } JSON.stringify(new U())`, `"{\"a\":1}"`},
		{"extends Error", `class MyErr extends Error { constructor(m) { super(m); this.name = "MyErr"; this.code = 7 } }
let r; try { throw new MyErr("bad") } catch (e) { r = [e instanceof MyErr, e instanceof Error, e.message, e.name, e.code, String(e)] } r`, `[true,true,"bad","MyErr",7,"MyErr: bad"]`},
		{"class expression", `const K = class { v() { return 1 } }; new K().v()`, `1`},
		{"typeof class", `class Q {} typeof Q`, `"function"`},
		{"object method this", `const o = { n: 2, twice() { return this.n * 2 } }; o.twice()`, `4`},
		{"function ctor", `function Pt(x) { this.x = x } new Pt(9).x`, `9`},
		{"new Error", `const e = new TypeError("t"); [e instanceof TypeError, e instanceof Error, e instanceof RangeError, e.message]`, `[true,true,false,"t"]`},
		{"new Promise", `await new Promise((res) => res(5))`, `5`},
		{"new Array", `[new Array(3).length, new Array(1, 2)]`, `[3,[1,2]]`},
		{"instanceof builtins", `[[] instanceof Array, ({}) instanceof Object, 1 instanceof Object, Promise.resolve() instanceof Promise]`, `[true,true,false,true]`},
	})
	for _, src := range []string{`class A {} A()`, `const x = 1; new x()`, `class A extends 5 {}`} {
		p, err := Parse(src, "t.js")
		if err != nil {
			t.Fatal(err)
		}
		it := New(t.Context(), Config{})
		if _, err := it.Run(p, "t.js"); err == nil || !strings.Contains(err.Error(), "TypeError") {
			t.Errorf("%s: want TypeError, got %v", src, err)
		}
	}
}

// LANG-10: Map, Set, Date.
func TestLangMapSetDate(t *testing.T) {
	runCases(t, []langCase{
		{"map", `const m = new Map([["a", 1]]); m.set("b", 2).set(NaN, 3); [m.get("a"), m.get(NaN), m.size, m.has("z"), [...m.keys()]]`, `[1,3,3,false,["a","b",null]]`},
		{"map object keys", `const k = {}; const m = new Map(); m.set(k, "v"); [m.get(k), m.get({})]`, `["v",null]`},
		{"map iterate", `const m = new Map([["x", 1], ["y", 2]]); const out = []; for (const [k, v] of m) out.push(k + v); m.delete("x"); [out, m.size]`, `[["x1","y2"],1]`},
		{"map to result", `new Map([["a", 1]])`, `{}`},
		{"set", `const s = new Set([1, 2, 2, 3]); s.add(1).add(4); [s.size, s.has(2), [...s]]`, `[4,true,[1,2,3,4]]`},
		{"set dedupe", `[...new Set("hello")].join("")`, `"helo"`},
		{"date", `const d = new Date("2026-11-24T10:20:30.000Z"); [d.toISOString(), d.getTime(), d.getUTCHours(), d.getUTCMonth(), JSON.stringify({d})]`, `["2026-11-24T10:20:30.000Z",1795515630000,10,10,"{\"d\":\"2026-11-24T10:20:30.000Z\"}"]`},
		{"date math", `const a = new Date(1000), b = new Date(4000); [b - a, a < b, new Date(NaN).getTime()]`, `[3000,true,null]`},
		{"date utc", `Date.UTC(2020, 0, 2)`, `1577923200000`},
		{"date invalid", `String(new Date("nope"))`, `"Invalid Date"`},
	})
	got, _ := run(t, `Date.now()`)
	if n := len(got); n < 13 {
		t.Fatalf("Date.now %s", got)
	}
	_ = time.Now
}

// LANG-11: tagged templates call a tag function the host or program defines.
func TestLangTaggedTemplates(t *testing.T) {
	runCases(t, []langCase{
		{"strings and values", "function tag(s, ...v) { return [s, v, s.raw] } tag`a${1}b${\"x\"}c`", `[["a","b","c"],[1,"x"],["a","b","c"]]`},
		{"quote tag", "const q = (s, ...v) => s.reduce((acc, p, i) => acc + p + (i < v.length ? \"'\" + v[i] + \"'\" : \"\"), \"\"); q`git log ${\"main; rm\"}`", `"git log 'main; rm'"`},
		{"member tag", "const o = { pre: \"#\", t(s, x) { return this.pre + s[0] + x } }; o.t`v=${5}`", `"#v=5"`},
		{"String.raw", "String.raw`a${1}b`", `"a1b"`},
		{"raw keeps escapes", "String.raw`C:\\new\\t${1}\\n`", `"C:\\new\\t1\\n"`},
		{"cooked vs raw", "((s) => [s[0], s.raw[0]])`a\\tb`", `["a\tb","a\\tb"]`},
		{"no subs", "((s) => s.length)`plain`", `1`},
	})
}

// LANG-12: the built-ins agents use.
func TestLangBuiltins(t *testing.T) {
	runCases(t, []langCase{
		{"number statics", `[Number.isInteger(5), Number.isInteger(5.5), Number.isFinite(1 / 0), Number.isNaN(NaN), Number.MAX_SAFE_INTEGER, Number.parseInt("42px")]`, `[true,false,false,true,9007199254740991,42]`},
		{"string statics", `[String.fromCharCode(72, 105), String.fromCodePoint(128512).length]`, `["Hi",2]`},
		{"string methods", `["b".localeCompare("a"), "a-b-a".lastIndexOf("a"), "x".concat(1, 2), "7".padStart(3, "0"), "abc".at(-1)]`, `[1,4,"x12","007","c"]`},
		{"array methods", `const a = [3, 1, 2]; [a.toSorted(), a, a.toReversed(), a.findLast(x => x > 1), a.findLastIndex(x => x > 5), [0, 0].fill(7), a.with(0, 9), [...a.entries()][1]]`, `[[1,2,3],[3,1,2],[2,1,3],2,-1,[7,7],[9,1,2],[1,1]]`},
		{"math", `[Math.log2(8), Math.log10(1000), Math.atan2(0, 1), Math.hypot(3, 4), Math.cbrt(27), typeof Math.random()]`, `[3,3,0,5,3,"number"]`},
		{"array of", `Array.of(1, 2)`, `[1,2]`},
		{"freeze", `"use strict"; const o = Object.freeze({a: 1}); let err = ""; try { o.a = 2 } catch (e) { err = e.name } [o.a, err]`, `[1,"TypeError"]`},
	})
}
