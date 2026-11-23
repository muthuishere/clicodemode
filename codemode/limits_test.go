package codemode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A symlink inside the module root must not let an import read a file
// outside it (reported by tmvp).
func TestImportSymlinkEscapeRejected(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.json")
	if err := os.WriteFile(secret, []byte(`{"token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Symlink(secret, filepath.Join(root, "s.json")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	inside := filepath.Join(root, "real.json")
	os.WriteFile(inside, []byte(`{"ok":true}`), 0o600)
	os.Symlink(inside, filepath.Join(root, "alias.json"))

	reg, _ := testRegistry(t)
	e := NewEngine(reg)
	entry := filepath.Join(root, "main.js")
	r := e.Run(context.Background(), `import s from "./s.json"; s`, entry)
	if r.OK || !strings.Contains(r.Error+diagText(r), "outside the module root") {
		t.Fatalf("symlink escape not rejected: %+v", r)
	}
	r = e.Run(context.Background(), `import a from "./alias.json"; a.ok`, entry)
	if !r.OK || r.Value != true {
		t.Fatalf("symlink inside the root should work: %+v", r)
	}
}

func diagText(r Result) string {
	var b strings.Builder
	for _, d := range r.Diagnostics {
		b.WriteString(d.String())
	}
	return b.String()
}

func TestStringAndArraySizeLimitsAreUncatchable(t *testing.T) {
	reg, _ := testRegistry(t)
	e := NewEngine(reg)
	e.Policy.Limits.MaxStringBytes = 1 << 20
	e.Policy.Limits.MaxArrayLen = 1000
	for _, src := range []string{
		`let s = "ab"; try { for (let i = 0; i < 40; i++) s = s + s } catch (e) {} "survived"`,
		`let s = "ab"; try { for (let i = 0; i < 40; i++) s += s } catch (e) {} "survived"`,
		`try { "x".repeat(1 << 20).repeat(2) } catch (e) {} "survived"`,
		`let s = "abcdefgh"; try { for (let i = 0; i < 20; i++) s = ` + "`${s}${s}`" + ` } catch (e) {} "survived"`,
		`const a = []; try { for (let i = 0; i < 2000; i++) a.push(i) } catch (e) {} "survived"`,
		`let a = [1]; try { for (let i = 0; i < 20; i++) a = [...a, ...a] } catch (e) {} "survived"`,
		`let a = [1]; try { for (let i = 0; i < 20; i++) a = a.concat(a) } catch (e) {} "survived"`,
	} {
		r := e.Run(context.Background(), src, "p.js")
		if r.OK || !strings.Contains(r.Error, "size limit exceeded") {
			t.Errorf("%s: want uncatchable size limit, got %+v", src, r)
		}
	}
	r := e.Run(context.Background(), `"ab".repeat(1000).length`, "p.js")
	if !r.OK || r.Value != 2000.0 {
		t.Fatalf("small strings must work: %+v", r)
	}
}

func TestMaxOpsLimit(t *testing.T) {
	reg, calls := testRegistry(t)
	e := NewEngine(reg)
	e.Policy.Limits.MaxOps = 2
	r := e.Run(context.Background(), `const x = {a: 1, b: 1}; try { for (let i = 0; i < 5; i++) await ops.math.add(x) } catch (e) {} "survived"`, "p.js")
	if r.OK || !strings.Contains(r.Error, "operation call limit exceeded") || calls.Load() > 2 {
		t.Fatalf("want op limit after 2 calls, got %+v calls=%d", r, calls.Load())
	}
}
