package codemode

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapabilityCache(t *testing.T) {
	reg, _ := testRegistry(t)
	caps := reg.Capabilities()
	c, err := NewFileCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := CapabilityCacheKey(caps.Version, caps.Fingerprint, "token-scope:admin secret-ish")
	if strings.Contains(key, "admin") || strings.ContainsAny(key, "/. ") {
		t.Fatalf("key leaks auth context or is not file-safe: %s", key)
	}
	if key == CapabilityCacheKey(caps.Version, caps.Fingerprint, "other") || key == CapabilityCacheKey("v2", caps.Fingerprint, "token-scope:admin secret-ish") {
		t.Fatal("key must vary with every part")
	}
	if _, ok, err := c.LoadCapabilities(key); ok || err != nil {
		t.Fatalf("want miss, got ok=%v err=%v", ok, err)
	}
	if err := c.SaveCapabilities(key, caps); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.LoadCapabilities(key)
	if !ok || err != nil || got.Fingerprint != caps.Fingerprint || len(got.Operations) != len(caps.Operations) {
		t.Fatalf("bad load: %v %v %+v", ok, err, got)
	}
	files, _ := filepath.Glob(filepath.Join(c.Dir, "capabilities", "*"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if bytes.Contains(b, []byte("secret-ish")) {
			t.Fatal("auth context written to disk")
		}
		if st, _ := os.Stat(f); st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", st.Mode())
		}
	}
	var nilCache *Cache
	if _, ok, _ := nilCache.LoadCapabilities(key); ok {
		t.Fatal("nil cache must miss")
	}
}

func TestCompiledCacheInvalidatesOnImportChange(t *testing.T) {
	reg, _ := testRegistry(t)
	e := NewEngine(reg)
	e.Cache = &Cache{Dir: t.TempDir()}
	dir := t.TempDir()
	main := filepath.Join(dir, "main.js")
	lib := filepath.Join(dir, "lib.js")
	src := `import { f } from "./lib.js"
await f()`
	os.WriteFile(main, []byte(src), 0o644)
	os.WriteFile(lib, []byte(`export async function f() { return await call("health.check", {}) }`), 0o644)

	p1, d1, hit := e.CompilePlan(src, main)
	if hit || hasErrors(d1) {
		t.Fatalf("first compile: hit=%v diags=%v", hit, d1)
	}
	p2, _, hit := e.CompilePlan(src, main)
	if !hit {
		t.Fatal("second compile should hit the cache")
	}
	b1, _ := json.Marshal(p1)
	b2, _ := json.Marshal(p2)
	if string(b1) != string(b2) {
		t.Fatalf("cached plan differs:\n%s\n%s", b1, b2)
	}

	// Change only the import: must miss and see the new operation reference.
	os.WriteFile(lib, []byte(`export async function f() { return await call("math.ad", {a:1,b:2}) }`), 0o644)
	p3, d3, hit := e.CompilePlan(src, main)
	if hit {
		t.Fatal("import change must invalidate the cache")
	}
	if p3.CacheKey == p1.CacheKey || !hasErrors(d3) {
		t.Fatalf("want new key + unknown-op diagnostic, got %v %v", p3.CacheKey, d3)
	}
	// Cached diagnostics are replayed on a hit.
	_, d4, hit := e.CompilePlan(src, main)
	if !hit || len(d4) != len(d3) || d4[0].Message != d3[0].Message {
		t.Fatalf("want cached diagnostics, hit=%v %v", hit, d4)
	}

	// A failed import is never cached.
	bad := `import { g } from "./missing.js"
1`
	if _, _, hit := e.CompilePlan(bad, main); hit {
		t.Fatal("unexpected hit")
	}
	if _, _, hit := e.CompilePlan(bad, main); hit {
		t.Fatal("failed imports must not be cached")
	}

	// Policy that changes diagnostics is part of the key.
	e.Policy.Exec.Programs = []string{"git"}
	if _, _, hit := e.CompilePlan(src, main); hit {
		t.Fatal("exec policy change must miss")
	}
}

func TestCLICacheEnvAndRunStep(t *testing.T) {
	reg, _ := testRegistry(t)
	cacheDir := t.TempDir()
	t.Setenv("CODEMODE_CACHE_DIR", cacheDir)
	dir := t.TempDir()
	prog := filepath.Join(dir, "p.js")
	os.WriteFile(prog, []byte(`import { k } from "./lib.js"
k`), 0o644)
	os.WriteFile(filepath.Join(dir, "lib.js"), []byte(`export const k = 1
export async function step(ctx) { const h = await call("health.check", {}); return { in: ctx.input, ok: h.ok, steps: ctx.steps } }
export default function (ctx) { return ctx.input }
export function boom() { throw Error("bad") }`), 0o644)

	cli := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		c := &CLI{Engine: NewEngine(reg), Name: "t code", Stdout: &out, Stderr: &errb, Stdin: strings.NewReader("")}
		return c.Main(context.Background(), args), out.String(), errb.String()
	}
	for i := 0; i < 2; i++ {
		for _, sub := range []string{"check", "compile", "run"} {
			if code, out, errs := cli(sub, prog); code != 0 {
				t.Fatalf("%s #%d: %d %s %s", sub, i, code, out, errs)
			}
		}
	}
	if files, _ := filepath.Glob(filepath.Join(cacheDir, "programs", "*.json")); len(files) == 0 {
		t.Fatal("CODEMODE_CACHE_DIR did not enable the cache")
	}

	code, out, errs := cli("run-step", filepath.Join(dir, "lib.js")+"#step", "--input", `{"x":[1,2]}`)
	if code != 0 {
		t.Fatalf("run-step: %d %s", code, errs)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || got["ok"] != true || got["steps"] == nil {
		t.Fatalf("bad output %q %v", out, err)
	}
	if code, out, _ := cli("run-step", filepath.Join(dir, "lib.js")+"#default", "--input=5"); code != 0 || strings.TrimSpace(out) != "5" {
		t.Fatalf("default export: %d %q", code, out)
	}
	if code, _, _ := cli("run-step", filepath.Join(dir, "lib.js")+"#boom"); code != 1 {
		t.Fatalf("throwing step should exit 1, got %d", code)
	}
	if code, _, _ := cli("run-step", filepath.Join(dir, "lib.js")+"#missing"); code != 1 {
		t.Fatalf("missing export should exit 1, got %d", code)
	}
	if code, _, _ := cli("run-step", filepath.Join(dir, "lib.js")+"#step", "--input", "{bad"); code != 2 {
		t.Fatal("bad --input is a usage error")
	}
	if code, _, _ := cli("run-step", "lib.js"); code != 2 {
		t.Fatal("missing #export is a usage error")
	}
}

func TestSkillOutput(t *testing.T) {
	reg, _ := testRegistry(t)
	s := Skill(reg, "mycli code")
	for _, want := range []string{"name: mycli-code\n", "description: ", "`health.check(", "await call(\"fail.always\"", "code/SKILL.md"} {
		if !strings.Contains(s, want) {
			t.Errorf("skill missing %q:\n%s", want, s)
		}
	}
	if s := Skill(NewRegistry("x"), "empty code"); !strings.Contains(s, "no operations") {
		t.Errorf("empty registry: %s", s)
	}
}
