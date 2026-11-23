import test from "node:test";
import assert from "node:assert/strict";
import { createCodeMode } from "../dist/index.js";

function make(version = "1.0.0") {
  const cm = createCodeMode({ version });
  cm.register({
    name: "demo.add", description: "add two numbers", sideEffects: "none", idempotent: true,
    inputSchema: { type: "object", properties: { a: { type: "number" }, b: { type: "number" } } },
    handler: async ({ a, b }) => ({ sum: a + b }),
  });
  return cm;
}

test("runs a program calling an async op", async () => {
  const cm = make();
  const r = await cm.run("const r = await ops.demo.add({a:1,b:2}); r.sum");
  assert.equal(r.ok, true, JSON.stringify(r));
  assert.equal(r.value, 3);
});

test("operations/describe list registered ops", async () => {
  const cm = make();
  const ops = await cm.operations();
  assert.deepEqual(ops.map((o) => o.name), ["demo.add"]);
  assert.equal((await cm.describe("demo.add")).side_effects, "none");
});

test("check reports an unsupported construct with file:line:col", async () => {
  const cm = make();
  const r = await cm.check("const x = 1;\ndelete x.y\n", "prog.js");
  assert.equal(r.ok, false);
  const d = r.diagnostics.find((d) => /delete/.test(d.message));
  assert.ok(d, JSON.stringify(r));
  assert.equal(d.file, "prog.js");
  assert.equal(d.line, 2);
  assert.ok(d.col >= 1);
});

test("fingerprint changes when an op is added", async () => {
  const cm = make();
  const a = (await cm.capabilities()).fingerprint;
  cm.register({ name: "demo.sub", description: "sub", handler: async ({ a, b }) => a - b });
  const b = (await cm.capabilities()).fingerprint;
  assert.notEqual(a, b);
  assert.equal((await cm.operations()).length, 2);
});

test("handler rejection surfaces as a rejected call", async () => {
  const cm = createCodeMode({ version: "1" });
  cm.register({ name: "demo.boom", description: "fails", handler: async () => { throw new Error("kaboom"); } });
  const bad = await cm.run("await ops.demo.boom({})");
  assert.equal(bad.ok, false);
  assert.match(bad.error, /kaboom/);
  const caught = await cm.run("let m = ''; try { await ops.demo.boom({}); } catch (e) { m = 'caught:' + e.message } m");
  assert.equal(caught.ok, true, JSON.stringify(caught));
  assert.match(caught.value, /caught:.*kaboom/);
});

test("input is passed to the program and eval", async () => {
  const cm = make();
  const r = await cm.run("input.n * 2", { input: { n: 21 } });
  assert.equal(r.value, 42);
  const e = await cm.eval("input.s + '!'", { input: { s: "hi" } });
  assert.equal(e.value, "hi!");
});

test("two engines are isolated", async () => {
  const a = make();
  const b = createCodeMode({ version: "2.0.0" });
  b.register({ name: "other.op", description: "x", handler: async () => "b" });
  assert.deepEqual((await a.operations()).map((o) => o.name), ["demo.add"]);
  assert.deepEqual((await b.operations()).map((o) => o.name), ["other.op"]);
  const r = await a.run("typeof ops.other");
  assert.notEqual(r.value, "object");
  assert.equal((await b.run("await ops.other.op({})")).value, "b");
});
