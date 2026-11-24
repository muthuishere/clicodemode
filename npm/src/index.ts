import { readFileSync, readdirSync } from "node:fs";
import { basename, join, relative, sep } from "node:path";
import { fileURLToPath } from "node:url";
import { getBridge, loadWasm, type Bridge } from "./wasm.js";
import type {
  Capabilities, CheckResult, CodeModeOptions, ModuleSpec, Operation, OperationSpec, Result,
} from "./types.js";

export * from "./types.js";
export { loadWasm, type Bridge } from "./wasm.js";

/** Structural subset of the standard AbortSignal (no DOM or @types/node needed). */
export interface AbortSignalLike {
  readonly aborted: boolean;
  addEventListener(type: "abort", fn: () => void, opts?: { once?: boolean }): void;
  removeEventListener(type: "abort", fn: () => void): void;
}

/** Default wall-clock bound for one run/eval call, in milliseconds. */
export const DEFAULT_TIMEOUT_MS = 30_000;

export interface CallOptions {
  /** Program input, visible to the program as `input`. */
  input?: unknown;
  /**
   * Bound for this call in ms (default DEFAULT_TIMEOUT_MS = 30000; 0 = none
   * beyond the engine's own limits.timeout_ms). On expiry the call resolves
   * with ok:false and pending host handlers are abandoned. Caveat: WASM has
   * no preemption, so the timeout cannot interrupt a CPU-bound loop that
   * never awaits a host operation; that is bounded by limits.max_steps.
   */
  timeoutMs?: number;
  /** Aborting resolves the call with ok:false and abandons pending host handlers. Like timeoutMs it takes effect when the program awaits a host operation, not inside a pure CPU loop. */
  signal?: AbortSignalLike;
}
export interface RunOptions extends CallOptions { file?: string }
export interface EvalOptions extends CallOptions {}

export interface CodeMode {
  register(op: OperationSpec): void;
  /** Add one module from its source text (e.g. bundled with `?raw`). */
  registerModule(mod: ModuleSpec): void;
  /** Add one .js/.json file read now (at startup) as "scheme:<file name>". */
  registerModuleFile(scheme: string, file: string | URL): void;
  /** Add every .js/.json file under a directory (read now), or every entry of
   * a {relativePath: source} map (for bundlers), as "scheme:<relative path>". */
  registerModules(scheme: string, dirOrFiles: string | URL | Record<string, string>): void;
  operations(): Promise<Operation[]>;
  describe(name: string): Promise<Operation>;
  capabilities(): Promise<Capabilities>;
  docs(): Promise<string>;
  skill(): Promise<string>;
  check(src: string, file?: string): Promise<CheckResult>;
  compile(src: string, file?: string): Promise<CheckResult>;
  run(src: string, opts?: RunOptions): Promise<Result>;
  eval(expr: string, opts?: EvalOptions): Promise<Result>;
}

let callSeq = 0;

const NAME_RE = /^[A-Za-z][A-Za-z0-9_-]*(\.[A-Za-z][A-Za-z0-9_-]*)*$/;
const MODULE_RE = /^[A-Za-z][A-Za-z0-9_-]*:[A-Za-z0-9_-]+(\/[A-Za-z0-9_-]+)*(\.json)?$/;

function parse<T>(s: string): T {
  const v = JSON.parse(s);
  if (v && typeof v === "object" && !Array.isArray(v) && typeof v.error === "string" && Object.keys(v).length === 1) {
    throw new Error("clicodemode: " + v.error); // bridge reports bad id / unknown op as {"error":...}
  }
  return v as T;
}

function withOk(r: CheckResult): CheckResult {
  const ds = r.diagnostics ?? [];
  return { ...r, diagnostics: ds, ok: !ds.some((d) => d.severity === "error") };
}

/** Parse bridge output; a non-JSON string (docs/skill text) is returned as is. */
function text(s: string): string {
  return s;
}

export function createCodeMode(options: CodeModeOptions): CodeMode {
  const specs = new Map<string, OperationSpec>();
  const modules = new Map<string, ModuleSpec>();
  const addModule = (m: ModuleSpec) => {
    const name = m.name.replace(/\.js$/, "");
    if (!MODULE_RE.test(name)) throw new Error(`clicodemode: invalid module name "${m.name}" (want scheme:path, e.g. mycli:layout)`);
    if (modules.has(name)) throw new Error(`clicodemode: module "${name}" already registered`);
    modules.set(name, { ...m, name });
    id = undefined; // engine is rebuilt with the new module set on the next call
  };
  let id: number | string | undefined;
  let bridgeP: Promise<Bridge> | undefined;

  const invoke = async (name: string, argsJson: string): Promise<string> => {
    const spec = specs.get(name);
    if (!spec) throw new Error(`unknown operation ${name}`);
    const args = argsJson ? JSON.parse(argsJson) : {};
    const out = await spec.handler(args ?? {});
    return JSON.stringify(out === undefined ? null : out);
  };

  const config = () => ({
    version: options.version,
    operations: [...specs.values()].map((s) => ({
      name: s.name,
      description: s.description,
      input_schema: s.inputSchema,
      output_schema: s.outputSchema,
      side_effects: s.sideEffects ?? "local",
      idempotent: s.idempotent ?? false,
      authorization: s.authorization,
      examples: s.examples,
    })),
    modules: [...modules.values()].map((m) => ({ name: m.name, source: m.source, description: m.description ?? "" })),
    limits: options.limits,
    moduleRoot: options.moduleRoot,
  });

  // Engine is created lazily on first call, with every op registered so far.
  async function ready(): Promise<{ b: Bridge; id: number | string }> {
    bridgeP ??= getBridge();
    const b = await bridgeP;
    if (id === undefined) {
      const made: unknown = b.create(JSON.stringify(config()), invoke);
      if (typeof made !== "number") {
        throw new Error("clicodemode: engine creation failed: " + ((made as any)?.error ?? String(made)));
      }
      id = made;
    }
    return { b, id };
  }

  // Runs one call under a callId so signal/timeout can cancel it in the engine.
  async function call(
    opts: CallOptions,
    start: (b: Bridge, id: number | string, callId: string) => Promise<string>,
  ): Promise<Result> {
    const { signal } = opts;
    const aborted = (): Result => ({ ok: false, value: null, error: "aborted", ms: 0 });
    if (signal?.aborted) return aborted();
    const { b, id } = await ready();
    if (signal?.aborted) return aborted();
    const callId = `c${++callSeq}`;
    const t = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;
    if (!(t >= 0)) throw new Error("clicodemode: timeoutMs must be >= 0");
    const onAbort = () => { b.cancel(callId); };
    signal?.addEventListener("abort", onAbort, { once: true });
    // The timer lives on the JS side: a Go-side timer would keep the Node
    // process alive until it expires even after the call finished.
    let timedOut = false;
    const timer = t > 0 ? setTimeout(() => { timedOut = true; b.cancel(callId); }, t) : undefined;
    try {
      const r = parse<Result>(await start(b, id, callId));
      if (timedOut && !r.ok) r.error = `timed out after ${t}ms (${r.error ?? "cancelled"})`;
      return r;
    } finally {
      if (timer !== undefined) clearTimeout(timer);
      signal?.removeEventListener("abort", onAbort);
    }
  }

  return {
    register(op) {
      if (!NAME_RE.test(op.name)) throw new Error(`clicodemode: invalid operation name "${op.name}" (want dotted segments of letters, digits, _ and -, e.g. health.check)`);
      if (typeof op.handler !== "function") throw new Error(`clicodemode: operation "${op.name}" has no handler`);
      if (specs.has(op.name)) throw new Error(`clicodemode: operation "${op.name}" already registered`);
      specs.set(op.name, op);
      id = undefined; // engine is rebuilt with the new op set on the next call
    },
    registerModule(mod) { addModule(mod); },
    registerModuleFile(scheme, file) {
      const p = file instanceof URL ? fileURLToPath(file) : file;
      if (!/\.(js|json)$/.test(p)) throw new Error(`clicodemode: module file "${p}" must end in .js or .json`);
      addModule({ name: `${scheme}:${basename(p)}`, source: readFileSync(p, "utf8") });
    },
    registerModules(scheme, dirOrFiles) {
      if (typeof dirOrFiles === "object" && !(dirOrFiles instanceof URL)) {
        for (const [rel, source] of Object.entries(dirOrFiles)) {
          if (/\.(js|json)$/.test(rel)) addModule({ name: `${scheme}:${rel.replace(/^\.?\//, "")}`, source });
        }
        return;
      }
      const root = dirOrFiles instanceof URL ? fileURLToPath(dirOrFiles) : dirOrFiles;
      let n = 0;
      const walk = (dir: string) => {
        for (const e of readdirSync(dir, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
          const full = join(dir, e.name);
          if (e.isDirectory()) walk(full);
          else if (/\.(js|json)$/.test(e.name)) {
            n++;
            addModule({ name: `${scheme}:${relative(root, full).split(sep).join("/")}`, source: readFileSync(full, "utf8") });
          }
        }
      };
      walk(root);
      if (n === 0) throw new Error(`clicodemode: no .js or .json files under "${root}"`);
    },
    async operations() { const { b, id } = await ready(); return parse<Operation[]>(b.operations(id)); },
    async describe(name) { const { b, id } = await ready(); return parse<Operation>(b.describe(id, name)); },
    async capabilities() { const { b, id } = await ready(); return parse<Capabilities>(b.capabilities(id)); },
    async docs() { const { b, id } = await ready(); return text(b.docs(id)); },
    async skill() { const { b, id } = await ready(); return text(b.skill(id)); },
    async check(src, file = "<program>") { const { b, id } = await ready(); return withOk(parse<CheckResult>(b.check(id, src, file))); },
    async compile(src, file = "<program>") { const { b, id } = await ready(); return withOk(parse<CheckResult>(b.compile(id, src, file))); },
    run(src, opts = {}) {
      return call(opts, (b, id, callId) =>
        b.run(id, src, opts.file ?? "<program>", JSON.stringify(opts.input ?? null), callId));
    },
    eval(expr, opts = {}) {
      return call(opts, (b, id, callId) =>
        b.eval(id, expr, JSON.stringify(opts.input ?? null), callId));
    },
  };
}
