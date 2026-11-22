package codemode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func execEngine(t *testing.T, pol ExecPolicy) *Engine {
	t.Helper()
	reg, _ := testRegistry(t)
	e := NewEngine(reg)
	e.Policy.Exec = pol
	return e
}

func mustRun(t *testing.T, e *Engine, src string) any {
	t.Helper()
	r := e.Run(context.Background(), src, "p.js")
	if !r.OK {
		t.Fatalf("run failed: %s", r.Error)
	}
	return r.Value
}

func TestExecDeniedByDefault(t *testing.T) {
	e := execEngine(t, ExecPolicy{})
	_, diags := e.Compile(`await exec(["echo", "hi"])`, "p.js")
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "allows no external programs") || diags[0].Line != 1 {
		t.Fatalf("want static denial, got %v", diags)
	}
	r := e.Run(context.Background(), `const p = "ec" + "ho"; await exec([p, "hi"])`, "p.js")
	if r.OK || !strings.Contains(r.Error, "not permitted") {
		t.Fatalf("dynamic argv must be denied at run time: %+v", r)
	}
	_, diags = e.Compile(`await exec("echo hi")`, "p.js")
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "argv array") {
		t.Fatalf("got %v", diags)
	}
	r = e.Run(context.Background(), `await shell("echo hi")`, "p.js")
	if r.OK || !strings.Contains(r.Error, "shell access needs its own authorization") {
		t.Fatalf("got %+v", r)
	}
}

func TestExec(t *testing.T) {
	e := execEngine(t, ExecPolicy{Programs: []string{"echo", "sh", "cat"}})
	v := mustRun(t, e, `const r = await exec(["echo", "hello world"]); [r.ok, r.exitCode, r.stdout]`)
	if got := v.([]any); got[0] != true || got[1] != 0.0 || got[2] != "hello world\n" {
		t.Fatalf("got %v", got)
	}
	// non-zero exit is a result, not an exception (same as a WFNexus run step)
	v = mustRun(t, e, `const r = await exec(["sh", "-c", "echo oops >&2; exit 3"]); [r.ok, r.exitCode, r.stderr]`)
	if got := v.([]any); got[0] != false || got[1] != 3.0 || got[2] != "oops\n" {
		t.Fatalf("got %v", got)
	}
	v = mustRun(t, e, `(await exec(["cat"], {stdin: "piped"})).stdout`)
	if v != "piped" {
		t.Fatalf("got %v", v)
	}
	// argv is not shell-interpreted
	v = mustRun(t, e, `(await exec(["echo", "$HOME; rm -rf /"])).stdout`)
	if v != "$HOME; rm -rf /\n" {
		t.Fatalf("got %v", v)
	}
	r := e.Run(context.Background(), `await exec(["sh", "-c", "sleep 5"], {timeoutMs: 50})`, "p.js")
	if r.OK || !strings.Contains(r.Error, "deadline") {
		t.Fatalf("timeout: %+v", r)
	}
}

func TestExecEnvIsMinimal(t *testing.T) {
	t.Setenv("CODEMODE_TEST_SECRET", "s3cret")
	t.Setenv("CODEMODE_TEST_PASS", "visible")
	e := execEngine(t, ExecPolicy{Programs: []string{"sh"}, Env: []string{"CODEMODE_TEST_PASS"}})
	v := mustRun(t, e, `(await exec(["sh", "-c", "echo [$CODEMODE_TEST_SECRET][$CODEMODE_TEST_PASS]"])).stdout`)
	if v != "[][visible]\n" {
		t.Fatalf("env leaked or missing: %q", v)
	}
}

func TestShellWhenAuthorized(t *testing.T) {
	e := execEngine(t, ExecPolicy{Shell: true})
	v := mustRun(t, e, `(await shell("echo a | tr a b")).stdout`)
	if v != "b\n" {
		t.Fatalf("got %v", v)
	}
}

func TestExecStreamJSONLPipeline(t *testing.T) {
	e := execEngine(t, ExecPolicy{Programs: []string{"sh"}})
	v := mustRun(t, e, `await execStream(["sh", "-c", "for i in 1 2 3 4 5; do echo '{\"level\":\"ERROR\",\"i\":'$i'}'; echo '{\"level\":\"INFO\"}'; done"])
		.jsonl().filter(x => x.level === "ERROR").map(x => x.i).take(3).toArray()`)
	if got := v.([]any); len(got) != 3 || got[2] != 3.0 {
		t.Fatalf("got %v", v)
	}
}

func TestExecStreamTakeKillsInfiniteProcess(t *testing.T) {
	if _, err := exec.LookPath("yes"); err != nil {
		t.Skip("no `yes` binary")
	}
	e := execEngine(t, ExecPolicy{Programs: []string{"yes"}})
	start := time.Now()
	v := mustRun(t, e, `await execStream(["yes", "line"]).take(3).toArray()`)
	if got := v.([]any); len(got) != 3 || got[0] != "line" {
		t.Fatalf("got %v", v)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("take did not stop the process promptly (%v)", el)
	}
}

func TestExecStreamFailureAndCancel(t *testing.T) {
	e := execEngine(t, ExecPolicy{Programs: []string{"sh"}})
	r := e.Run(context.Background(), `await execStream(["sh", "-c", "echo a; echo broke >&2; exit 4"]).toArray()`, "p.js")
	if r.OK || !strings.Contains(r.Error, "exit 4: broke") {
		t.Fatalf("got %+v", r)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	r = e.Run(ctx, `await execStream(["sh", "-c", "while true; do echo x; sleep 0.01; done"]).count()`, "p.js")
	if r.OK || time.Since(start) > 4*time.Second {
		t.Fatalf("cancel did not stop the stream: %+v after %v", r, time.Since(start))
	}
}

func TestExecStreamWriteJSONL(t *testing.T) {
	e := execEngine(t, ExecPolicy{Programs: []string{"sh"}})
	var out strings.Builder
	e.Stdout = &out
	v := mustRun(t, e, `await execStream(["sh", "-c", "printf 'a,b\n1,2\n3,4\n'"]).csv().writeJSONL()`)
	if v != 2.0 || out.String() != "{\"a\":\"1\",\"b\":\"2\"}\n{\"a\":\"3\",\"b\":\"4\"}\n" {
		t.Fatalf("got %v %q", v, out.String())
	}
	_ = os.Stdout
}

func TestExecCwdConfined(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	if err := os.MkdirAll(filepath.Join(base, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	e := execEngine(t, ExecPolicy{Programs: []string{"pwd"}, Shell: true, Dir: base})
	v := mustRun(t, e, `(await exec(["pwd"], {cwd: "sub"})).stdout.trim()`)
	if s, _ := v.(string); filepath.Base(s) != "sub" {
		t.Fatalf("inside cwd: got %v", v)
	}
	for _, src := range []string{
		`await exec(["pwd"], {cwd: "../outside"})`,
		`await exec(["pwd"], {cwd: "sub/../../outside"})`,
		`await exec(["pwd"], {cwd: "/"})`,
		`await exec(["pwd"], {cwd: "link"})`,
		`await shell("pwd", {cwd: ".."})`,
		`await execStream(["pwd"], {cwd: ".."}).toArray()`,
	} {
		r := e.Run(context.Background(), src, "p.js")
		if r.OK || !strings.Contains(r.Error, "outside the allowed directory") {
			t.Fatalf("%s: want rejection, got %+v", src, r)
		}
	}
}
