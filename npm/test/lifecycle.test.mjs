import test from "node:test";
import assert from "node:assert/strict";
import { createCodeMode } from "../dist/index.js";

const never = () => new Promise(() => {});
function make(extra = {}) {
  const cm = createCodeMode({ version: "1.0.0", ...extra });
  cm.register({ name: "demo.add", description: "add", handler: async ({ a, b }) => ({ sum: a + b }) });
  cm.register({ name: "demo.never", description: "never resolves", handler: never });
  cm.register({ name: "demo.echo", description: "echo", handler: async (a) => a });
  cm.register({ name: "demo.sleep", description: "sleep", handler: ({ ms, v }) => new Promise((r) => setTimeout(() => r(v), ms)) });
  return cm;
}

test("timeout abandons a never-resolving handler and the engine stays usable", async () => {
  const cm = make();
  const t = Date.now();
  const r = await cm.run("await ops.demo.never({})", { timeoutMs: 300 });
  assert.equal(r.ok, false);
  assert.match(r.error, /timed out after 300ms/);
  assert.ok(Date.now() - t < 3000, `took ${Date.now() - t}ms`);
  const again = await cm.run("(await ops.demo.add({a:2,b:3})).sum");
  assert.equal(again.value, 5);
});

test("process exits after a never-resolving handler times out", async () => {
  const { spawnSync } = await import("node:child_process");
  const code = `
    import { createCodeMode } from ${JSON.stringify(new URL("../dist/index.js", import.meta.url).href)};
    const cm = createCodeMode({ version: "1" });
    cm.register({ name: "x.never", description: "n", handler: () => new Promise(() => {}) });
    const r = await cm.run("await ops.x.never({})", { timeoutMs: 200 });
    console.log(r.ok);`;
  const p = spawnSync(process.execPath, ["--input-type=module", "-e", code], { timeout: 20000, encoding: "utf8" });
  assert.equal(p.status, 0, p.stderr + p.error);
  assert.equal(p.stdout.trim(), "false");
});

test("AbortSignal aborts a run waiting on a handler", async () => {
  const cm = make();
  const ac = new AbortController();
  setTimeout(() => ac.abort(), 200);
  const t = Date.now();
  const r = await cm.run("await ops.demo.never({})", { signal: ac.signal, timeoutMs: 0 });
  assert.equal(r.ok, false);
  assert.match(r.error, /cancel/i);
  assert.ok(Date.now() - t < 3000);
  assert.equal((await cm.run("1+1")).value, 2);
});

test("an already-aborted signal does not run the program", async () => {
  const cm = make();
  const ac = new AbortController(); ac.abort();
  const r = await cm.run("1+1", { signal: ac.signal });
  assert.equal(r.ok, false);
});

test("eval honours timeoutMs", async () => {
  const cm = make();
  const r = await cm.eval("await ops.demo.never({})", { timeoutMs: 200 });
  assert.equal(r.ok, false);
});

test("limits.max_steps stops a CPU loop", async () => {
  const cm = make({ limits: { max_steps: 50000 } });
  const r = await cm.run("let i = 0; while (true) { i++ }");
  assert.equal(r.ok, false);
  assert.match(r.error, /step budget/);
  assert.equal((await cm.run("1")).value, 1);
});

test("exec is denied with a clear message", async () => {
  const cm = make();
  const r = await cm.run('await exec(["echo", "hi"])');
  assert.equal(r.ok, false);
  const msg = (r.diagnostics ?? []).map((d) => d.message).join("\n") + (r.error ?? "");
  assert.match(msg, /"echo" is not permitted/);
  assert.match(msg, /no external programs/);
});

test("docs() and skill() return text", async () => {
  const cm = make();
  for (const t of [await cm.docs(), await cm.skill()]) {
    assert.equal(typeof t, "string");
    assert.ok(t.length > 100);
    assert.match(t, /demo\.add/);
  }
});

test("invalid and duplicate op names throw", () => {
  const cm = make();
  assert.throws(() => cm.register({ name: "Bad Name", description: "x", handler: async () => 1 }), /invalid operation name/);
  assert.throws(() => cm.register({ name: "", description: "x", handler: async () => 1 }), /invalid operation name/);
  assert.throws(() => cm.register({ name: "demo.add", description: "x", handler: async () => 1 }), /already registered/);
  assert.throws(() => cm.register({ name: "ok.name", description: "x" }), /no handler/);
});

test("two engines are isolated, including handlers", async () => {
  const a = createCodeMode({ version: "1" });
  const b = createCodeMode({ version: "1" });
  a.register({ name: "who.am", description: "x", handler: async () => "a" });
  b.register({ name: "who.am", description: "x", handler: async () => "b" });
  const [ra, rb] = await Promise.all([a.run("await ops.who.am({})"), b.run("await ops.who.am({})")]);
  assert.equal(ra.value, "a");
  assert.equal(rb.value, "b");
});

test("large payload round-trips through a handler", async () => {
  const cm = make();
  const big = "x".repeat(2_000_000);
  const r = await cm.run("const o = await ops.demo.echo({s: input.s, n: input.n}); [o.s.length, o.n.length]",
    { input: { s: big, n: Array.from({ length: 50000 }, (_, i) => i) } });
  assert.equal(r.ok, true, String(r.error).slice(0, 300));
  assert.deepEqual(r.value, [2_000_000, 50000]);
});

test("a handler throw is a catchable rejection; a sync throw too", async () => {
  const cm = make();
  cm.register({ name: "demo.sync_boom", description: "x", handler: () => { throw new Error("sync-kaboom"); } });
  const r = await cm.run("let m=''; try { await ops.demo.sync_boom({}) } catch (e) { m = e.message } m");
  assert.equal(r.ok, true, JSON.stringify(r));
  assert.match(r.value, /sync-kaboom/);
});

test("concurrent runs on one engine each get their own result", async () => {
  const cm = make();
  const rs = await Promise.all(Array.from({ length: 8 }, (_, i) =>
    cm.run("(await ops.demo.sleep({ms: 30, v: input.i}))", { input: { i } })));
  assert.deepEqual(rs.map((r) => r.value), [0, 1, 2, 3, 4, 5, 6, 7]);
});

test("cancelling one concurrent run leaves the others running", async () => {
  const cm = make();
  const ac = new AbortController();
  const slow = cm.run("await ops.demo.never({})", { signal: ac.signal, timeoutMs: 0 });
  const ok = cm.run("await ops.demo.sleep({ms: 100, v: 7})");
  setTimeout(() => ac.abort(), 50);
  const [a, b] = await Promise.all([slow, ok]);
  assert.equal(a.ok, false);
  assert.equal(b.value, 7);
});

test("host op results keep object key order (integer keys first, then insertion order)", async () => {
  const cm = createCodeMode({ version: "1" });
  cm.register({ name: "k.get", description: "x", handler: async () => ({ b: 1, a: 2, "1": 3 }) });
  const r = await cm.run("Object.keys(await ops.k.get()).join(',')");
  assert.equal(r.ok, true, JSON.stringify(r));
  assert.equal(r.value, "1,b,a");
});
