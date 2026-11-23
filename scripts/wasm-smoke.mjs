// Smoke test: node scripts/wasm-smoke.mjs (after scripts/build-wasm.sh)
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const dist = path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "npm", "dist");
createRequire(import.meta.url)(path.join(dist, "wasm_exec.js"));
const go = new Go();
const { instance } = await WebAssembly.instantiate(fs.readFileSync(path.join(dist, "codemode.wasm")), go.importObject);
go.run(instance); // blocks in Go's select{}; resolves never
const cm = globalThis.__codemode;

const config = {
  version: "1.0.0",
  operations: [
    { name: "demo.add", description: "add two numbers", side_effects: "none", idempotent: true,
      input_schema: { type: "object", properties: { a: { type: "number" }, b: { type: "number" } }, required: ["a", "b"] } },
    { name: "demo.fail", description: "always fails" },
  ],
  limits: { max_concurrency: 4, timeout_ms: 5000 },
};
const invoke = async (name, argsJson) => {
  const a = JSON.parse(argsJson);
  await new Promise((r) => setTimeout(r, 5));
  if (name === "demo.add") return JSON.stringify({ sum: a.a + a.b });
  throw new Error("boom from host");
};
const id = cm.create(JSON.stringify(config), invoke);
console.log("id:", id, "version:", cm.version());
console.log("operations:", cm.operations(id).slice(0, 80) + "...");
console.log("check(class):", cm.check(id, "class A {}", "p.js"));
console.log("check(bad op):", cm.check(id, 'await ops.demo.ad({a:1})', "p.js"));
console.log("run add:", await cm.run(id, 'print("hi"); const r = await ops.demo.add({a:1,b:2}); r', "p.js", ""));
console.log("run input:", await cm.run(id, "input.x * 2", "p.js", '{"x":21}'));
console.log("run fail:", await cm.run(id, 'try { await ops.demo.fail({}) } catch (e) { e.message }', "p.js", ""));
console.log("run exec:", await cm.run(id, 'await exec(["ls"])', "p.js", ""));
console.log("eval:", await cm.eval(id, "1+2", ""));
console.log("docs bytes:", cm.docs(id).length, "skill bytes:", cm.skill(id).length);
console.log("dup op:", cm.create(JSON.stringify({ version: "1", operations: [config.operations[0], config.operations[0]] }), invoke));
console.log("bad id:", cm.operations(99));
process.exit(0);
