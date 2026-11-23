export type SideEffect = "none" | "local" | "external";

export interface Example {
  description?: string;
  args?: Record<string, unknown>;
  result?: unknown;
}

export type Handler = (args: Record<string, any>) => unknown | Promise<unknown>;

/** What you pass to register(). Mirrors Go codemode.Operation. */
export interface OperationSpec {
  name: string; // dotted lower-case, e.g. "health.check"
  description: string;
  inputSchema?: unknown;
  outputSchema?: unknown;
  sideEffects?: SideEffect; // default "local"
  idempotent?: boolean;
  authorization?: string;
  examples?: Example[];
  handler: Handler;
}

/** Operation as the engine reports it (Go JSON, snake_case). */
export interface Operation {
  name: string;
  description: string;
  input_schema?: unknown;
  output_schema?: unknown;
  side_effects: SideEffect;
  idempotent: boolean;
  authorization?: string;
  examples?: Example[];
}

export interface Diagnostic {
  severity: "error" | "warning";
  file: string;
  line: number;
  col: number;
  message: string;
  hint?: string;
}

export interface LanguageInfo {
  name: string;
  features: string[];
  unsupported: string[];
  globals: string[];
}

export interface Capabilities {
  spec: string;
  version: string;
  fingerprint: string;
  language: LanguageInfo;
  operations: Operation[];
}

export interface Result {
  ok: boolean;
  value: unknown;
  error?: string;
  diagnostics?: Diagnostic[];
  ms: number;
  /** print/console.log output of the program. */
  stdout?: string;
}

/** Result of check(): diagnostics plus the operations the program uses. */
export interface CheckResult {
  ok: boolean;
  diagnostics?: Diagnostic[];
  /** compile() only: program metadata (null on a parse error). */
  program?: { file: string; hash: string; imports: string[] | null; operations: string[] | null; cache_key: string } | null;
  [k: string]: unknown;
}

export interface Limits {
  max_steps?: number;
  max_call_depth?: number;
  max_concurrency?: number;
  timeout_ms?: number;
}

export interface CodeModeOptions {
  version: string;
  limits?: Limits;
  moduleRoot?: string;
}
