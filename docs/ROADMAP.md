# Roadmap

Each step ships on its own, with tests.

1. **v0.1 (done)**: registry, describe/operations/capabilities + fingerprint,
   interpreter (sync + async + modules), limits, schema validation, policy,
   CLI `operations|describe|capabilities|docs|check|compile|run|eval`, demo.
2. **Adapters for existing command trees (done)**: `adapters/cmdtree`
   `FromRunner` / `FromFlagSet` (stdlib only; cobra wiring documented). Original scope: helpers that turn a cobra/urfave
   command (or a `func(args []string) error` runner capturing stdout) into an
   Operation without duplicating the handler. Proving ground: AOS, after the
   AOS owner agrees (see COMPATIBILITY.md).
3. **exec + streams (done, v0.2)**: `exec(argv)` against a host
   allowlist, `execStream`, `.lines() .jsonl() .json() .csv() .yaml()`,
   `.map .filter .take .reduce .writeJSONL()`, backpressure, kill on cancel;
   `pmap` with bounded concurrency; `cwd` confined to `ExecPolicy.Dir`. Shell
   only behind a separate policy switch.
4. **workflow primitives (done, v0.3)** except typed outputs: `workflow`,
   `step` (needs, when, retry, timeout, maxParallel, bounded foreach with
   parallel, while/until with required maxIterations, continueOnError);
   imperative `.run()`; declarative `.plan()` and `.toYAML()` (WFNexus field
   subset, steps as `<cli> code run-step ./module.js#export`, served by the
   `run-step` subcommand). Step refs come from export metadata recorded by
   the interpreter. Next: round-trip the YAML through WFNexus (item 6).
5. **AOS (done)**: `aos code` registers every batch verb as an
   operation through the same `cli.Invoke`; JSON-first results; `aos batch`
   untouched (run-phase golden tests). Shipped in aos v0.15.1; on clicodemode v0.6.2 since aos v0.15.2.
6. **WFNexus (done, wfnexus#187)**: exported plans pass WFNexus's parser and dry
   run, and ran for real (outcomes via exit codes, step context via inline
   `run:` assignments).
7. **Caches (done)**: capability cache (version + fingerprint + auth context),
   compiled cache (program/import hashes + compiler + capability hash),
   `Engine.Cache` / `CODEMODE_CACHE_DIR`.
8. **Optional MCP adapter (done)**; generated per-CLI skill (done: `code skill`,
   grouped for large CLIs).
9. **ctx-optimize (done, shipped in ctx-optimize v0.16.1)**: `ctx-optimize code` over its read-only
   verbs with `cmdtree.WithJSONResult` and `WithFlagsLast`.
10. **Sessions and language (done, v0.7.0)**: `Engine.NewSession` keeps one
   top-level scope across runs (per-run timeout and cancel, detach and `Poll`,
   captured console events, `Vars`/`Reset`); regex on RE2, `switch`, classes
   with `new`/`this`/`super`/`instanceof`, `Map`/`Set`/`Date`, tagged
   templates. Built for karya's agent REPL; approval, quoting and output caps
   stay with the host.
11. **Host modules (done, v0.8.0)**: a CLI ships JS modules in its build (a
   folder, one file or content, in all four languages) and agents import them
   as `scheme:path`. Next: move aos's composed batch recipes into modules.

Next candidates: a benchmark measuring model turns and tokens with a real agent,
`code run --plan` with a host approval hook for external side effects,
`code run --stats`, a `codemode init` scaffolder.

Known interpreter gaps to close as needed: labelled statements, getters/setters,
regex lookaround, live module bindings, microtask ordering. Sessions are Go-only
for now; the Python, TypeScript and Rust wrappers get the language changes.
