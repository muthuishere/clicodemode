package codemode

// Conformance tests for docs/SPEC.md. Each test is named after the
// requirement it checks. Requirements covered by deeper unit tests elsewhere
// are listed in specCoverage so nothing in the spec is untested.

import (
	"encoding/json"
	"go/build"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var specCoverage = map[string]string{
	"CAP-5":  "TestCapabilityCache* (cache_test.go)",
	"OP-4":   "TestPolicyDenies",
	"OP-5":   "TestArgumentValidationAndErrors, TestPanickingHandlerDoesNotCrashHost",
	"OP-6":   "TestPromiseAllRunsHostCallsInParallel (js)",
	"LANG-1": "TestLanguage, TestAsync (js)",
	"LANG-3": "TestImportsAndJSONData, TestModules (js)",
	"LANG-6": "TestLanguage/key_order (js)",
	"LIM-1":  "TestBudgetsAndCancellation (js)",
	"LIM-2":  "TestLimitsCannotBeSwallowedOnAsyncPaths (js), TestOperationLimitsCannotBeSwallowed",
	"LIM-3":  "TestCancellationStopsRun, TestExecStreamFailureAndCancel",
	"EXE-2":  "TestExecDeniedByDefault, TestShellWhenAuthorized",
	"EXE-3":  "TestExecEnvIsMinimal, TestExecCwdConfined",
	"EXE-4":  "TestExec",
	"STR-1":  "TestStreams (js)",
	"STR-2":  "TestExecStreamTakeKillsInfiniteProcess, TestExecStreamFailureAndCancel",
	"STR-3":  "TestYAMLParallelKeepsOrderAndFirstError, TestStreamYAML (js)",
	"WF-1":   "workflow validation tests (workflow_test.go)",
	"WF-2":   "workflow DAG/continueOnError tests (workflow_test.go)",
	"WF-3":   "workflow guard tests (workflow_test.go)",
	"WF-4":   "workflow loop tests (workflow_test.go)",
	"WF-6":   "TestWorkflowPlanRefsFromImports",
	"MCP-2":  "adapters/mcp server_test.go",
}

// Every requirement ID in SPEC.md is either tested here or listed above.
func TestSpecEveryRequirementCovered(t *testing.T) {
	b, err := os.ReadFile("../docs/SPEC.md")
	if err != nil {
		t.Fatal(err)
	}
	ids := regexp.MustCompile(`(?m)^\| ([A-Z]+-\d+) \|`).FindAllStringSubmatch(string(b), -1)
	if len(ids) < 40 {
		t.Fatalf("found only %d requirement IDs", len(ids))
	}
	src, _ := os.ReadFile("spec_test.go")
	for _, m := range ids {
		id := m[1]
		testName := "TestSpec_" + strings.ReplaceAll(id, "-", "")
		if _, ok := specCoverage[id]; !ok && !strings.Contains(string(src), "func "+testName+"(") {
			t.Errorf("%s has neither %s nor a specCoverage entry", id, testName)
		}
	}
}

func TestSpec_CLI1(t *testing.T) {
	code, out, _ := cli(t, "", "help")
	for _, sub := range []string{"operations", "describe", "capabilities", "docs", "check", "compile", "run", "eval", "skill", "run-step"} {
		if !strings.Contains(out, " "+sub) {
			t.Errorf("usage lacks %q", sub)
		}
	}
	if code != 0 {
		t.Fatalf("help exit %d", code)
	}
	if c, _, _ := cli(t, ""); c != 0 {
		t.Fatalf("no-arg exit %d", c)
	}
}

func TestSpec_CLI2(t *testing.T) {
	if code, out, _ := cli(t, `1 + 1`, "run", "-"); code != 0 || strings.TrimSpace(out) != "2" {
		t.Fatalf("stdin run: %d %q", code, out)
	}
	code, _, errOut := cli(t, `import x from "./x.js"`, "run", "-")
	if code != 2 || !strings.Contains(errOut, "imports are not available") {
		t.Fatalf("stdin import must fail before running: %d %s", code, errOut)
	}
}

func TestSpec_CLI3(t *testing.T) {
	for _, c := range []struct {
		src  string
		want int
	}{
		{`1`, 0},
		{`throw Error("x")`, 1},
		{`await call("fail.always")`, 1},
		{`call("fail.always"); 1`, 1}, // unhandled rejection
		{`let x = ;`, 2},
		{`await call("no.such", {})`, 2},
	} {
		if code, _, e := cli(t, c.src, "run", "-"); code != c.want {
			t.Errorf("%q: exit %d, want %d (%s)", c.src, code, c.want, e)
		}
	}
	if code, _, _ := cli(t, "", "bogus-subcommand"); code != 2 {
		t.Errorf("usage error exit %d, want 2", code)
	}
}

func TestSpec_CLI4(t *testing.T) {
	_, out, _ := cli(t, `({a: 1})`, "run", "-", "--json")
	var r map[string]any
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("not one JSON object: %q", out)
	}
	for _, k := range []string{"ok", "value", "ms"} {
		if _, ok := r[k]; !ok {
			t.Errorf("missing %q in %s", k, out)
		}
	}
	if _, out, _ := cli(t, `"raw text"`, "run", "-"); out != "raw text\n" {
		t.Errorf("string result not raw: %q", out)
	}
	if _, out, _ := cli(t, `undefined`, "run", "-"); out != "" {
		t.Errorf("undefined result printed %q", out)
	}
}

func TestSpec_CLI5(t *testing.T) {
	_, _, errOut := cli(t, "const a = 1\nawait call(\"health.chek\")", "check", "-")
	if !regexp.MustCompile(`<stdin>:2:12: error: unknown operation "health.chek" \(did you mean "health.check"\?\)`).MatchString(errOut) {
		t.Fatalf("diagnostic format: %q", errOut)
	}
	_, out, _ := cli(t, "let x = ;", "check", "-", "--json")
	var ds []map[string]any
	if json.Unmarshal([]byte(out), &ds) != nil || len(ds) != 1 {
		t.Fatalf("json diags: %q", out)
	}
	for _, k := range []string{"severity", "file", "line", "col", "message"} {
		if _, ok := ds[0][k]; !ok {
			t.Errorf("diagnostic missing %q", k)
		}
	}
}

func TestSpec_CLI6(t *testing.T) {
	if code, out, _ := cli(t, `1`, "check", "-", "--json"); code != 0 || strings.TrimSpace(out) != "[]" {
		t.Fatalf("clean check: %d %q", code, out)
	}
}

func TestSpec_CLI7(t *testing.T) {
	code, _, errOut := cli(t, "", "describe", "math.ad")
	if code != 2 || !strings.Contains(errOut, `did you mean "math.add"`) {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestSpec_CLI8(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "s.js"), []byte(`export function f({input}) { return {got: input.n} }
export function o() { return {outcome: "b"} }`), 0o644)
	mod := filepath.Join(dir, "s.js")
	if code, out, _ := cli(t, "", "run-step", mod+"#f", "--input", `{"n": 7}`); code != 0 || !strings.Contains(out, `"got": 7`) {
		t.Fatalf("run-step: %d %s", code, out)
	}
	if code, _, _ := cli(t, "", "run-step", mod+"#o", "--outcomes", "a,b"); code != 11 {
		t.Fatalf("outcome b must exit 11, got %d", code)
	}
	if code, _, _ := cli(t, "", "run-step", "nohash.js"); code != 2 {
		t.Fatalf("usage error exit %d", code)
	}
}

func TestSpec_CLI9(t *testing.T) {
	_, out, _ := cli(t, `print("side"); 5`, "run", "-")
	if out != "side\n5\n" {
		t.Fatalf("got %q", out)
	}
}

func TestSpec_CAP1(t *testing.T) {
	_, out, _ := cli(t, "", "capabilities", "--json")
	var c map[string]any
	if json.Unmarshal([]byte(out), &c) != nil {
		t.Fatal(out)
	}
	for _, k := range []string{"spec", "version", "fingerprint", "language", "operations"} {
		if _, ok := c[k]; !ok {
			t.Errorf("missing %q", k)
		}
	}
	if c["spec"] != SpecVersion {
		t.Errorf("spec %v", c["spec"])
	}
}

func TestSpec_CAP2(t *testing.T) {
	reg, _ := testRegistry(t)
	f := reg.Capabilities().Fingerprint
	if !regexp.MustCompile(`^sha256:[0-9a-f]{32}$`).MatchString(f) {
		t.Fatalf("format %q", f)
	}
	reg2, _ := testRegistry(t)
	if reg2.Capabilities().Fingerprint != f {
		t.Fatal("not stable across identical registries")
	}
}

func TestSpec_CAP3(t *testing.T) {
	TestRegistryValidation(t)
}

func TestSpec_CAP4(t *testing.T) {
	for _, src := range []string{"class A {}", "new X()", "this.x", "function* g() {}", "switch (1) {}", "delete o.x", "import('x')"} {
		if _, diags := NewEngine(NewRegistry("v")).Compile(src, "p.js"); len(diags) == 0 {
			t.Errorf("unsupported %q accepted", src)
		}
	}
	lang := NewRegistry("v").Capabilities().Language
	if len(lang.Features) == 0 || len(lang.Unsupported) == 0 || len(lang.Globals) == 0 {
		t.Fatal("language info incomplete")
	}
}

func TestSpec_OP1(t *testing.T) {
	_, out, _ := cli(t, `const a = await call("math.add", {a: 1, b: 2}); const b = await ops.math.add({a: 1, b: 2}); a === b && a`, "run", "-")
	if strings.TrimSpace(out) != "3" {
		t.Fatalf("got %q", out)
	}
}

func TestSpec_OP2(t *testing.T) {
	_, _, errOut := cli(t, `await call("math.add", {a: 1})`, "run", "-")
	if !strings.Contains(errOut, "b is required") {
		t.Fatalf("got %s", errOut)
	}
}

func TestSpec_OP3(t *testing.T) {
	reg, calls := testRegistry(t)
	r := NewEngine(reg).Run(t.Context(), `await call("health.check"); await call("x" + ".y")`, "p.js")
	if r.OK || calls.Load() != 1 {
		t.Fatalf("computed unknown op must fail at run time after the first call: %+v calls=%d", r, calls.Load())
	}
}

func TestSpec_LANG2(t *testing.T) {
	_, diags := NewEngine(NewRegistry("v")).Compile("class A {}", "p.js")
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "classes are not supported") {
		t.Fatalf("%v", diags)
	}
}

func TestSpec_LANG4(t *testing.T) {
	if _, out, _ := cli(t, "export default async function () { return 42 }", "run", "-"); strings.TrimSpace(out) != "42" {
		t.Fatalf("default export fn: %q", out)
	}
	if _, out, _ := cli(t, "1\n2\nPromise.resolve(3)", "run", "-"); strings.TrimSpace(out) != "3" {
		t.Fatalf("last expr awaited: %q", out)
	}
}

func TestSpec_LANG5(t *testing.T) {
	_, _, errOut := cli(t, `Promise.reject(Error("lost")); 1`, "run", "-")
	if !strings.Contains(errOut, "unhandled promise rejection") {
		t.Fatalf("%s", errOut)
	}
}

func TestSpec_EXE1(t *testing.T) {
	_, diags := NewEngine(NewRegistry("v")).Compile(`await exec(["rm", "-rf", "/"])`, "p.js")
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "not permitted") {
		t.Fatalf("%v", diags)
	}
}

func TestSpec_WF5(t *testing.T) {
	TestWorkflowYAMLGolden(t)
}

func TestSpec_MCP1(t *testing.T) {
	for _, pkg := range []string{".", "../js"} {
		p, err := build.ImportDir(pkg, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range p.Imports {
			if strings.Contains(imp, "adapters/") {
				t.Errorf("%s imports %s", pkg, imp)
			}
		}
	}
}
