import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

/** The string-in/string-out bridge the Go side installs on globalThis.__codemode. */
export interface Bridge {
  create(configJson: string, invoke: (name: string, argsJson: string) => Promise<string>): number | string;
  operations(id: number | string): string;
  describe(id: number | string, name: string): string;
  capabilities(id: number | string): string;
  check(id: number | string, src: string, file: string): string;
  compile(id: number | string, src: string, file: string): string;
  docs(id: number | string): string;
  skill(id: number | string): string;
  version(): string;
  run(id: number | string, src: string, file: string, inputJson: string, callId?: string, timeoutMs?: string): Promise<string>;
  eval(id: number | string, expr: string, inputJson: string, callId?: string, timeoutMs?: string): Promise<string>;
  /** Abort the running call named callId; true if one was running. */
  cancel(callId: string): boolean;
}

let bridge: Promise<Bridge> | undefined;

/**
 * Seam for other runtimes: give it the wasm bytes and it boots the Go runtime.
 * A browser/bundler entry can fetch() the bytes and import wasm_exec.js itself,
 * then call this.
 */
export async function loadWasm(bytes: Uint8Array | ArrayBuffer): Promise<Bridge> {
  const g = globalThis as any;
  if (!g.Go) throw new Error("clicodemode: wasm_exec.js has not defined globalThis.Go");
  const go = new g.Go();
  const { instance } = await WebAssembly.instantiate(bytes, go.importObject);
  void go.run(instance); // never resolves while the bridge is alive
  for (let i = 0; i < 2000 && !g.__codemode; i++) await new Promise((r) => setTimeout(r, 5));
  if (!g.__codemode) throw new Error("clicodemode: wasm started but globalThis.__codemode never appeared");
  return g.__codemode as Bridge;
}

/** Node ESM loader; one wasm module per process. */
export function getBridge(): Promise<Bridge> {
  bridge ??= (async () => {
    if ((globalThis as any).__codemode) return (globalThis as any).__codemode as Bridge;
    await import("./wasm_exec.js" as string);
    const wasmPath = fileURLToPath(new URL("./codemode.wasm", import.meta.url));
    return loadWasm(readFileSync(wasmPath));
  })();
  bridge.catch(() => { bridge = undefined; });
  return bridge;
}
