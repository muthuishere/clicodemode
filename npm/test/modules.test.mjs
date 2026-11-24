import { test } from "node:test";
import assert from "node:assert/strict";
import { createCodeMode } from "../dist/index.js";

const make = () => {
  const cm = createCodeMode({ version: "1.0.0" });
  cm.register({ name: "math.add", description: "add", handler: async ({ a, b }) => ({ sum: a + b }) });
  return cm;
};

test("registerModules reads a folder; programs import scheme:path", async () => {
  const cm = make();
  cm.registerModules("my", new URL("./fixtures/mods/", import.meta.url));
  const r = await cm.run(`import { calc } from "my:layout"; await calc(5)`);
  assert.equal(r.ok, true, JSON.stringify(r));
  assert.equal(r.value, 11);
  const caps = await cm.capabilities();
  const layout = caps.modules.find((m) => m.name === "my:layout");
  assert.equal(layout.description, "Layout helpers.");
  assert.deepEqual(layout.exports, [{ name: "calc", doc: "Doubles then adds the configured offset." }]);
  assert.match(await cm.skill(), /import \{ calc \} from "my:layout"/);
});

test("registerModule from content and registerModules from a map (bundlers)", async () => {
  const cm = make();
  cm.registerModule({ name: "b:one", source: "export const one = 1" });
  cm.registerModules("c", { "two.js": "export const two = 2", "notes.txt": "ignored" });
  const r = await cm.run(`import { one } from "b:one"; import { two } from "c:two"; one + two`);
  assert.equal(r.value, 3, JSON.stringify(r));
});

test("registerModuleFile and bad names", async () => {
  const cm = make();
  cm.registerModuleFile("f", new URL("./fixtures/mods/util/math.js", import.meta.url));
  assert.equal((await cm.run(`import { twice } from "f:math"; twice(4)`)).value, 8);
  assert.throws(() => cm.registerModule({ name: "nocolon", source: "" }), /invalid module name/);
  assert.throws(() => cm.registerModule({ name: "f:math", source: "" }), /already registered/);
});

test("a module using an unknown op fails check, nothing runs", async () => {
  const cm = make();
  cm.registerModule({ name: "x:bad", source: "export const f = () => ops.nope({})" });
  const r = await cm.check(`import { f } from "x:bad"`);
  assert.equal(r.ok, false);
  assert.match(JSON.stringify(r.diagnostics), /unknown operation/);
});

test("clicodemode-embed bakes a folder into a module at build time", async () => {
  const { execFileSync } = await import("node:child_process");
  const { mkdtempSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const { join } = await import("node:path");
  const out = join(mkdtempSync(join(tmpdir(), "ccm-")), "modules.generated.js");
  execFileSync(process.execPath, [new URL("../bin/clicodemode-embed.mjs", import.meta.url).pathname, new URL("./fixtures/mods", import.meta.url).pathname, out]);
  const { default: files } = await import(out);
  assert.deepEqual(Object.keys(files), ["cfg.json", "layout.js", "util/math.js"]);
  const cm = make();
  cm.registerModules("g", files);
  assert.equal((await cm.run(`import { calc } from "g:layout"; await calc(2)`)).value, 5);
});
