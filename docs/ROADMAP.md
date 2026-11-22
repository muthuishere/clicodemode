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
5. **AOS batch adapter**: golden tests first (inside AOS), then the AOS-side
   registration so JS calls wrap `cli.Invoke`. Old YAML byte-identical.
6. **WFNexus plan adapter**: emitted plans validated by WFNexus's own parser.
7. **Caches (done)**: capability cache (version + fingerprint + auth context),
   compiled cache (program/import hashes + compiler + capability hash),
   `Engine.Cache` / `CODEMODE_CACHE_DIR`.
8. **Optional MCP adapter**; generated per-CLI skill (done: `code skill`).

Known interpreter gaps to close as needed: labelled statements, `switch`,
regex, getters/setters, live module bindings, microtask ordering.
