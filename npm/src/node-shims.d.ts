// Minimal Node typings so the package needs no @types/node.
declare module "node:fs" {
  export function readFileSync(path: string | URL): Uint8Array;
}
declare module "node:url" {
  export function fileURLToPath(u: string | URL): string;
}

declare const WebAssembly: {
  instantiate(bytes: any, imports?: any): Promise<{ instance: any }>;
};
declare function setTimeout(fn: (...a: any[]) => void, ms: number): unknown;
declare class URL { constructor(u: string, base?: string | URL); }
interface ImportMeta { url: string }
