#!/usr/bin/env node
// Runs conformance/cases/*.json through the JS API of the npm package.
//
//   node conformance/run-js.mjs <path-to>/npm/dist/index.js [--filter substr] [--strict] [-v]
//
// Dependency-free. Exit code 1 if any case fails. Cases flagged
// "known_deviation" describe a bug in the Go reference engine: a failure there
// is reported as "DEVIATION" and does not fail the run, unless --strict.
// A pass on such a case is reported as "FIXED" (the port is better than Go).
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const args = process.argv.slice(2);
const flag = (n) => args.includes(n);
const opt = (n) => { const i = args.indexOf(n); return i >= 0 ? args[i + 1] : undefined; };
const distPath = args.find((a, i) => !a.startsWith("-") && args[i - 1] !== "--filter");
if (!distPath) {
  console.error("usage: node run-js.mjs <path to npm/dist/index.js> [--filter substr] [--strict] [-v]");
  process.exit(2);
}
const filter = opt("--filter");
const strict = flag("--strict");
const verbose = flag("-v");
const { createCodeMode } = await import(pathToFileURL(path.resolve(distPath)).href);
const casesDir = path.join(path.dirname(fileURLToPath(import.meta.url)), "cases");

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const deepEqual = (a, b) => JSON.stringify(canon(a)) === JSON.stringify(canon(b));
// Compare JSON values ignoring object key order (fixtures compare as data).
function canon(v) {
  if (Array.isArray(v)) return v.map(canon);
  if (v && typeof v === "object") return Object.fromEntries(Object.keys(v).sort().map((k) => [k, canon(v[k])]));
  return v === undefined ? null : v;
}

function stubSpec(o, tracker) {
  const spec = {
    name: o.name,
    description: o.description ?? "",
    idempotent: !!o.idempotent,
    handler: async () => {
      tracker.calls[o.name] = (tracker.calls[o.name] ?? 0) + 1;
      tracker.inFlight++;
      tracker.peak = Math.max(tracker.peak, tracker.inFlight);
      try {
        if (o.panics) throw new Error("stub panic");
        if (o.blocks_until_cancel) await new Promise(() => {});
        if (o.delay_ms) await sleep(o.delay_ms);
        if (o.throws) throw new Error(o.throws);
        return o.returns === undefined ? null : o.returns;
      } finally {
        tracker.inFlight--;
      }
    },
  };
  if (o.no_handler) delete spec.handler;
  if (o.input_schema !== undefined) spec.inputSchema = o.input_schema;
  if (o.side_effects !== undefined) spec.sideEffects = o.side_effects;
  return spec;
}

function build(ops, tracker, limits) {
  const cm = createCodeMode({ version: "conformance 1.0", limits: limits ?? undefined });
  for (const o of ops ?? []) cm.register(stubSpec(o, tracker));
  return cm;
}

class Skip extends Error {}

async function runCase(c) {
  const mode = c.mode ?? "run";
  const x = c.expect;
  const tracker = { calls: {}, inFlight: 0, peak: 0 };
  if (c.policy) throw new Skip("policy option not supported by the JS API");

  if (mode === "register") {
    let failedOn = null;
    const cm = createCodeMode({ version: "conformance 1.0" });
    for (const o of c.ops ?? []) {
      try { cm.register(stubSpec(o, tracker)); } catch (e) { failedOn = o.name; break; }
    }
    if (x.register_error) {
      if (failedOn === null) throw new Error(`registration must fail for ${JSON.stringify(x.register_error.op)}`);
      if (failedOn !== x.register_error.op) throw new Error(`registration failed on ${JSON.stringify(failedOn)}, want ${JSON.stringify(x.register_error.op)}`);
    } else if (failedOn !== null) throw new Error(`unexpected registration error on ${failedOn}`);
    return;
  }
  if (mode === "fingerprint") {
    const f = async (ops) => (await build(ops, tracker).capabilities()).fingerprint;
    const [a, b] = [await f(c.ops), await f(c.ops_b)];
    const re = /^sha256:[0-9a-f]{32}$/;
    if (!re.test(a) || !re.test(b)) throw new Error(`fingerprint format: ${a} ${b}`);
    if ((a === b) !== (x.fingerprint === "equal")) throw new Error(`fingerprints ${a} vs ${b}, want ${x.fingerprint}`);
    return;
  }

  const cm = build(c.ops, tracker, c.limits);
  const file = c.file ?? "program.js";
  let res, diags, program;
  if (mode === "check") {
    const r = await cm.check(c.program, file);
    diags = r.diagnostics ?? [];
    if (x.operations) {
      const comp = await cm.compile(c.program, file);
      program = comp.program;
    }
  } else if (mode === "eval") {
    res = await cm.eval(c.program, { input: c.input });
    diags = res.diagnostics ?? [];
  } else {
    res = await cm.run(c.program, { file, input: c.input });
    diags = res.diagnostics ?? [];
  }

  for (const [name, n] of Object.entries(x.calls ?? {})) {
    const got = tracker.calls[name] ?? 0;
    if (got !== n) throw new Error(`op ${name} called ${got} times, want ${n}`);
  }
  if (x.stdout !== undefined) {
    if (res?.stdout === undefined) throw new Skip("result.stdout missing");
    if (res.stdout !== x.stdout) throw new Error(`stdout ${JSON.stringify(res.stdout)}, want ${JSON.stringify(x.stdout)}`);
  }
  if (x.max_in_flight_at_most && tracker.peak > x.max_in_flight_at_most) {
    throw new Error(`peak in-flight ${tracker.peak} > ${x.max_in_flight_at_most}`);
  }
  if (mode === "check" && x.operations) {
    const got = program?.operations ?? [];
    if (!deepEqual(got, x.operations)) throw new Error(`operations ${JSON.stringify(got)}, want ${JSON.stringify(x.operations)}`);
  }
  if (x.exit !== undefined) {
    let exit = 0;
    if (mode === "check") exit = diags.some((d) => d.severity === "error") ? 2 : 0;
    else if (!res.ok) exit = /nothing was run| error: syntax error/.test(res.error ?? "") ? 2 : 1;
    if (exit !== x.exit) throw new Error(`exit class ${exit}, want ${x.exit} (${JSON.stringify(res ?? diags)})`);
  }

  if (x.diagnostics) {
    if (mode !== "check" && (res.ok || !/nothing was run| error: syntax error/.test(res.error ?? ""))) {
      throw new Error(`expected diagnostics and nothing run, got ${JSON.stringify(res)}`);
    }
    if (diags.length !== x.diagnostics.length) throw new Error(`got ${diags.length} diagnostics ${JSON.stringify(diags)}, want ${x.diagnostics.length}`);
    x.diagnostics.forEach((w, i) => {
      const g = diags[i];
      if (w.file && g.file !== w.file) throw new Error(`diag ${i} file ${g.file}, want ${w.file}`);
      if (w.severity && g.severity !== w.severity) throw new Error(`diag ${i} severity ${g.severity}`);
      if ((w.line && g.line !== w.line) || (w.col && g.col !== w.col)) throw new Error(`diag ${i} at ${g.line}:${g.col}, want ${w.line}:${w.col} (${g.message})`);
      if (!(g.message ?? "").includes(w.messageContains ?? "")) throw new Error(`diag ${i} message ${JSON.stringify(g.message)} lacks ${JSON.stringify(w.messageContains)}`);
      if (!(g.hint ?? "").includes(w.hintContains ?? "")) throw new Error(`diag ${i} hint ${JSON.stringify(g.hint)} lacks ${JSON.stringify(w.hintContains)}`);
    });
  } else if (x.error) {
    if (res.ok) throw new Error(`expected failure containing ${JSON.stringify(x.error.messageContains)}, got ${JSON.stringify(res.value)}`);
    if (!(res.error ?? "").includes(x.error.messageContains ?? "")) throw new Error(`error ${JSON.stringify(res.error)} lacks ${JSON.stringify(x.error.messageContains)}`);
    if (x.error.line) {
      const last = diags[diags.length - 1];
      if (!last || last.line !== x.error.line) throw new Error(`error line ${last?.line}, want ${x.error.line}`);
    }
  } else if ("result" in x) {
    if (!res.ok) throw new Error(`run failed: ${res.error} ${JSON.stringify(res.diagnostics)}`);
    if (!deepEqual(res.value, x.result)) throw new Error(`result ${JSON.stringify(res.value)}, want ${JSON.stringify(x.result)}`);
  }
}

const files = fs.readdirSync(casesDir).filter((f) => f.endsWith(".json")).sort();
const seen = new Set();
const tally = { pass: 0, fail: 0, skip: 0, deviation: 0, fixed: 0 };
for (const f of files) {
  for (const c of JSON.parse(fs.readFileSync(path.join(casesDir, f), "utf8"))) {
    if (seen.has(c.id)) { console.log(`FAIL  ${c.id}: duplicate id`); tally.fail++; continue; }
    seen.add(c.id);
    if (filter && !c.id.includes(filter)) continue;
    let err = null, skipped = null;
    try {
      await Promise.race([runCase(c), sleep(30000).then(() => { throw new Error("case timed out after 30s"); })]);
    } catch (e) {
      if (e instanceof Skip) skipped = e.message; else err = e;
    }
    if (skipped) { tally.skip++; console.log(`SKIP  ${c.id}: ${skipped}`); }
    else if (err && c.known_deviation && !strict) { tally.deviation++; console.log(`DEVIATION  ${c.id} (known Go deviation): ${err.message}`); }
    else if (err) { tally.fail++; console.log(`FAIL  ${c.id}: ${err.message}`); }
    else if (c.known_deviation) { tally.fixed++; console.log(`FIXED ${c.id}: passes although the Go engine deviates`); }
    else { tally.pass++; if (verbose) console.log(`PASS  ${c.id}`); }
  }
}
console.log(`\n${tally.pass} passed, ${tally.fail} failed, ${tally.skip} skipped, ${tally.deviation} known deviations, ${tally.fixed} fixed deviations`);
process.exit(tally.fail ? 1 : 0);
