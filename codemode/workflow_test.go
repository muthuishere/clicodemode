package codemode

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/clicodemode/js"
)

// runWF writes files into a temp dir and runs main.js through the engine.
func runWF(t *testing.T, files map[string]string, limits js.Limits) Result {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, _ := testRegistry(t)
	e := NewEngine(reg)
	e.WorkflowCLI = "democli"
	e.Policy.Limits = limits
	main := filepath.Join(dir, "main.js")
	return e.Run(context.Background(), files["main.js"], main)
}

func mustOK(t *testing.T, r Result) map[string]any {
	t.Helper()
	if !r.OK {
		t.Fatalf("run failed: %s", r.Error)
	}
	m, ok := r.Value.(map[string]any)
	if !ok {
		t.Fatalf("value is %T: %v", r.Value, r.Value)
	}
	return m
}

func stepOf(t *testing.T, res map[string]any, id string) map[string]any {
	t.Helper()
	s, ok := res["steps"].(map[string]any)[id].(map[string]any)
	if !ok {
		t.Fatalf("no step %q in %v", id, res)
	}
	return s
}

func wantErr(t *testing.T, r Result, sub string) {
	t.Helper()
	if r.OK || !strings.Contains(r.Error, sub) {
		t.Fatalf("want error containing %q, got ok=%v err=%q", sub, r.OK, r.Error)
	}
}

func TestWorkflowSequentialWithoutNeeds(t *testing.T) {
	res := mustOK(t, runWF(t, map[string]string{"main.js": `
const order = []
const mk = id => async ({steps}) => { await sleep(id === "a" ? 30 : 1); order.push(id); return Object.keys(steps).length }
const r = await workflow("seq", [step("a", mk("a")), step("b", mk("b")), step("c", mk("c"))]).run()
export default {r, order}
`}, js.Limits{}))
	if got := strings.Join(toStrings(res["order"]), ","); got != "a,b,c" {
		t.Fatalf("order %s", got)
	}
	r := res["r"].(map[string]any)
	if r["ok"] != true || stepOf(t, r, "c")["output"] != float64(2) {
		t.Fatalf("result %v", r)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestWorkflowDAGOrderAndParallelism(t *testing.T) {
	res := mustOK(t, runWF(t, map[string]string{"main.js": `
const order = []
let active = 0, peak = 0
const mk = id => async ({steps, input}) => {
  active++; peak = Math.max(peak, active)
  await sleep(20)
  active--; order.push(id)
  return {id, seen: Object.keys(steps).sort(), n: input.n}
}
const w = workflow("dag", [
  step("d", mk("d"), {needs: ["b", "c"]}),
  step("a", mk("a")),
  step("b", mk("b"), {needs: ["a"]}),
  step("c", mk("c"), {needs: ["a"]}),
  step("x1", mk("x1")), step("x2", mk("x2")), step("x3", mk("x3")), step("x4", mk("x4")),
], {maxParallel: 3})
const r = await w.run({n: 7})
export default {r, order, peak}
`}, js.Limits{}))
	order := toStrings(res["order"])
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	if !(pos["a"] < pos["b"] && pos["a"] < pos["c"] && pos["b"] < pos["d"] && pos["c"] < pos["d"]) {
		t.Fatalf("bad order %v", order)
	}
	if p := res["peak"].(float64); p > 3 || p < 2 {
		t.Fatalf("peak %v, want 3 (maxParallel)", res["peak"])
	}
	d := stepOf(t, res["r"].(map[string]any), "d")["output"].(map[string]any)
	if seen := "," + strings.Join(toStrings(d["seen"]), ",") + ","; !strings.Contains(seen, ",a,b,c,") || d["n"] != float64(7) {
		t.Fatalf("d saw %v", d)
	}
}

func TestWorkflowValidation(t *testing.T) {
	cases := map[string]string{
		`workflow("w", [step("a", () => 1, {needs: ["a"]})])`:                                                                         "needs itself",
		`workflow("w", [step("a", () => 1, {needs: ["zz"]})])`:                                                                        `needs unknown step "zz"`,
		`workflow("w", [step("a", () => 1, {needs: ["b"]}), step("b", () => 1, {needs: ["a"]})])`:                                     "dependency cycle a → b → a",
		`workflow("w", [step("a", () => 1, {needs: ["c"]}), step("b", () => 1, {needs: ["a"]}), step("c", () => 1, {needs: ["b"]})])`: "dependency cycle a → c → b → a",
		`workflow("w", [step("a", () => 1), step("a", () => 2)])`:                                                                     "duplicate step id",
		`step("a", () => 1, {while: {path: "a.more", equals: true}})`:                                                                 "while requires maxIterations",
		`step("a", () => 1, {until: {path: "a.done", exists: true}, maxIterations: 1000})`:                                            "maxIterations must be an integer from 1 to 999",
		`step("a", () => 1, {until: {path: "a.done", exists: true}, maxIterations: 0})`:                                               "maxIterations must be an integer from 1 to 999",
		`step("a", () => 1, {foreach: [1], until: {path: "a", exists: true}, maxIterations: 2})`:                                      "exactly one of foreach, while, until",
		`step("a", () => 1, {when: {path: "x"}})`:                                                                                     "exactly one of equals or exists",
		`step("a", () => 1, {retry: {attempts: 0}})`:                                                                                  "retry.attempts",
		`step("a", () => 1, {bogus: 1})`:                                                                                              `unknown option "bogus"`,
	}
	for src, want := range cases {
		wantErr(t, runWF(t, map[string]string{"main.js": src}, js.Limits{}), want)
	}
}

func TestWorkflowGuards(t *testing.T) {
	res := mustOK(t, runWF(t, map[string]string{"main.js": `
const w = workflow("g", [
  step("inspect", () => ({ok: true, okStr: "true", gone: null, "a.b": 1})),
  step("inspect.deep", () => ({v: 2})),
  step("strTrue", () => "ran", {needs: ["inspect"], when: {path: "Steps.inspect.okStr", equals: true}}),
  step("boolTrue", () => "ran", {needs: ["inspect"], when: {path: "Steps.inspect.ok", equals: true}}),
  step("lower", () => "ran", {needs: ["inspect"], when: {path: "steps.inspect.okStr", equals: "true"}}),
  step("bare", () => "ran", {needs: ["inspect"], when: {path: "inspect.ok", equals: true}}),
  step("nullExists", () => "ran", {needs: ["inspect"], when: {path: "Steps.inspect.gone", exists: true}}),
  step("missingExists", () => "ran", {needs: ["inspect"], when: {path: "Steps.inspect.nope", exists: true}}),
  step("missingNot", () => "ran", {needs: ["inspect"], when: {path: "Steps.inspect.nope", exists: false}}),
  step("unresolved", () => "ran", {needs: ["inspect"], when: {path: "Steps.inspect.nope", equals: null}}),
  step("longest", () => "ran", {needs: ["inspect.deep"], when: {path: "Steps.inspect.deep.v", equals: 2}}),
  step("inputG", () => "ran", {needs: ["inspect"], when: [{path: "input.env", equals: "prod"}, {path: "Steps.inspect.ok", equals: true}]}),
  step("inputNo", () => "ran", {needs: ["inspect"], when: [{path: "input.env", equals: "prod"}, {path: "Steps.inspect.ok", equals: false}]}),
])
export default await w.run({env: "prod"})
`}, js.Limits{}))
	want := map[string]string{
		"strTrue": "skipped", "boolTrue": "done", "lower": "done", "bare": "done",
		"nullExists": "done", "missingExists": "skipped", "missingNot": "done",
		"unresolved": "skipped", "longest": "done", "inputG": "done", "inputNo": "skipped",
	}
	for id, st := range want {
		if got := stepOf(t, res, id)["status"]; got != st {
			t.Errorf("%s: status %v, want %s (%v)", id, got, st, stepOf(t, res, id))
		}
	}
	if r := stepOf(t, res, "strTrue")["skipReason"].(string); !strings.Contains(r, `is "true", not true`) {
		t.Errorf("skipReason %q", r)
	}
	if r := stepOf(t, res, "unresolved")["skipReason"].(string); !strings.Contains(r, "did not resolve") {
		t.Errorf("skipReason %q", r)
	}
}

func TestWorkflowFailureStopsDependents(t *testing.T) {
	res := mustOK(t, runWF(t, map[string]string{"main.js": `
const boom = () => { throw Error("boom") }
const w = workflow("f", [
  step("a", boom),
  step("b", () => 1, {needs: ["a"]}),
  step("c", () => 2, {needs: ["b"]}),
  step("soft", async () => { throw Error("meh") }, {continueOnError: true}),
  step("afterSoft", () => 3, {needs: ["soft"]}),
])
export default await w.run()
`}, js.Limits{}))
	if res["ok"] != false {
		t.Fatal("want ok=false")
	}
	if s := stepOf(t, res, "a"); s["status"] != "failed" || !strings.Contains(s["error"].(string), "boom") {
		t.Fatalf("a %v", s)
	}
	for _, id := range []string{"b", "c"} {
		if s := stepOf(t, res, id); s["status"] != "skipped" || !strings.Contains(s["skipReason"].(string), "failed") {
			t.Fatalf("%s %v", id, s)
		}
	}
	if s := stepOf(t, res, "afterSoft"); s["status"] != "done" {
		t.Fatalf("afterSoft %v", s)
	}
	// with only continueOnError failures the run is ok
	res = mustOK(t, runWF(t, map[string]string{"main.js": `
export default await workflow("f", [step("a", () => { throw Error("x") }, {continueOnError: true}), step("b", () => 1)]).run()
`}, js.Limits{}))
	if res["ok"] != true || stepOf(t, res, "b")["status"] != "done" {
		t.Fatalf("%v", res)
	}
}

func TestWorkflowRetryAndTimeout(t *testing.T) {
	res := mustOK(t, runWF(t, map[string]string{"main.js": `
let n = 0
const flaky = async () => { n++; if (n < 3) throw Error("try " + n); return n }
const slow = async () => { await sleep(500); return 1 }
const w = workflow("r", [
  step("flaky", flaky, {retry: {attempts: 3, backoff: 5}}),
  step("never", () => { throw Error("no") }, {retry: {attempts: 2, backoff: "1ms"}, continueOnError: true}),
  step("slow", slow, {timeout: "20ms", continueOnError: true}),
])
export default await w.run()
`}, js.Limits{}))
	if s := stepOf(t, res, "flaky"); s["status"] != "done" || s["attempts"] != float64(3) || s["output"] != float64(3) {
		t.Fatalf("flaky %v", s)
	}
	if s := stepOf(t, res, "never"); s["status"] != "failed" || s["attempts"] != float64(2) {
		t.Fatalf("never %v", s)
	}
	if s := stepOf(t, res, "slow"); s["status"] != "failed" || !strings.Contains(s["error"].(string), "timed out") {
		t.Fatalf("slow %v", s)
	}
}

func TestWorkflowBackoffRespectsCancellation(t *testing.T) {
	start := time.Now()
	r := runWF(t, map[string]string{"main.js": `
export default await workflow("r", [step("x", () => { throw Error("no") }, {retry: {attempts: 5, backoff: "10s"}})]).run()
`}, js.Limits{Timeout: 150 * time.Millisecond})
	if r.OK {
		t.Fatalf("want cancellation, got %v", r.Value)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("backoff ignored cancellation: %v", el)
	}
}

func TestWorkflowLoops(t *testing.T) {
	res := mustOK(t, runWF(t, map[string]string{"main.js": `
let active = 0, peak = 0
const each = async ({item, index}) => { active++; peak = Math.max(peak, active); await sleep(15); active--; return item * 10 + index }
const poll = ({steps, index}) => ({n: (steps.poll ? steps.poll.n : 0) + 1, done: index === 2})
const grow = ({steps}) => ({more: !(steps.grow && steps.grow.n >= 3), n: (steps.grow ? steps.grow.n : 0) + 1})
const w = workflow("loops", [
  step("each", each, {foreach: [1, 2, 3, 4, 5], parallel: 2}),
  step("poll", poll, {until: {path: "Steps.poll.done", equals: true}, maxIterations: 10}),
  step("grow", grow, {while: {path: "grow.more", equals: true}, maxIterations: 10}),
  step("capped", () => ({done: false}), {until: {path: "Steps.capped.done", equals: true}, maxIterations: 4, continueOnError: true}),
])
const r = await w.run()
export default {r, peak}
`}, js.Limits{}))
	if p := res["peak"].(float64); p != 2 {
		t.Fatalf("foreach peak %v, want 2", res["peak"])
	}
	r := res["r"].(map[string]any)
	each := stepOf(t, r, "each")["output"].(map[string]any)
	if each["count"] != float64(5) || each["last"] != float64(54) {
		t.Fatalf("each %v", each)
	}
	poll := stepOf(t, r, "poll")["output"].(map[string]any)
	if poll["count"] != float64(3) || poll["last"].(map[string]any)["n"] != float64(3) {
		t.Fatalf("poll %v", poll)
	}
	grow := stepOf(t, r, "grow")["output"].(map[string]any)
	if grow["count"] != float64(4) {
		t.Fatalf("grow %v", grow)
	}
	if s := stepOf(t, r, "capped"); s["status"] != "failed" || s["attempts"] != float64(4) || !strings.Contains(s["error"].(string), "maxIterations 4") {
		t.Fatalf("capped %v", s)
	}
}

var planFiles = map[string]string{
	"steps/health.js": `
export async function inspect({input}) { return {ok: true, host: input.host} }
export const report = async ({steps}) => ({sent: steps.inspect.ok})
export function each({item}) { return item }
`,
	"lib.js": `export default function poll() { return {outcome: "ready"} }`,
	"main.js": `
import { inspect, report, each } from "./steps/health.js"
import poll from "./lib.js"
const w = workflow("health-check", [
  step("inspect", inspect, { retry: { attempts: 3, backoff: "2s" }, timeout: 30000 }),
  step("report", report, { needs: ["inspect"], when: [{ path: "Steps.inspect.ok", equals: true }, { path: "input.host", exists: true }], continueOnError: true }),
  step("each", each, { needs: ["inspect"], foreach: ["a", "b"], parallel: 2 }),
  step("poll", poll, { needs: ["report"], outcomes: ["ready", "waiting"], until: { path: "Steps.poll.outcome", equals: "ready" }, maxIterations: 5 }),
])
export default { plan: w.plan(), yaml: w.toYAML(), run: await w.run({host: "h1"}) }
`,
}

func TestWorkflowPlanRefsFromImports(t *testing.T) {
	res := mustOK(t, runWF(t, planFiles, js.Limits{}))
	steps := res["plan"].(map[string]any)["steps"].([]any)
	want := []string{"./steps/health.js#inspect", "./steps/health.js#report", "./steps/health.js#each", "./lib.js#default"}
	for i, w := range want {
		if got := steps[i].(map[string]any)["ref"]; got != w {
			t.Errorf("step %d ref %v, want %s", i, got, w)
		}
	}
	if stepOf(t, res["run"].(map[string]any), "report")["output"].(map[string]any)["sent"] != true {
		t.Fatalf("run %v", res["run"])
	}
	wantErr(t, runWF(t, map[string]string{"main.js": `workflow("w", [step("a", () => 1)]).plan()`}, js.Limits{}),
		"declarative plans need step functions imported from a module")
	wantErr(t, runWF(t, map[string]string{"main.js": `workflow("w", [step("a", () => 1)]).toYAML()`}, js.Limits{}),
		"declarative plans need step functions imported from a module")
	wantErr(t, runWF(t, map[string]string{"lib.js": planFiles["lib.js"], "main.js": `import poll from "./lib.js"
workflow("w", [step("a", poll, {foreach: []})]).toYAML()`}, js.Limits{}), "non-empty foreach")
	wantErr(t, runWF(t, map[string]string{"lib.js": planFiles["lib.js"], "main.js": `import poll from "./lib.js"
workflow("w", [step("a", poll, {until: {path: "input.x", exists: true}, maxIterations: 3})]).toYAML()`}, js.Limits{}), "must read their own output")
	// WFNexus run steps only expose {ok, exitCode, stdout, stderr, outcome}:
	// guards on JS return fields must be refused at export time.
	wantErr(t, runWF(t, map[string]string{"lib.js": planFiles["lib.js"], "main.js": `import poll from "./lib.js"
workflow("w", [step("a", poll), step("b", poll, {needs: ["a"], when: {path: "Steps.a.ready", equals: true}})]).toYAML()`}, js.Limits{}), "cannot work in WFNexus")
	wantErr(t, runWF(t, map[string]string{"lib.js": planFiles["lib.js"], "main.js": `import poll from "./lib.js"
workflow("w", [step("a", poll), step("b", poll, {needs: ["a"], when: {path: "Steps.a.outcome", equals: "ready"}})]).toYAML()`}, js.Limits{}), "declares no outcomes")
	if _, err := runWFYAML(t, `import poll from "./lib.js"
workflow("w", [step("a", poll), step("b", poll, {needs: ["a"], when: {path: "Steps.a.ok", equals: true}})]).toYAML()`); err != nil {
		t.Fatalf("<id>.ok guards are valid in WFNexus: %v", err)
	}
	// in-process, a declared outcome is enforced
	res2 := mustOK(t, runWF(t, map[string]string{"main.js": `export default await workflow("w", [step("a", () => ({outcome: "nope"}), {outcomes: ["ready"]})]).run()`}, js.Limits{}))
	if s := stepOf(t, res2, "a"); s["status"] != "failed" || !strings.Contains(s["error"].(string), "must return {outcome: one of ready}") {
		t.Fatalf("outcome not enforced: %v", s)
	}
}

func runWFYAML(t *testing.T, main string) (any, error) {
	t.Helper()
	r := runWF(t, map[string]string{"lib.js": planFiles["lib.js"], "main.js": main}, js.Limits{})
	if !r.OK {
		return nil, errors.New(r.Error)
	}
	return r.Value, nil
}

const goldenYAML = `name: health-check
steps:
  - id: inspect
    retry:
      max_attempts: 3
      backoff_sec: 2
    timeout_sec: 30
    run: democli code run-step ./steps/health.js#inspect
  - id: report
    needs: [inspect]
    when:
      - path: inspect.ok
        equals: true
      - path: input.host
        exists: true
    continue-on-error: true
    run: democli code run-step ./steps/health.js#report
  - id: each
    needs: [inspect]
    foreach: ["a","b"]
    parallel: 2
    run: democli code run-step ./steps/health.js#each
  - id: poll
    needs: [report]
    until:
      - path: poll.outcome
        equals: "ready"
    max_iterations: 5
    exit-codes:
      10: ready
      11: waiting
    run: "democli code run-step ./lib.js#default --outcomes ready,waiting"
`

func TestWorkflowYAMLGolden(t *testing.T) {
	res := mustOK(t, runWF(t, planFiles, js.Limits{}))
	if got := res["yaml"].(string); got != goldenYAML {
		t.Fatalf("yaml mismatch:\n--- got\n%s\n--- want\n%s", got, goldenYAML)
	}
}

// WFNexus treats an undeclared exit 0 as "succeeded, no outcome" and loops
// again, so run-step --outcomes must never exit 0.
func TestRunStepOutcomesNeverExitZero(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "s.js"), []byte(`
export function ready() { return {outcome: "ready"} }
export function waiting() { return {outcome: "waiting"} }
export function none() { return {ok: true} }
export function boom() { throw Error("x") }`), 0o644)
	reg, _ := testRegistry(t)
	for export, want := range map[string]int{"ready": 10, "waiting": 11, "none": 1, "boom": 1} {
		var out, errb bytes.Buffer
		c := &CLI{Engine: NewEngine(reg), Name: "t code", Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}
		got := c.Main(context.Background(), []string{"run-step", filepath.Join(dir, "s.js") + "#" + export, "--outcomes", "ready,waiting"})
		if got != want {
			t.Errorf("%s: exit %d, want %d (%s)", export, got, want, errb.String())
		}
	}
}
