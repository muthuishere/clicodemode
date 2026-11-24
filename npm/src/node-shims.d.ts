// Minimal Node typings so the package needs no @types/node.
declare module "node:fs" {
  export function readFileSync(path: string | URL): Uint8Array;
  export function readFileSync(path: string | URL, enc: "utf8"): string;
  export function readdirSync(path: string, opts: { withFileTypes: true }): { name: string; isDirectory(): boolean }[];
}
declare module "node:path" {
  export function basename(p: string): string;
  export function join(...p: string[]): string;
  export function relative(from: string, to: string): string;
  export const sep: string;
}
declare module "node:url" {
  export function fileURLToPath(u: string | URL): string;
}

declare const WebAssembly: {
  instantiate(bytes: any, imports?: any): Promise<{ instance: any }>;
};
declare function setTimeout(fn: (...a: any[]) => void, ms: number): unknown;
declare function clearTimeout(t: unknown): void;
declare class URL { constructor(u: string, base?: string | URL); }
interface ImportMeta { url: string }
