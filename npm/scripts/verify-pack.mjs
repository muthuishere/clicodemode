#!/usr/bin/env node
// Proves the packed tarball works on a fresh machine without the registry:
// npm pack -> brand-new dir -> npm install <tgz> -> run the README quickstart
// verbatim as ESM JS, and type-check the README TS quickstart (tsc --noEmit)
// against the installed types. Needs a prior `npm run build:wasm && npm run build`.
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync, mkdirSync, readdirSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const pkgDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const sh = (cmd, args, cwd) => execFileSync(cmd, args, { cwd, encoding: "utf8", stdio: ["ignore", "pipe", "inherit"] });
const fail = (m) => { console.error("verify-pack FAILED: " + m); process.exit(1); };

if (!existsSync(join(pkgDir, "dist/codemode.wasm"))) fail("dist/codemode.wasm missing: run `npm run build:wasm && npm run build` first");

const work = mkdtempSync(join(tmpdir(), "clicodemode-pack-"));
const raw = JSON.parse(sh("npm", ["pack", "--json", "--pack-destination", work], pkgDir));
const packOut = Array.isArray(raw) ? raw : Object.values(raw); // some npm builds key the result by name
const files = packOut[0].files.map((f) => f.path);
for (const need of ["dist/index.js", "dist/index.d.ts", "dist/types.d.ts", "dist/wasm.js", "dist/codemode.wasm", "dist/wasm_exec.js", "README.md"]) {
  if (!files.includes(need)) fail(`tarball lacks ${need}`);
}
const tgz = join(work, packOut[0].filename);
console.log("packed", tgz, `(${files.length} files)`);

const app = join(work, "app");
mkdirSync(app);
sh("npm", ["init", "-y"], app);
const pj = JSON.parse(readFileSync(join(app, "package.json"), "utf8"));
pj.type = "module";
writeFileSync(join(app, "package.json"), JSON.stringify(pj));
sh("npm", ["install", "--no-audit", "--no-fund", tgz], app);

// Pull the quickstart blocks out of the README, verbatim.
const readme = readFileSync(join(pkgDir, "README.md"), "utf8");
const block = (lang) => {
  const m = readme.match(new RegExp("<!-- quickstart-" + lang + " -->\\s*```" + lang + "\\n([\\s\\S]*?)```"));
  if (!m) fail(`README has no <!-- quickstart-${lang} --> block`);
  return m[1];
};
writeFileSync(join(app, "quickstart.mjs"), block("js"));
writeFileSync(join(app, "quickstart.ts"), block("ts"));

const out = sh(process.execPath, ["quickstart.mjs"], app).trim();
console.log("--- quickstart.mjs output ---\n" + out);
if (!out) fail("quickstart.mjs printed nothing");

writeFileSync(join(app, "tsconfig.json"), JSON.stringify({
  compilerOptions: { target: "ES2022", module: "NodeNext", moduleResolution: "NodeNext", lib: ["ES2022", "DOM"], types: [], strict: true, noEmit: true, skipLibCheck: false },
  include: ["quickstart.ts"],
}));
const tsc = join(pkgDir, "node_modules/typescript/bin/tsc");
if (!existsSync(tsc)) fail("typescript not installed in npm/ (run npm ci)");
sh(process.execPath, [tsc, "-p", "tsconfig.json"], app);
console.log("tsc --noEmit on quickstart.ts against installed types: ok");

// Run the TS quickstart too, by stripping nothing: compile it with tsc to JS.
sh(process.execPath, [tsc, "-p", "tsconfig.json", "--noEmit", "false", "--outDir", "out"], app);
const out2 = sh(process.execPath, [join(app, "out/quickstart.js")], app).trim();
console.log("--- quickstart.ts (compiled) output ---\n" + out2);
if (out2 !== out) fail("TS and JS quickstarts printed different output");
console.log("verify-pack OK");
